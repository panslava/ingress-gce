/*
Copyright 2022 The Kubernetes Authors.

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

package instancegroup

import (
	"time"

	apiv1 "k8s.io/api/core/v1"

	"google.golang.org/api/compute/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/flags"
	"k8s.io/ingress-gce/pkg/instances"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/klog"
)

type MultiIGNodeController struct {
	clusterNamer *namer.Namer
	instancePool *instances.MultiIGInstances
	// lister is a cache of the k8s Node resources.
	lister cache.Indexer
	// queue is the TaskQueue used to manage the node worker updates.
	queue utils.TaskQueue
	// hasSynced returns true if relevant caches have done their initial
	// synchronization.
	hasSynced func() bool

	stopCh <-chan struct{}
}

// NewMultiIGNodeController returns a new multi instances group controller.
func NewMultiIGNodeController(ctx *context.ControllerContext, stopCh <-chan struct{}) *MultiIGNodeController {
	igc := &MultiIGNodeController{
		lister:       ctx.NodeInformer.GetIndexer(),
		clusterNamer: ctx.ClusterNamer,
		hasSynced:    ctx.HasSynced,
		stopCh:       stopCh,
	}
	igc.queue = utils.NewPeriodicTaskQueue("", "nodes", igc.sync)

	// Cast or die
	pool, ok := ctx.InstancePool.(*instances.MultiIGInstances)
	if !ok {
		klog.Fatalf("provided InstancePool must be of type MultiIGInstances")
	}
	igc.instancePool = pool

	ctx.NodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			igc.queue.Enqueue(obj)
		},
		DeleteFunc: func(obj interface{}) {
			igc.queue.Enqueue(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			// TODO(cezarygerard) trigger sync periodically.
			if utils.NodeStatusChanged(oldObj.(*apiv1.Node), newObj.(*apiv1.Node)) {
				igc.queue.Enqueue(newObj)
			}
		},
	})
	return igc
}

// Run the queue to process updates for the nodes. This must be run in a
// separate goroutine (method will block until queue shutdown).
func (igc *MultiIGNodeController) Run() {
	start := time.Now()
	for !igc.hasSynced() {
		klog.V(2).Infof("Waiting for hasSynced (%s elapsed)", time.Now().Sub(start))
		time.Sleep(1 * time.Second)
	}
	klog.V(2).Infof("Caches synced (took %s)", time.Now().Sub(start))
	go igc.queue.Run()
	<-igc.stopCh
	igc.Shutdown()
}

// Shutdown shuts down the goroutine that processes node updates.
func (igc *MultiIGNodeController) Shutdown() {
	igc.queue.Shutdown()
}

func (igc *MultiIGNodeController) splitNodesByIG(nodeToIG map[string]*compute.InstanceGroup) map[string][]string {
	igToNodes := make(map[string][]string)
	for node, ig := range nodeToIG {
		igToNodes[ig.Name] = append(igToNodes[ig.Name], node)
	}
	return igToNodes
}

func (igc *MultiIGNodeController) calculateDataPerZone(zone string, zonalNodeNames []string) (err error, nodesToIG map[string]*compute.InstanceGroup, duplicateNodesToIGs map[string][]*compute.InstanceGroup, igSizes map[string]int, nodesToRemove []string, nodesToAdd []string) {
	allIGsForZone, err := igc.instancePool.Cloud.ListInstanceGroups(zone)
	if err != nil {
		return err, nil, nil, nil, nil, nil
	}
	nodesToIG = make(map[string]*compute.InstanceGroup)
	duplicateNodesToIGs = make(map[string][]*compute.InstanceGroup)
	igSizes = make(map[string]int)
	gceNodes := sets.NewString()
	for _, ig := range allIGsForZone {
		if igc.clusterNamer.NameBelongsToCluster(ig.Name) {
			instancesInIG, err := igc.instancePool.Cloud.ListInstancesInInstanceGroup(ig.Name, ig.Zone, "ALL")
			if err != nil {
				return err, nil, nil, nil, nil, nil
			}
			for _, i := range instancesInIG {
				instanceName, err := utils.KeyName(i.Instance)
				if err != nil {
					return err, nil, nil, nil, nil, nil
				}
				if _, ok := nodesToIG[instanceName]; ok {
					duplicateNodesToIGs[instanceName] = append(duplicateNodesToIGs[instanceName], ig)
				} else {
					nodesToIG[instanceName] = ig
				}
				gceNodes.Insert(instanceName)
				igSizes[ig.Name]++
			}
		}
	}
	kubeNodes := sets.NewString(zonalNodeNames...)

	// A node deleted via kubernetes could still exist as a gce vm. We don't
	// want to route requests to it. Similarly, a node added to kubernetes
	// needs to get added to the instance group, so we do route requests to it.
	nodesToRemove = gceNodes.Difference(kubeNodes).List()
	nodesToAdd = kubeNodes.Difference(gceNodes).List()

	return nil, nodesToIG, duplicateNodesToIGs, igSizes, nodesToRemove, nodesToAdd
}

func (igc *MultiIGNodeController) syncZone(zone string, zonalNodeNames []string) error {
	err, nodesToIG, duplicateNodesToIG, igSizes, nodesToRemove, nodesToAdd := igc.calculateDataPerZone(zone, zonalNodeNames)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Removing %d, adding %d nodes", len(nodesToRemove), len(nodesToAdd))

	if err = igc.removeNodes(nodesToRemove, zone, nodesToIG, duplicateNodesToIG); err != nil {
		return err
	}
	if err = igc.addNodes(nodesToAdd, zone, igSizes); err != nil {
		return err
	}
	return nil
}

func (igc *MultiIGNodeController) sync(key string) error {
	allNodeNames, err := utils.GetReadyNodeNames(listers.NewNodeLister(igc.lister))
	if err != nil {
		return err
	}
	for zone, zonalNodeNames := range igc.instancePool.SplitNodesByZone(allNodeNames) {
		if err = igc.syncZone(zone, zonalNodeNames); err != nil {
			return err
		}
	}
	return nil
}

func (igc *MultiIGNodeController) removeNodes(removeNodes []string, zone string, nodesToIG map[string]*compute.InstanceGroup, duplicateNodesToIG map[string][]*compute.InstanceGroup) error {
	if len(removeNodes) == 0 && len(duplicateNodesToIG) == 0 {
		return nil
	}
	igToRemoveNodes := make(map[string]sets.String)
	for _, node := range removeNodes {
		if _, ok := igToRemoveNodes[nodesToIG[node].Name]; !ok {
			igToRemoveNodes[nodesToIG[node].Name] = sets.NewString()
		}
		igToRemoveNodes[nodesToIG[node].Name].Insert(node)
	}
	for node, IGs := range duplicateNodesToIG {
		for _, IG := range IGs {
			if _, ok := igToRemoveNodes[IG.Name]; !ok {
				igToRemoveNodes[IG.Name] = sets.NewString()
			}
			igToRemoveNodes[IG.Name].Insert(node)
		}
	}

	for igName, nodes := range igToRemoveNodes {
		if err := igc.instancePool.Cloud.RemoveInstancesFromInstanceGroup(igName, zone, igc.instancePool.GetInstanceReferences(zone, nodes.List())); err != nil {
			return err
		}
	}
	return nil
}

func (igc *MultiIGNodeController) addNodes(addNodes []string, zone string, igSizes map[string]int) error {
	if len(addNodes) == 0 && flags.F.MaxIgSize == 0 {
		return nil
	}
	for igIndex := 0; len(addNodes) != 0; igIndex++ {
		currentIGName := igc.clusterNamer.InstanceGroupByIndex(igIndex)
		if _, exist := igSizes[currentIGName]; !exist {
			err := igc.instancePool.Cloud.CreateInstanceGroup(&compute.InstanceGroup{Name: currentIGName}, zone)
			if err != nil {
				return err
			}
			igSizes[currentIGName] = 0
		}
		availableSize := flags.F.MaxIgSize - igSizes[currentIGName]
		currentNodesToAdd := addNodes
		if availableSize < len(currentNodesToAdd) {
			currentNodesToAdd = currentNodesToAdd[:availableSize]
		}
		if err := igc.instancePool.Cloud.AddInstancesToInstanceGroup(currentIGName, zone, igc.instancePool.GetInstanceReferences(zone, currentNodesToAdd)); err != nil {
			return err
		}
		if len(addNodes) > availableSize {
			addNodes = addNodes[availableSize:]
		} else {
			addNodes = []string{}
		}
	}
	return nil
}
