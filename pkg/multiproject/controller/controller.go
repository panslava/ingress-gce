package controller

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	ingresscontext "k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/multiproject/crd"
	"k8s.io/ingress-gce/pkg/multiproject/manager"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/klog/v2"
)

const (
	projectControllerName = "multi-project-controller"
	workersNum            = 5
)

type ProjectController struct {
	manager *manager.ProjectControllerManager

	client        kubernetes.Interface
	projectLister cache.Indexer
	projectQueue  utils.TaskQueue
	numWorkers    int
	logger        klog.Logger
	stopCh        <-chan struct{}
	hasSynced     func() bool
}

// NewProjectController creates a new instance of the Project controller.
func NewProjectController(ctx *ingresscontext.ControllerContext, informer cache.SharedIndexInformer, stopCh <-chan struct{}, logger klog.Logger) *ProjectController {
	logger = logger.WithName(projectControllerName)
	pc := &ProjectController{
		client:        ctx.KubeClient,
		projectLister: informer.GetIndexer(),
		stopCh:        stopCh,
		numWorkers:    workersNum,
		logger:        logger,
		hasSynced:     ctx.HasSynced,
		manager:       manager.NewProjectControllerManager(ctx.KubeClient, logger),
	}

	pc.projectQueue = utils.NewPeriodicTaskQueueWithMultipleWorkers(projectControllerName, "projects", pc.numWorkers, pc.syncWrapper, logger)

	informer.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) { pc.projectQueue.Enqueue(obj) },
			UpdateFunc: func(old, cur interface{}) {
				oldProj, ok := old.(*crd.Project)
				if !ok {
					pc.logger.Error(fmt.Errorf("unexpected type for project, expected *Project but got %T", old), "Type assertion failed")
					return
				}
				curProj, ok := cur.(*crd.Project)
				if !ok {
					pc.logger.Error(fmt.Errorf("unexpected type for project, expected *Project but got %T", cur), "Type assertion failed")
					return
				}
				if needsUpdate(oldProj, curProj) {
					pc.projectQueue.Enqueue(cur)
				}
			},
		})

	return pc
}

func (pc *ProjectController) Run() {
	defer pc.shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-pc.stopCh
		cancel()
	}()

	wait.PollUntilContextCancel(ctx, 5*time.Second, false, func(ctx context.Context) (bool, error) {
		pc.logger.V(2).Info("Waiting for initial cache sync before starting Project Controller")
		return pc.hasSynced(), nil
	})

	pc.logger.Info("Running Project Controller", "numWorkers", pc.numWorkers)
	pc.projectQueue.Run()
	<-pc.stopCh
}

func (pc *ProjectController) shutdown() {
	pc.logger.Info("Shutting down Project Controller")
	pc.projectQueue.Shutdown()
}

func (pc *ProjectController) syncWrapper(key string) error {
	syncID := rand.Int31()
	svcLogger := pc.logger.WithValues("projectKey", key, "syncId", syncID)

	defer func() {
		if r := recover(); r != nil {
			svcLogger.Error(fmt.Errorf("panic in Project sync worker goroutine: %v", r), "Recovered from panic")
		}
	}()
	err := pc.sync(key, svcLogger)
	if err != nil {
		svcLogger.Error(err, "Error syncing project", "key", key)
	}
	return err
}

func (pc *ProjectController) sync(key string, logger klog.Logger) error {
	logger = logger.WithName("projectcloud.sync")

	project, exists, err := pc.projectLister.GetByKey(key)
	if err != nil {
		return fmt.Errorf("failed to lookup project for key %s: %w", key, err)
	}
	if !exists || project == nil {
		logger.V(3).Info("Project does not exist anymore")
		return nil
	}
	proj, ok := project.(*crd.Project)
	if !ok {
		return fmt.Errorf("unexpected type for project, expected *Project but got %T", project)
	}

	if proj.DeletionTimestamp != nil {
		logger.Info("Project is being deleted, stopping controllers", "project", proj)

		err = pc.manager.StopControllersForProject(proj)
		if err != nil {
			return fmt.Errorf("failed to stop controllers for project %v: %w", proj, err)
		}

		return nil
	}

	logger.V(2).Info("Syncing project", "project", proj)

	// Restart controllers for the project.
	err = pc.manager.StopControllersForProject(proj)
	if err != nil {
		return fmt.Errorf("failed to stop controllers for project %v: %w", proj, err)
	}
	err = pc.manager.StartControllersForProject(proj)
	if err != nil {
		return fmt.Errorf("failed to start controllers for project %v: %w", proj, err)
	}

	logger.V(2).Info("Successfully synced project", "project", proj)
	return nil
}

// needsUpdate determines if the project needs to be updated.
func needsUpdate(old, new *crd.Project) bool {
	// TODO: Implement this
	return true
}
