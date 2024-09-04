package store

import (
	"k8s.io/cloud-provider-gcp/providers/gce"
	"k8s.io/ingress-gce/pkg/projectcloud/crd"
)

// CloudProjectReader defines the interface for reading cloud information for a project.
type CloudProjectReader interface {
	CloudForProject(project *crd.Project) (*gce.Cloud, error)
}

// CloudProjectWriter defines the interface for writing cloud information for a project.
type CloudProjectWriter interface {
	StoreCloudForProject(project *crd.Project, cloud *gce.Cloud) error
	DeleteCloudForProject(project *crd.Project) error
}

// CloudStorer extends CloudProjectReader with methods to store and delete cloud information.
type CloudStorer interface {
	CloudProjectReader
	CloudProjectWriter
}
