package gce

import (
	"k8s.io/cloud-provider-gcp/providers/gce"
	"k8s.io/ingress-gce/pkg/multiproject/crd"
	"k8s.io/klog/v2"
)

func NewGCEClientForProject(logger klog.Logger, proj *crd.Project) (*gce.Cloud, error) {
	logger.Info("Creating GCE client for project", "projectId", proj.ProjectID)

	panic("Not implemented")
}
