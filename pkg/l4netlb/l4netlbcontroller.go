/*
Copyright 2021 The Kubernetes Authors.

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

package l4netlb

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	v1 "k8s.io/api/core/v1"
	listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/cloud-provider/service/helpers"
	"k8s.io/ingress-gce/pkg/annotations"
	"k8s.io/ingress-gce/pkg/backends"
	"k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/controller/translator"
	"k8s.io/ingress-gce/pkg/instances"
	"k8s.io/ingress-gce/pkg/loadbalancers"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/common"
	utilslb "k8s.io/ingress-gce/pkg/utils/loadbalancers"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/ingress-gce/pkg/utils/patch"
	"k8s.io/klog"
)

const (
	// The max tolerated delay between update being enqueued and sync being invoked.
	enqueueToSyncDelayThreshold = 15 * time.Minute
)

type L4NetLBController struct {
	ctx           *context.ControllerContext
	svcQueue      utils.TaskQueue
	serviceLister cache.Indexer
	nodeLister    listers.NodeLister
	stopCh        chan struct{}

	translator *translator.Translator
	namer      namer.L4ResourcesNamer
	// enqueueTracker tracks the latest time an update was enqueued
	enqueueTracker utils.TimeTracker
	// syncTracker tracks the latest time an enqueued service was synced
	syncTracker         utils.TimeTracker
	sharedResourcesLock sync.Mutex

	backendPool  *backends.Backends
	instancePool instances.NodePool
	igLinker     *backends.RegionalInstanceGroupLinker
}

// NewL4NetLBController creates a controller for l4 external loadbalancer.
func NewL4NetLBController(
	ctx *context.ControllerContext,
	stopCh chan struct{}) *L4NetLBController {
	if ctx.NumL4Workers <= 0 {
		klog.Infof("L4 Worker count has not been set, setting to 1")
		ctx.NumL4Workers = 1
	}

	backendPool := backends.NewPool(ctx.Cloud, ctx.L4Namer)
	l4netLBc := &L4NetLBController{
		ctx:           ctx,
		serviceLister: ctx.ServiceInformer.GetIndexer(),
		nodeLister:    listers.NewNodeLister(ctx.NodeInformer.GetIndexer()),
		stopCh:        stopCh,
		translator:    ctx.Translator,
		backendPool:   backendPool,
		namer:         ctx.L4Namer,
		instancePool:  ctx.InstancePool,
		igLinker:      backends.NewRegionalInstanceGroupLinker(ctx.InstancePool, backendPool),
	}
	l4netLBc.svcQueue = utils.NewPeriodicTaskQueueWithMultipleWorkers("l4netLB", "services", ctx.NumL4Workers, l4netLBc.sync)

	ctx.ServiceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			addSvc := obj.(*v1.Service)
			svcKey := utils.ServiceKeyFunc(addSvc.Namespace, addSvc.Name)
			if l4netLBc.shouldProcessService(addSvc, nil) {
				klog.V(3).Infof("L4 External LoadBalancer Service %s added, enqueuing", svcKey)
				l4netLBc.ctx.Recorder(addSvc.Namespace).Eventf(addSvc, v1.EventTypeNormal, "ADD", svcKey)
				l4netLBc.svcQueue.Enqueue(addSvc)
				l4netLBc.enqueueTracker.Track()
			} else {
				klog.V(4).Infof("Ignoring add for non external lb service %s", svcKey)
			}
		},
		// Deletes will be handled in the Update when the deletion timestamp is set.
		UpdateFunc: func(old, cur interface{}) {
			curSvc := cur.(*v1.Service)
			oldSvc := old.(*v1.Service)
			svcKey := utils.ServiceKeyFunc(curSvc.Namespace, curSvc.Name)
			if l4netLBc.shouldProcessService(curSvc, oldSvc) {
				klog.V(3).Infof("L4 External LoadBalancer Service %s updated, enqueuing", svcKey)
				l4netLBc.svcQueue.Enqueue(curSvc)
				l4netLBc.enqueueTracker.Track()
				return
			}
		},
	})
	ctx.AddHealthCheck("service-controller health", l4netLBc.checkHealth)
	return l4netLBc
}

// needsAddition checks if given service should be added by controller
func needsAddition(newSvc, oldSvc *v1.Service) bool {
	if oldSvc != nil {
		return false
	}
	needsNetLB, _ := annotations.WantsL4NetLB(newSvc)
	return needsNetLB
}

// needsDeletion return true if svc required deleting RBS based NetLB
func needsDeletion(svc *v1.Service) bool {
	if !utils.IsL4NetLBService(svc) {
		return false
	}
	if common.IsDeletionCandidateForGivenFinalizer(svc.ObjectMeta, common.NetLBFinalizerV2) {
		return true
	}
	needsNetLB, _ := annotations.WantsL4NetLB(svc)
	return !needsNetLB
}

// needsPeriodicEnqueue return true if svc required periodic enqueue
func needsPeriodicEnqueue(newSvc, oldSvc *v1.Service) bool {
	if oldSvc == nil {
		return false
	}
	needsNetLb, _ := annotations.WantsL4NetLB(newSvc)
	return needsNetLb && reflect.DeepEqual(oldSvc, newSvc)
}

// needsUpdate checks if load balancer needs to be updated due to change in attributes.
func (lc *L4NetLBController) needsUpdate(newSvc, oldSvc *v1.Service) bool {
	if oldSvc == nil {
		return false
	}
	oldSvcWantsILB, oldType := annotations.WantsL4NetLB(oldSvc)
	newSvcWantsILB, newType := annotations.WantsL4NetLB(newSvc)
	recorder := lc.ctx.Recorder(oldSvc.Namespace)
	if oldSvcWantsILB != newSvcWantsILB {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "Type", "%v -> %v", oldType, newType)
		return true
	}
	if !newSvcWantsILB && !oldSvcWantsILB {
		// Ignore any other changes if both the previous and new service do not need L4 External LB.
		return false
	}
	if !reflect.DeepEqual(oldSvc.Spec.LoadBalancerSourceRanges, newSvc.Spec.LoadBalancerSourceRanges) {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "LoadBalancerSourceRanges", "%v -> %v",
			oldSvc.Spec.LoadBalancerSourceRanges, newSvc.Spec.LoadBalancerSourceRanges)
		return true
	}

	if !utilslb.PortsEqualForLBService(oldSvc, newSvc) || oldSvc.Spec.SessionAffinity != newSvc.Spec.SessionAffinity {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "Ports/SessionAffinity", "Ports %v, SessionAffinity %v -> Ports %v, SessionAffinity  %v",
			oldSvc.Spec.Ports, oldSvc.Spec.SessionAffinity, newSvc.Spec.Ports, newSvc.Spec.SessionAffinity)
		return true
	}
	if !reflect.DeepEqual(oldSvc.Spec.SessionAffinityConfig, newSvc.Spec.SessionAffinityConfig) {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "SessionAffinityConfig", "%v -> %v",
			oldSvc.Spec.SessionAffinityConfig, newSvc.Spec.SessionAffinityConfig)
		return true
	}
	if oldSvc.Spec.LoadBalancerIP != newSvc.Spec.LoadBalancerIP {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "LoadbalancerIP", "%v -> %v",
			oldSvc.Spec.LoadBalancerIP, newSvc.Spec.LoadBalancerIP)
		return true
	}
	if len(oldSvc.Spec.ExternalIPs) != len(newSvc.Spec.ExternalIPs) {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "ExternalIP", "Count: %v -> %v",
			len(oldSvc.Spec.ExternalIPs), len(newSvc.Spec.ExternalIPs))
		return true
	}
	for i := range oldSvc.Spec.ExternalIPs {
		if oldSvc.Spec.ExternalIPs[i] != newSvc.Spec.ExternalIPs[i] {
			recorder.Eventf(newSvc, v1.EventTypeNormal, "ExternalIP", "Added: %v",
				newSvc.Spec.ExternalIPs[i])
			return true
		}
	}
	if !reflect.DeepEqual(oldSvc.Annotations, newSvc.Annotations) {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "Annotations", "%v -> %v",
			oldSvc.Annotations, newSvc.Annotations)
		return true
	}
	if oldSvc.Spec.ExternalTrafficPolicy != newSvc.Spec.ExternalTrafficPolicy {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "ExternalTrafficPolicy", "%v -> %v",
			oldSvc.Spec.ExternalTrafficPolicy, newSvc.Spec.ExternalTrafficPolicy)
		return true
	}
	if oldSvc.Spec.HealthCheckNodePort != newSvc.Spec.HealthCheckNodePort {
		recorder.Eventf(newSvc, v1.EventTypeNormal, "HealthCheckNodePort", "%v -> %v",
			oldSvc.Spec.HealthCheckNodePort, newSvc.Spec.HealthCheckNodePort)
		return true
	}
	return false
}

// shouldProcessUpdate checks if given service should be process by controller
func (lc *L4NetLBController) shouldProcessService(newSvc, oldSvc *v1.Service) bool {
	if needsAddition(newSvc, oldSvc) || lc.needsUpdate(newSvc, oldSvc) || needsDeletion(newSvc) {
		l4netlb := loadbalancers.NewL4NetLB(newSvc, lc.ctx.Cloud, meta.Regional, lc.namer, lc.ctx.Recorder(newSvc.Namespace), &lc.sharedResourcesLock)
		return lc.isRbsBasedLBService(newSvc, l4netlb)
	}
	return needsPeriodicEnqueue(newSvc, oldSvc)
}

// isRbsBasedLBService returns if the given LoadBalancer service is not legacy target pool based LoadBalancer.
func (lc *L4NetLBController) isRbsBasedLBService(svc *v1.Service, l4 *loadbalancers.L4NetLB) bool {
	// skip services that are being handled by the legacy service controller.
	if utils.IsLegacyL4NetLBService(svc) {
		klog.Warningf("Ignoring update for service %s:%s managed by service controller", svc.Namespace, svc.Name)
		return false
	}
	if lc.hasLegacyForwardingRule(svc) {
		klog.Warningf("Ignoring update for service %s:%s which have legacy forwarding rule", svc.Namespace, svc.Name)
		return false
	}
	return true
}

func (lc *L4NetLBController) checkHealth() error {
	lastEnqueueTime := lc.enqueueTracker.Get()
	lastSyncTime := lc.syncTracker.Get()
	// if lastEnqueue time is more than 15 minutes before the last sync time, the controller is falling behind.
	// This indicates that the controller was stuck handling a previous update, or sync function did not get invoked.
	syncTimeLatest := lastEnqueueTime.Add(enqueueToSyncDelayThreshold)
	if lastSyncTime.After(syncTimeLatest) {
		msg := fmt.Sprintf("L4 External LoadBalancer Sync happened at time %v - %v after enqueue time, threshold is %v", lastSyncTime, lastSyncTime.Sub(lastEnqueueTime), enqueueToSyncDelayThreshold)
		klog.Error(msg)
	}
	return nil
}

// Run starts the loadbalancer controller.
func (lc *L4NetLBController) Run() {
	defer lc.shutdown()
	klog.Infof("Starting l4NetLBController")
	lc.svcQueue.Run()

	<-lc.stopCh
}

func (lc *L4NetLBController) shutdown() {
	klog.Infof("Shutting down l4NetLBController")
	lc.svcQueue.Shutdown()
}

func (lc *L4NetLBController) sync(key string) error {
	lc.syncTracker.Track()
	svc, exists, err := lc.ctx.Services().GetByKey(key)
	if err != nil {
		return fmt.Errorf("Failed to lookup L4 External LoadBalancer service for key %s : %w", key, err)
	}
	if !exists || svc == nil {
		klog.V(3).Infof("Ignoring sync of non-existent service %s", key)
		return nil
	}
	if needsDeletion(svc) {
		klog.V(3).Infof("Deleting L4 External LoadBalancer resources for service %s", key)
		result := lc.garbageCollectRBSNetLB(key, svc)
		if result == nil {
			return nil
		}
		return result.Error
	}

	if wantsNetLB, _ := annotations.WantsL4NetLB(svc); wantsNetLB {
		result := lc.syncInternal(svc)
		if result == nil {
			// result will be nil if the service was ignored(due to presence of service controller finalizer).
			return nil
		}
		return result.Error
	}
	klog.V(3).Infof("Ignoring sync of service %s, neither delete nor ensure needed.", key)
	return nil
}

// syncInternal ensures load balancer resources for the given service, as needed.
// Returns an error if processing the service update failed.
func (lc *L4NetLBController) syncInternal(service *v1.Service) *loadbalancers.SyncResultNetLB {
	l4netlb := loadbalancers.NewL4NetLB(service, lc.ctx.Cloud, meta.Regional, lc.namer, lc.ctx.Recorder(service.Namespace), &lc.sharedResourcesLock)
	// check again that it's not legacy service
	if !lc.isRbsBasedLBService(service, l4netlb) {
		return nil
	}

	if err := common.EnsureServiceFinalizer(service, common.NetLBFinalizerV2, lc.ctx.KubeClient); err != nil {
		return &loadbalancers.SyncResultNetLB{Error: fmt.Errorf("Failed to attach L4 External LoadBalancer finalizer to service %s/%s, err %w", service.Namespace, service.Name, err)}
	}

	nodeNames, err := utils.GetReadyNodeNames(lc.nodeLister)
	if err != nil {
		return &loadbalancers.SyncResultNetLB{Error: err}
	}

	if err := lc.ensureInstanceGroups(service, nodeNames); err != nil {
		lc.ctx.Recorder(service.Namespace).Eventf(service, v1.EventTypeWarning, "SyncInstanceGroupsFailed",
			"Error syncing instance group, err: %v", err)
		return &loadbalancers.SyncResultNetLB{Error: err}
	}

	// Use the same function for both create and updates. If controller crashes and restarts,
	// all existing services will show up as Service Adds.
	syncResult := l4netlb.EnsureFrontend(nodeNames, service)
	if syncResult.Error != nil {
		lc.ctx.Recorder(service.Namespace).Eventf(service, v1.EventTypeWarning, "SyncExternalLoadBalancerFailed",
			"Error ensuring Resource for L4 External LoadBalancer, err: %v", syncResult.Error)
		return syncResult
	}

	if err = lc.ensureBackendLinking(l4netlb.ServicePort); err != nil {
		lc.ctx.Recorder(service.Namespace).Eventf(service, v1.EventTypeWarning, "SyncExternalLoadBalancerFailed",
			"Error linking instance groups to backend service, err: %v", err)
		syncResult.Error = err
		return syncResult
	}

	err = lc.ensureServiceStatus(service, syncResult.Status)
	if err != nil {
		lc.ctx.Recorder(service.Namespace).Eventf(service, v1.EventTypeWarning, "SyncExternalLoadBalancerFailed",
			"Error updating L4 External LoadBalancer, err: %v", err)
		syncResult.Error = err
		return syncResult
	}
	lc.ctx.Recorder(service.Namespace).Eventf(service, v1.EventTypeNormal, "SyncLoadBalancerSuccessful",
		"Successfully ensured L4 External LoadBalancer resources")
	if err = lc.updateAnnotations(service, syncResult.Annotations); err != nil {
		lc.ctx.Recorder(service.Namespace).Eventf(service, v1.EventTypeWarning, "SyncExternalLoadBalancerFailed",
			"Failed to update annotations for load balancer, err: %v", err)
		syncResult.Error = fmt.Errorf("failed to set resource annotations, err: %w", err)
		return syncResult
	}
	return nil
}

func (lc *L4NetLBController) updateAnnotations(svc *v1.Service, newAnnotations map[string]string) error {
	newObjectMeta := utilslb.ComputeNewAnnotationsIfNeeded(svc, newAnnotations)
	if newObjectMeta == nil {
		return nil
	}
	klog.V(3).Infof("Patching annotations of service %v/%v", svc.Namespace, svc.Name)
	return patch.PatchServiceObjectMetadata(lc.ctx.KubeClient.CoreV1(), svc, *newObjectMeta)
}

func (lc *L4NetLBController) ensureBackendLinking(port utils.ServicePort) error {
	zones, err := lc.translator.ListZones(utils.CandidateNodesPredicate)
	if err != nil {
		return err
	}
	return lc.igLinker.Link(port, lc.ctx.Cloud.ProjectID(), zones)
}

func (lc *L4NetLBController) ensureInstanceGroups(service *v1.Service, nodeNames []string) error {
	// TODO(kl52752) implement limit for 1000 nodes in instance group
	// TODO(kl52752) Move instance creation and deletion logic to NodeController
	// to avoid race condition between controllers
	_, _, nodePorts, _ := utils.GetPortsAndProtocol(service.Spec.Ports)
	_, err := lc.instancePool.EnsureInstanceGroupsAndPorts(lc.ctx.ClusterNamer.InstanceGroup(), nodePorts)
	if err != nil {
		return err
	}
	return lc.instancePool.Sync(nodeNames)
}

func (lc *L4NetLBController) ensureServiceStatus(svc *v1.Service, newStatus *v1.LoadBalancerStatus) error {
	if helpers.LoadBalancerStatusEqual(&svc.Status.LoadBalancer, newStatus) {
		return nil
	}
	return patch.PatchServiceLoadBalancerStatus(lc.ctx.KubeClient.CoreV1(), svc, *newStatus)
}

// hasLegacyForwardingRule return true if forwarding rule is target pool based
func (lc *L4NetLBController) hasLegacyForwardingRule(svc *v1.Service) bool {
	//TODO(kl52752) Add check for service annotation first to reduce GCE API calls
	l4netlb := loadbalancers.NewL4NetLB(svc, lc.ctx.Cloud, meta.Regional, lc.namer, lc.ctx.Recorder(svc.Namespace), &lc.sharedResourcesLock)
	frName := utils.LegacyForwardingRuleName(svc)
	existingFR := l4netlb.GetForwardingRule(frName, meta.VersionGA)
	return existingFR != nil && existingFR.LoadBalancingScheme == string(cloud.SchemeExternal) && existingFR.Target != ""
}

// garbageCollectRBSNetLB cleans-up all gce resources related to service and removes NetLB finalizer
func (lc *L4NetLBController) garbageCollectRBSNetLB(key string, svc *v1.Service) *loadbalancers.SyncResult {
	l4netLB := loadbalancers.NewL4NetLB(svc, lc.ctx.Cloud, meta.Regional, lc.namer, lc.ctx.Recorder(svc.Namespace), &lc.sharedResourcesLock)
	lc.ctx.Recorder(svc.Namespace).Eventf(svc, v1.EventTypeNormal, "DeletingLoadBalancer",
		"Deleting L4 External LoadBalancer for %s", key)
	result := l4netLB.EnsureLoadBalancerDeleted(svc)
	if result.Error != nil {
		lc.ctx.Recorder(svc.Namespace).Eventf(svc, v1.EventTypeWarning, "DeleteLoadBalancerFailed",
			"Error deleting L4 External LoadBalancer, err: %v", result.Error)
		return result
	}
	if err := lc.ensureServiceStatus(svc, &v1.LoadBalancerStatus{}); err != nil {
		lc.ctx.Recorder(svc.Namespace).Eventf(svc, v1.EventTypeWarning, "DeleteLoadBalancer",
			"Error reseting L4 External LoadBalancer status to empty, err: %v", err)
		result.Error = fmt.Errorf("Failed to reset L4 External LoadBalancer status, err: %w", err)
		return result
	}
	if err := common.EnsureDeleteServiceFinalizer(svc, common.NetLBFinalizerV2, lc.ctx.KubeClient); err != nil {
		lc.ctx.Recorder(svc.Namespace).Eventf(svc, v1.EventTypeWarning, "DeleteLoadBalancerFailed",
			"Error removing finalizer from L4 External LoadBalancer, err: %v", err)
		result.Error = fmt.Errorf("Failed to remove L4 External LoadBalancer finalizer, err: %w", err)
		return result
	}
	lc.ctx.Recorder(svc.Namespace).Eventf(svc, v1.EventTypeNormal, "DeletedLoadBalancer", "Deleted L4 External LoadBalancer")
	return result
}
