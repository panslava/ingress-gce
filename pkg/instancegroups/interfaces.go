package instancegroups

import (
	"google.golang.org/api/compute/v1"
	"k8s.io/ingress-gce/pkg/utils"
)

// ZoneLister manages lookups for GCE instance groups/instances to zones.
type ZoneLister interface {
	ListZones(predicate utils.NodeConditionPredicate) ([]string, error)
	GetZoneForNode(name string) (string, error)
}

type IG interface {

	// TODO: Refactor for modulatiry.
	ListInstancesInInstanceGroup(name, zone string, state string) ([]*compute.InstanceWithNamedPorts, error)
	AddInstancesToInstanceGroup(name, zone string, instanceRefs []*compute.InstanceReference) error
	RemoveInstancesFromInstanceGroup(name, zone string, instanceRefs []*compute.InstanceReference) error
	ToInstanceReferences(zone string, instanceNames []string) (refs []*compute.InstanceReference)
}

// CloudProvider is an interface for managing gce instances groups, and the instances therein.
type CloudProvider interface {
	GetInstanceGroup(name, zone string) (*compute.InstanceGroup, error)
	CreateInstanceGroup(ig *compute.InstanceGroup, zone string) error
	DeleteInstanceGroup(name, zone string) error
	ListInstanceGroups(zone string) ([]*compute.InstanceGroup, error)

	// TODO: Refactor for modulatiry.
	ListInstancesInInstanceGroup(name, zone string, state string) ([]*compute.InstanceWithNamedPorts, error)
	AddInstancesToInstanceGroup(name, zone string, instanceRefs []*compute.InstanceReference) error
	RemoveInstancesFromInstanceGroup(name, zone string, instanceRefs []*compute.InstanceReference) error
	ToInstanceReferences(zone string, instanceNames []string) (refs []*compute.InstanceReference)
	SetNamedPortsOfInstanceGroup(igName, zone string, namedPorts []*compute.NamedPort) error
}
