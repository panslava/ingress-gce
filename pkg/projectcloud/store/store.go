package store

import (
	"fmt"
	"sync"

	"k8s.io/cloud-provider-gcp/providers/gce"
	"k8s.io/ingress-gce/pkg/projectcloud/crd"
)

// CloudStore is a thread-safe store for managing cloud instances associated with projects.
type CloudStore struct {
	mu    sync.RWMutex
	store map[string]*gce.Cloud
}

// NewCloudStore creates a new instance of CloudStore.
func NewCloudStore() *CloudStore {
	return &CloudStore{
		store: make(map[string]*gce.Cloud),
	}
}

// CloudForProject retrieves the cloud instance for the given project.
func (s *CloudStore) CloudForProject(project *crd.Project) (*gce.Cloud, error) {
	if project == nil {
		return nil, fmt.Errorf("project is nil")
	}
	key := keyForProject(*project)
	s.mu.RLock()
	defer s.mu.RUnlock()

	cloud, exists := s.store[key]
	if !exists {
		return nil, nil
	}
	return cloud, nil
}

// StoreCloudForProject stores the cloud instance for the given project.
func (s *CloudStore) StoreCloudForProject(project *crd.Project, cloud *gce.Cloud) error {
	if project == nil {
		return fmt.Errorf("project is nil")
	}
	if cloud == nil {
		return fmt.Errorf("cannot store nil cloud for project: %s", project.ProjectID)
	}
	key := keyForProject(*project)
	s.mu.Lock()
	defer s.mu.Unlock()

	s.store[key] = cloud
	return nil
}

// DeleteCloudForProject deletes the cloud instance for the given project.
func (s *CloudStore) DeleteCloudForProject(project *crd.Project) error {
	if project == nil {
		return fmt.Errorf("project is nil")
	}
	key := keyForProject(*project)
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.store[key]; !exists {
		return fmt.Errorf("cloud not found for project: %s", project.ProjectID)
	}
	delete(s.store, key)
	return nil
}

// keyForProject generates a unique key for the given project.
func keyForProject(project crd.Project) string {
	return project.ProjectID
}
