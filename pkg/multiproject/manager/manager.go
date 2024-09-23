package manager

import (
	"fmt"
	"sync"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	ingctx "k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/flags"
	"k8s.io/ingress-gce/pkg/multiproject/crd"
	"k8s.io/ingress-gce/pkg/multiproject/gce"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/klog/v2"
)

type sharedContext struct {
	kubeClient                kubernetes.Interface
	defaultBackendServicePort utils.ServicePort
}

type ProjectControllerManager struct {
	controllers      map[string]*ControllerSet
	mu               sync.Mutex
	logger           klog.Logger
	informersFactory informers.SharedInformerFactory
	sharedContext    sharedContext
}

type ControllerSet struct {
	// Add fields for the controllers you need to manage
}

func NewProjectControllerManager(kubeClient kubernetes.Interface, logger klog.Logger) *ProjectControllerManager {
	return &ProjectControllerManager{
		controllers:      make(map[string]*ControllerSet),
		logger:           logger,
		informersFactory: informers.NewSharedInformerFactory(kubeClient, 0),
		sharedContext: sharedContext{
			kubeClient: kubeClient,
		},
	}
}

func (pcm *ProjectControllerManager) StartControllersForProject(proj *crd.Project) error {
	pcm.mu.Lock()
	defer pcm.mu.Unlock()

	if _, exists := pcm.controllers[proj.ProjectID]; exists {
		return fmt.Errorf("controllers for project %s already exist", proj.ProjectID)
	}

	// Initialize and start the controllers for the project
	pcm.controllers[proj.ProjectID] = &ControllerSet{
		// Initialize your controllers here
	}

	pcm.logger.Info("Started controllers for project", "projectId", proj.ProjectID)
	return nil
}

func (pcm *ProjectControllerManager) StopControllersForProject(proj *crd.Project) error {
	pcm.mu.Lock()
	defer pcm.mu.Unlock()

	_, exists := pcm.controllers[proj.ProjectID]
	if !exists {
		pcm.logger.Info("Controllers for project do not exist", "projectId", proj.ProjectID)
		return nil
	}

	// Stop and clean up the controllers for the project
	// ...

	delete(pcm.controllers, proj.ProjectID)
	pcm.logger.Info("Stopped controllers for project", "projectId", proj.ProjectID)
	return nil
}

func (pcm *ProjectControllerManager) startControllers(proj *crd.Project) error {
	cloud, err := gce.NewGCEClientForProject(pcm.logger, proj)
	if err != nil {
		return fmt.Errorf("failed to create GCE client for project %s: %v", proj.ProjectID, err)
	}

	ctxConfig := ingctx.ControllerContextConfig{
		ResyncPeriod:          flags.F.ResyncPeriod,
		DefaultBackendSvcPort: pcm.sharedContext.defaultBackendServicePort,
		HealthCheckPath:       flags.F.HealthCheckPath,
	}
	ctx := ingctx.NewControllerContext(pcm.sharedContext.kubeClient, backendConfigClient, frontendConfigClient, firewallCRClient, svcNegClient, ingParamsClient, svcAttachmentClient, networkClient, nodeTopologyClient, eventRecorderKubeClient, cloud, namer, kubeSystemUID, ctxConfig, rootLogger)

}
