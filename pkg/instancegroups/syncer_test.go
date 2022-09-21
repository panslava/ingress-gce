package instancegroups

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/api/compute/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/ingress-gce/pkg/test"
	"k8s.io/ingress-gce/pkg/utils/namer"
)

const (
	defaultZone = "default-zone"
	basePath    = "/basepath/projects/project-id/"
)

var defaultNamer = namer.NewNamer("uid1", "fw1")

func newTestSyncer(f *FakeInstanceGroups, zone string, maxIGSize int) *Syncer {
	return NewSyncer(&SyncerConfig{
		Cloud:      f,
		Namer:      defaultNamer,
		ZoneLister: &FakeZoneLister{[]string{zone}},
		Recorder:   record.NewFakeRecorder(100),
		BasePath:   basePath,
		MaxIGSize:  maxIGSize,
	})
}

func TestSync(t *testing.T) {
	maxIGSize := 1000

	names1001 := make([]string, maxIGSize+1)
	for i := 1; i <= maxIGSize+1; i++ {
		names1001[i-1] = fmt.Sprintf("n%d", i)
	}

	testCases := []struct {
		gceNodes       sets.String
		kubeNodes      sets.String
		shouldSkipSync bool
	}{
		{
			gceNodes:  sets.NewString("n1"),
			kubeNodes: sets.NewString("n1", "n2"),
		},
		{
			gceNodes:  sets.NewString("n1, n2"),
			kubeNodes: sets.NewString("n1"),
		},
		{
			gceNodes:       sets.NewString("n1", "n2"),
			kubeNodes:      sets.NewString("n1", "n2"),
			shouldSkipSync: true,
		},
		{
			gceNodes:  sets.NewString(),
			kubeNodes: sets.NewString(names1001...),
		},
		{
			gceNodes:  sets.NewString("n0", "n1"),
			kubeNodes: sets.NewString(names1001...),
		},
	}

	for _, testCase := range testCases {
		// create fake gce instance groups with existing gceNodes
		igName := defaultNamer.InstanceGroup()
		ig := &compute.InstanceGroup{Name: igName}
		zonesToIGs := ZonesToIGsToInstances{
			defaultZone: {
				ig: testCase.gceNodes,
			},
		}
		fakeGCEInstanceGroups := NewFakeInstanceGroups(zonesToIGs, maxIGSize)

		igSyncer := newTestSyncer(fakeGCEInstanceGroups, defaultZone, maxIGSize)
		// run sync with expected kubeNodes
		apiCallsCountBeforeSync := len(fakeGCEInstanceGroups.calls)
		err := igSyncer.Sync(testCase.kubeNodes.List())
		if err != nil {
			t.Fatalf("igSyncer.Sync(%v) returned error %v, want nil", testCase.kubeNodes.List(), err)
		}

		// run assertions
		apiCallsCountAfterSync := len(fakeGCEInstanceGroups.calls)
		if testCase.shouldSkipSync && apiCallsCountBeforeSync != apiCallsCountAfterSync {
			t.Errorf("Should skip sync. apiCallsCountBeforeSync = %d, apiCallsCountAfterSync = %d", apiCallsCountBeforeSync, apiCallsCountAfterSync)
		}

		instancesList, err := fakeGCEInstanceGroups.ListInstancesInInstanceGroup(ig.Name, defaultZone, allInstances)
		if err != nil {
			t.Fatalf("fakeGCEInstanceGroups.ListInstancesInInstanceGroup(%s, %s, %s) returned error %v, want nil", ig.Name, defaultZone, allInstances, err)
		}
		instances, err := test.InstancesListToNameSet(instancesList)
		if err != nil {
			t.Fatalf("test.InstancesListToNameSet(%v) returned error %v, want nil", ig, err)
		}

		expectedInstancesSize := testCase.kubeNodes.Len()
		if testCase.kubeNodes.Len() > maxIGSize {
			// If kubeNodes bigger than maximum instance group size, resulted instances
			// should be truncated to flags.F.MaxIgSize
			expectedInstancesSize = maxIGSize
		}
		if instances.Len() != expectedInstancesSize {
			t.Errorf("instances.Len() = %d not equal expectedInstancesSize = %d", instances.Len(), expectedInstancesSize)
		}
		if !testCase.kubeNodes.IsSuperset(instances) {
			t.Errorf("kubeNodes = %v is not superset of instances = %v", testCase.kubeNodes, instances)
		}

		// call sync one more time and check that it will be no-op and will not cause any api calls
		apiCallsCountBeforeSync = len(fakeGCEInstanceGroups.calls)
		err = igSyncer.Sync(testCase.kubeNodes.List())
		if err != nil {
			t.Fatalf("igSyncer.Sync(%v) returned error %v, want nil", testCase.kubeNodes.List(), err)
		}
		apiCallsCountAfterSync = len(fakeGCEInstanceGroups.calls)
		if apiCallsCountBeforeSync != apiCallsCountAfterSync {
			t.Errorf("Should skip sync if called second time with the same kubeNodes. apiCallsCountBeforeSync = %d, apiCallsCountAfterSync = %d", apiCallsCountBeforeSync, apiCallsCountAfterSync)
		}
	}
}

func TestGetInstanceReferences(t *testing.T) {
	maxIGSize := 1000
	zonesToIGs := map[string]IGsToInstances{
		defaultZone: {
			&compute.InstanceGroup{Name: "ig"}: sets.NewString("ig"),
		},
	}
	fakeGCE := NewFakeInstanceGroups(zonesToIGs, maxIGSize)
	syncer := newTestSyncer(fakeGCE, defaultZone, maxIGSize)

	nodeNames := []string{"node-1", "node-2", "node-3", "node-4.region.zone"}

	expectedRefs := map[string]struct{}{}
	for _, nodeName := range nodeNames {
		name := strings.Split(nodeName, ".")[0]
		expectedRefs[fmt.Sprintf("%szones/%s/instances/%s", basePath, defaultZone, name)] = struct{}{}
	}

	refs := syncer.getInstancesReferences(defaultZone, nodeNames)
	for _, ref := range refs {
		if _, ok := expectedRefs[ref.Instance]; !ok {
			t.Errorf("found unexpected reference: %s, expected only %+v", ref.Instance, expectedRefs)
		}
	}
}
