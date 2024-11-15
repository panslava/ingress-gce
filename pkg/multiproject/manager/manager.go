package manager

import (
	"fmt"
	"sync"

	"k8s.io/client-go/kubernetes"
	"k8s.io/ingress-gce/pkg/multiproject/neg"
	"k8s.io/ingress-gce/pkg/multiproject/projectcrd"
	"k8s.io/ingress-gce/pkg/multiproject/sharedcontext"
	"k8s.io/klog/v2"
)

type ProjectControllersManager struct {
	controllers   map[int64]*ControllerSet
	mu            sync.Mutex
	logger        klog.Logger
	sharedContext sharedcontext.SharedContext
}

type ControllerSet struct {
	stopCh chan struct{}
}

func NewProjectControllerManager(
	kubeClient kubernetes.Interface,
	sharedContext sharedcontext.SharedContext,
	logger klog.Logger,
) *ProjectControllersManager {
	return &ProjectControllersManager{
		controllers:   make(map[int64]*ControllerSet),
		logger:        logger,
		sharedContext: sharedContext,
	}
}

func (pcm *ProjectControllersManager) StartControllersForProject(proj *projectcrd.Project) error {
	pcm.mu.Lock()
	defer pcm.mu.Unlock()

	if _, exists := pcm.controllers[proj.ProjectNumber()]; exists {
		pcm.logger.Info("Controllers for project already exist, skipping start", "projectId", proj.ProjectNumber())
		return nil
	}

	negControllerStopCh, err := neg.StartNEGController(&pcm.sharedContext, pcm.logger, proj)
	if err != nil {
		return fmt.Errorf("failed to start NEG controller for project %d: %v", proj.ProjectNumber(), err)
	}
	pcm.controllers[proj.ProjectNumber()] = &ControllerSet{
		stopCh: negControllerStopCh,
	}

	pcm.logger.Info("Started controllers for project", "projectId", proj.ProjectNumber)
	return nil
}

func (pcm *ProjectControllersManager) StopControllersForProject(proj *projectcrd.Project) error {
	pcm.mu.Lock()
	defer pcm.mu.Unlock()

	_, exists := pcm.controllers[proj.ProjectNumber()]
	if !exists {
		pcm.logger.Info("Controllers for project do not exist", "projectId", proj.ProjectNumber())
		return nil
	}

	close(pcm.controllers[proj.ProjectNumber()].stopCh)

	delete(pcm.controllers, proj.ProjectNumber())
	pcm.logger.Info("Stopped controllers for project", "projectId", proj.ProjectNumber)
	return nil
}
