package instancegroups

import (
	"testing"

	"google.golang.org/api/compute/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

func newTestController(f *FakeInstanceGroups, zone string) *Controller {
	return NewController(&ControllerConfig{
		Cloud:      f,
		Namer:      defaultNamer,
		ZoneLister: &FakeZoneLister{[]string{zone}},
	})
}

func TestSetNamedPorts(t *testing.T) {
	maxIGSize := 1000
	zonesToIGs := map[string]IGsToInstances{
		defaultZone: {
			&compute.InstanceGroup{Name: "ig"}: sets.NewString("ig"),
		},
	}
	fakeIGs := NewFakeInstanceGroups(zonesToIGs, maxIGSize)
	controller := newTestController(fakeIGs, defaultZone)

	testCases := []struct {
		activePorts   []int64
		expectedPorts []int64
	}{
		{
			// Verify setting a port works as expected.
			[]int64{80},
			[]int64{80},
		},
		{
			// Utilizing multiple new ports
			[]int64{81, 82},
			[]int64{80, 81, 82},
		},
		{
			// Utilizing existing ports
			[]int64{80, 82},
			[]int64{80, 81, 82},
		},
		{
			// Utilizing a new port and an old port
			[]int64{80, 83},
			[]int64{80, 81, 82, 83},
		},
		// TODO: Add tests to remove named ports when we support that.
	}
	for _, testCase := range testCases {
		igs, err := controller.EnsureInstanceGroupsAndPorts("ig", testCase.activePorts)
		if err != nil {
			t.Fatalf("unexpected error in setting ports %v to instance group: %s", testCase.activePorts, err)
		}
		if len(igs) != 1 {
			t.Fatalf("expected a single instance group, got: %v", igs)
		}
		actualPorts := igs[0].NamedPorts
		if len(actualPorts) != len(testCase.expectedPorts) {
			t.Fatalf("unexpected named ports on instance group. expected: %v, got: %v", testCase.expectedPorts, actualPorts)
		}
		for i, p := range igs[0].NamedPorts {
			if p.Port != testCase.expectedPorts[i] {
				t.Fatalf("unexpected named ports on instance group. expected: %v, got: %v", testCase.expectedPorts, actualPorts)
			}
		}
	}
}
