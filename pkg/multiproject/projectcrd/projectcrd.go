package projectcrd

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/ingress-gce/pkg/flags"
)

// Project is the Schema for the Project resource in the Multi-Project cluster.
type Project struct {
	metav1.TypeMeta
	metav1.ObjectMeta

	Spec   ProjectSpec
	Status ProjectStatus
}

// ProjectName returns the name of the project which this CRD represents.
func (p *Project) ProjectName() string {
	return p.ObjectMeta.Labels[flags.F.MultiProjectCRDProjectNameLabel]
}

func (p *Project) ProjectID() string {
	return p.Spec.ProjectID
}

func (p *Project) ProjectNumber() int64 {
	return p.Spec.ProjectNumber
}

func (p *Project) NetworkURL() string {
	return p.Spec.NetworkConfig.Network
}

func (p *Project) SubnetworkURL() string {
	return p.Spec.NetworkConfig.DefaultSubnetwork
}

// ProjectList contains a list of Projects.
type ProjectList struct {
	metav1.TypeMeta
	metav1.ListMeta
	Items []Project
}

// ProjectSpec specifies the desired state of the project in the MT cluster.
type ProjectSpec struct {
	// ProjectNumber is the GCP project number where the project exists.
	ProjectNumber int64
	// ProjectID is the GCP project ID where the project exists.
	ProjectID string
	// NetworkConfig specifies the network configuration for the project.
	NetworkConfig NetworkConfig
}

// NetworkConfig specifies the network configuration for the project.
type NetworkConfig struct {
	Network           string
	DefaultSubnetwork string
}

// ProjectStatus stores the observed state of the project.
type ProjectStatus struct {
	// Conditions describe the current state of the Project.
	Conditions []metav1.Condition
}
