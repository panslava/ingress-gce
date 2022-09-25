/*
Copyright 2015 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package instancegroups

import (
	compute "google.golang.org/api/compute/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/ingress-gce/pkg/utils"
)

// Manager is an interface to sync kubernetes nodes to google cloud instance groups
// through the Provider interface. It handles zones opaquely using the zoneLister.
type Manager interface {
	EnsureInstanceGroupsAndPorts(ports []int64) ([]*compute.InstanceGroup, error)
	DeleteInstanceGroup() error

	Get(name, zone string) (*compute.InstanceGroup, error)
	List() ([]*compute.InstanceGroup, error)
	ListZonal(zone string) ([]*compute.InstanceGroup, error)

	Sync(nodeNames []string) error
}

// ZoneLister manages lookups for GCE instance groups/instances to zones.
type ZoneLister interface {
	ListZones(predicate utils.NodeConditionPredicate) ([]string, error)
	GetZoneForNode(name string) (string, error)
}

// Provider is an interface for managing gce instances groups, and the instances therein.
type Provider interface {
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

type recorderSource interface {
	Recorder(ns string) record.EventRecorder
}
