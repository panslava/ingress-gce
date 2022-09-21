package instancegroups

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"google.golang.org/api/compute/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/ingress-gce/pkg/events"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/klog/v2"
)

const (
	// State string required by gce library to list all instances.
	allInstances = "ALL"
)

type Syncer struct {
	zoneLister ZoneLister
	cloud      CloudProvider
	namer      namer.BackendNamer
	recorder   record.EventRecorder

	instanceLinkFormat string
	maxIGSize          int
}

type SyncerConfig struct {
	ZoneLister ZoneLister
	Cloud      CloudProvider
	Namer      namer.BackendNamer
	Recorder   record.EventRecorder

	BasePath  string
	MaxIGSize int
}

func NewSyncer(config *SyncerConfig) *Syncer {
	return &Syncer{
		zoneLister:         config.ZoneLister,
		cloud:              config.Cloud,
		namer:              config.Namer,
		recorder:           config.Recorder,
		instanceLinkFormat: config.BasePath + "zones/%s/instances/%s",
		maxIGSize:          config.MaxIGSize,
	}
}

// Sync Kubernetes nodes with the GCP instances in the instance groups.
func (s *Syncer) Sync(nodes []string) (err error) {
	klog.V(2).Infof("Syncing nodes %v", nodes)

	defer func() {
		// The node list is only responsible for syncing nodes to instance
		// groups. It never creates/deletes, so if an instance groups is
		// not found there's nothing it can do about it anyway. Most cases
		// this will happen because the backend list has deleted the instance
		// group, however if it happens because a user deletes the IG by mistake
		// we should just wait till the backend list fixes it.
		if utils.IsHTTPErrorCode(err, http.StatusNotFound) {
			klog.Infof("Node list encountered a 404, ignoring: %v", err)
			err = nil
		}
	}()

	clusterIGs, err := s.listClusterIGs()
	if err != nil {
		klog.Errorf("List error: %v", err)
		return err
	}

	for _, igName := range clusterIGs {
		gceNodes := sets.NewString()
		gceNodes, err = s.listIGInstancesInAllZones(igName)
		if err != nil {
			klog.Errorf("listIGInstancesInAllZones(%q) error: %v", igName, err)
			return err
		}
		kubeNodes := sets.NewString(nodes...)

		// Individual InstanceGroup has a limit for 1000 instances in Google Cloud.
		// As a result, it's not possible to add more to it.
		if len(kubeNodes) > s.maxIGSize {
			// List() will return a sorted list so the kubeNodesList truncation will have a stable set of nodes.
			kubeNodesList := kubeNodes.List()

			// Store first 10 truncated nodes for logging
			truncateForLogs := func(nodes []string) []string {
				maxLogsSampleSize := 10
				if len(nodes) <= maxLogsSampleSize {
					return nodes
				}
				return nodes[:maxLogsSampleSize]
			}

			klog.Warningf("Total number of kubeNodes: %d, truncating to maximum Instance Group size = %d. "+
				"Instance group name: %s. First truncated instances: %v",
				len(kubeNodesList), s.maxIGSize, igName, truncateForLogs(nodes[s.maxIGSize:]))
			kubeNodes = sets.NewString(kubeNodesList[:s.maxIGSize]...)
		}

		// A node deleted via kubernetes could still exist as a gce vm. We don't
		// want to route requests to it. Similarly, a node added to kubernetes
		// needs to get added to the instance group, so we do route requests to it.
		removeNodes := gceNodes.Difference(kubeNodes).List()
		addNodes := kubeNodes.Difference(gceNodes).List()

		klog.V(2).Infof("Removing %d, adding %d nodes", len(removeNodes), len(addNodes))

		start := time.Now()
		if len(removeNodes) != 0 {
			err = s.remove(igName, removeNodes)
			klog.V(2).Infof("Remove(%q, _) = %v (took %s); nodes = %v", igName, err, time.Now().Sub(start), removeNodes)
			if err != nil {
				return err
			}
		}

		start = time.Now()
		if len(addNodes) != 0 {
			err = s.add(igName, addNodes)
			klog.V(2).Infof("Add(%q, _) = %v (took %s); nodes = %v", igName, err, time.Now().Sub(start), addNodes)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// listClusterIGs lists the names of all InstanceGroups belonging to this cluster.
func (s *Syncer) listClusterIGs() ([]string, error) {
	var igs []*compute.InstanceGroup

	zones, err := s.zoneLister.ListZones(utils.AllNodesPredicate)
	if err != nil {
		return nil, err
	}

	for _, zone := range zones {
		igsForZone, err := s.cloud.ListInstanceGroups(zone)
		if err != nil {
			return nil, err
		}

		for _, ig := range igsForZone {
			igs = append(igs, ig)
		}
	}

	var names []string
	for _, ig := range igs {
		if s.namer.NameBelongsToCluster(ig.Name) {
			names = append(names, ig.Name)
		}
	}

	return names, nil
}

// listIGInstancesInAllZones returns names of all instances in all zones belonging to the instance group
func (s *Syncer) listIGInstancesInAllZones(name string) (sets.String, error) {
	nodeNames := sets.NewString()
	zones, err := s.zoneLister.ListZones(utils.AllNodesPredicate)
	if err != nil {
		return nodeNames, err
	}

	for _, zone := range zones {
		instances, err := s.cloud.ListInstancesInInstanceGroup(name, zone, allInstances)
		if err != nil {
			return nodeNames, err
		}
		for _, ins := range instances {
			name, err := utils.KeyName(ins.Instance)
			if err != nil {
				return nodeNames, err
			}
			nodeNames.Insert(name)
		}
	}
	return nodeNames, nil
}

// remove instances from the appropriately zoned Instance Group.
func (s *Syncer) remove(groupName string, names []string) error {
	events.GlobalEventf(s.recorder, core.EventTypeNormal, events.RemoveNodes, "Removing %s from InstanceGroup %q", events.TruncatedStringList(names), groupName)
	var errs []error
	for zone, nodeNames := range s.splitNodesByZone(names) {
		klog.V(1).Infof("Removing nodes %v from %v in zone %v", nodeNames, groupName, zone)
		if err := s.cloud.RemoveInstancesFromInstanceGroup(groupName, zone, s.getInstancesReferences(zone, nodeNames)); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}

	err := fmt.Errorf("RemoveInstances: %v", errs)
	events.GlobalEventf(s.recorder, core.EventTypeWarning, events.RemoveNodes, "Error removing nodes %s from InstanceGroup %q: %v", events.TruncatedStringList(names), groupName, err)
	return err
}

// add given instances to the appropriately zoned Instance Group.
func (s *Syncer) add(groupName string, names []string) error {
	events.GlobalEventf(s.recorder, core.EventTypeNormal, events.AddNodes, "Adding %s to InstanceGroup %q", events.TruncatedStringList(names), groupName)
	var errs []error
	for zone, nodeNames := range s.splitNodesByZone(names) {
		klog.V(1).Infof("Adding nodes %v to %v in zone %v", nodeNames, groupName, zone)
		if err := s.cloud.AddInstancesToInstanceGroup(groupName, zone, s.getInstancesReferences(zone, nodeNames)); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}

	err := fmt.Errorf("AddInstances: %v", errs)
	events.GlobalEventf(s.recorder, core.EventTypeWarning, events.AddNodes, "Error adding %s to InstanceGroup %q: %v", events.TruncatedStringList(names), groupName, err)
	return err
}

// splitNodesByZones takes a list of node names and returns a map of zone:node names.
// It figures out the zones by asking the zoneLister.
func (s *Syncer) splitNodesByZone(names []string) map[string][]string {
	nodesByZone := map[string][]string{}
	for _, name := range names {
		zone, err := s.zoneLister.GetZoneForNode(name)
		if err != nil {
			klog.Errorf("Failed to get zones for %v: %v, skipping", name, err)
			continue
		}
		if _, ok := nodesByZone[zone]; !ok {
			nodesByZone[zone] = []string{}
		}
		nodesByZone[zone] = append(nodesByZone[zone], name)
	}
	return nodesByZone
}

// getInstancesReferences creates and returns the instance references by generating the
// expected instance URLs
func (s *Syncer) getInstancesReferences(zone string, nodeNames []string) (refs []*compute.InstanceReference) {
	for _, nodeName := range nodeNames {
		refs = append(refs, &compute.InstanceReference{Instance: fmt.Sprintf(s.instanceLinkFormat, zone, canonicalizeInstanceName(nodeName))})
	}
	return refs
}

// canonicalizeInstanceNeme take a GCE instance 'hostname' and break it down
// to something that can be fed to the GCE API client library.  Basically
// this means reducing 'kubernetes-node-2.c.my-proj.internal' to
// 'kubernetes-node-2' if necessary.
// Helper function is copied from legacy-cloud-provider gce_utils.go
func canonicalizeInstanceName(name string) string {
	ix := strings.Index(name, ".")
	if ix != -1 {
		name = name[:ix]
	}
	return name
}
