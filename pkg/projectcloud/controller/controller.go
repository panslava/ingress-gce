package controller

import (
	"fmt"
	"math/rand"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/cloud-provider-gcp/providers/gce"
	"k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/projectcloud/crd"
	"k8s.io/ingress-gce/pkg/projectcloud/store"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/klog/v2"
)

const (
	projectControllerName = "project-cloud-controller"
	workersNum            = 5
)

type ProjectController struct {
	gceClouds store.CloudStorer

	client        kubernetes.Interface
	projectLister cache.Indexer
	projectQueue  utils.TaskQueue
	numWorkers    int
	logger        klog.Logger
	stopCh        <-chan struct{}
	hasSynced     func() bool
}

// NewProjectController creates a new instance of the Project controller.
func NewProjectController(ctx *context.ControllerContext, informer cache.SharedIndexInformer, stopCh <-chan struct{}, logger klog.Logger) *ProjectController {
	logger = logger.WithName(projectControllerName)
	pc := &ProjectController{
		client:        ctx.KubeClient,
		projectLister: informer.GetIndexer(),
		stopCh:        stopCh,
		numWorkers:    workersNum,
		logger:        logger,
		hasSynced:     ctx.HasSynced,
	}

	pc.projectQueue = utils.NewPeriodicTaskQueueWithMultipleWorkers("project", "projects", pc.numWorkers, pc.syncWrapper, logger)

	informer.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    pc.enqueueProject,
			UpdateFunc: func(old, cur interface{}) { pc.enqueueProject(cur) },
			DeleteFunc: pc.handleProjectDeletion,
		})

	return pc
}

func (pc *ProjectController) Run() {
	defer pc.shutdown()

	wait.PollUntil(5*time.Second, func() (bool, error) {
		pc.logger.V(2).Info("Waiting for initial cache sync before starting Project Controller")
		return pc.hasSynced(), nil
	}, pc.stopCh)

	pc.logger.Info("Running Project Controller", "numWorkers", pc.numWorkers)
	pc.projectQueue.Run()
	<-pc.stopCh
}

func (pc *ProjectController) enqueueProject(obj interface{}) {
	key, err := cache.MetaNamespaceKeyFunc(obj)
	if err != nil {
		pc.logger.Error(err, "Failed to get key for project", "obj", obj)
		return
	}
	pc.projectQueue.Enqueue(key)
}

func (pc *ProjectController) handleProjectDeletion(obj interface{}) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
	if err != nil {
		pc.logger.Error(err, "Failed to get key for deleted project", "obj", obj)
		return
	}
	pc.projectQueue.Enqueue(key)
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
	return pc.sync(key, svcLogger)
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

	// Ensure the project is of the correct type
	proj, ok := project.(*crd.Project)
	if !ok {
		err := fmt.Errorf("unexpected type for project, expected *Project but got %T", project)
		logger.Error(err, "Type assertion failed", "key", key)
		return err
	}
	if proj.DeletionTimestamp != nil {
		logger.V(3).Info("Project is being deleted, deleting cloud")
		pc.gceClouds.DeleteCloudForProject(proj)
		return nil
	}

	cloud, err := pc.gceClouds.CloudForProject(proj)
	if err != nil {
		logger.Error(err, "Failed to get cloud for project")
		return err
	}
	if cloud != nil {
		logger.V(2).Info("Project already has cloud created, skipping", "projectId", proj.ProjectID)
		return nil
	}

	logger.V(2).Info("Creating cloud for project", "projectId", proj.ProjectID)

	cloud, err = NewCloudFromProject(proj)
	if err != nil {
		logger.Error(err, "Failed to create GCE cloud for project", "key", key)
		return err
	}

	pc.gceClouds.StoreCloudForProject(proj, cloud)
	return nil
}

// NewCloudFromProject mocks functionality to create new cloud for Project.
func NewCloudFromProject(proj *crd.Project) (*gce.Cloud, error) {
	if proj == nil || proj.ProjectID == "" {
		return nil, fmt.Errorf("ProjectID is required to create a GCE cloud")
	}
	return &gce.Cloud{}, nil
}
