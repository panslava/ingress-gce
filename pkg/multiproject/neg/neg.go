package neg

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/cloud-provider-gcp/providers/gce"
	"k8s.io/ingress-gce/pkg/flags"
	multiprojectgce "k8s.io/ingress-gce/pkg/multiproject/gce"
	"k8s.io/ingress-gce/pkg/multiproject/projectcrd"
	"k8s.io/ingress-gce/pkg/multiproject/projectinformer"
	"k8s.io/ingress-gce/pkg/multiproject/sharedcontext"
	"k8s.io/ingress-gce/pkg/neg"
	"k8s.io/ingress-gce/pkg/neg/syncers/labels"
	negtypes "k8s.io/ingress-gce/pkg/neg/types"
	"k8s.io/ingress-gce/pkg/network"
	svcnegclient "k8s.io/ingress-gce/pkg/svcneg/client/clientset/versioned"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/ingress-gce/pkg/utils/zonegetter"
	"k8s.io/klog/v2"
)

var svcNegResource = schema.GroupVersionResource{Group: "networking.gke.io", Version: "v1beta1", Resource: "servicenetworkendpointgroups"}
var gkeNetworkParamsResource = schema.GroupVersionResource{Group: "networking.gke.io", Version: "v1", Resource: "networkparams"}

func StartNEGController(sharedContext *sharedcontext.SharedContext, logger klog.Logger, proj *projectcrd.Project) (chan struct{}, error) {
	cloud, err := multiprojectgce.NewGCEForProject(*sharedContext.DefaultCloudConfigFile, proj, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCE client for project %+v: %v", proj, err)
	}

	projectName := proj.ProjectName()

	// Using informer factory, create required namespaced informers for the NEG controller.
	ingressInformer := projectinformer.NewProjectInformer(sharedContext.InformersFactory.Networking().V1().Ingresses().Informer(), projectName)
	serviceInformer := projectinformer.NewProjectInformer(sharedContext.InformersFactory.Core().V1().Services().Informer(), projectName)
	podInformer := projectinformer.NewProjectInformer(sharedContext.InformersFactory.Core().V1().Pods().Informer(), projectName)
	nodeInformer := projectinformer.NewProjectInformer(sharedContext.InformersFactory.Core().V1().Nodes().Informer(), projectName)
	endpointSliceInformer := projectinformer.NewProjectInformer(sharedContext.InformersFactory.Discovery().V1().EndpointSlices().Informer(), projectName)

	svcNegInformer, err := sharedContext.InformersFactory.ForResource(svcNegResource)
	if err != nil {
		return nil, fmt.Errorf("failed to get svcNeg informer: %v", err)
	}
	projectFilteredSvcNegInformer := projectinformer.NewProjectInformer(svcNegInformer.Informer(), projectName)

	networkInformer, err := sharedContext.InformersFactory.ForResource(gkeNetworkParamsResource)
	if err != nil {
		return nil, fmt.Errorf("failed to get network informer: %v", err)
	}
	projectFilteredNetworkInformer := projectinformer.NewProjectInformer(networkInformer.Informer(), projectName)

	gkeNetworkParamsInformer, err := sharedContext.InformersFactory.ForResource(gkeNetworkParamsResource)
	if err != nil {
		return nil, fmt.Errorf("failed to get gkeNetworkParams informer: %v", err)
	}
	projectFilteredGkeNetworkParamsInformer := projectinformer.NewProjectInformer(gkeNetworkParamsInformer.Informer(), projectName)

	// Create a function to check if all the informers have synced.
	hasSynced := func() bool {
		return ingressInformer.HasSynced() &&
			serviceInformer.HasSynced() &&
			podInformer.HasSynced() &&
			nodeInformer.HasSynced() &&
			endpointSliceInformer.HasSynced() &&
			projectFilteredSvcNegInformer.HasSynced() &&
			projectFilteredNetworkInformer.HasSynced() &&
			projectFilteredGkeNetworkParamsInformer.HasSynced()
	}

	zoneGetter := zonegetter.NewZoneGetter(nodeInformer, cloud.SubnetworkURL())

	// Create a channel to stop the controller for this specific project.
	projectStopCh := make(chan struct{})
	defer close(projectStopCh)

	// joinedStopCh is a channel that will be closed when the global stop channel or the project stop channel is closed.
	joinedStopCh := make(chan struct{})
	go func() {
		defer close(joinedStopCh)
		select {
		case <-sharedContext.GlobalStopCh:
		case <-projectStopCh:
		}
	}()

	negController := createNEGController(
		sharedContext.KubeClient,
		sharedContext.SvcNegClient,
		sharedContext.EventRecorderClient,
		sharedContext.KubeSystemUID,
		ingressInformer,
		serviceInformer,
		podInformer,
		nodeInformer,
		endpointSliceInformer,
		projectFilteredSvcNegInformer,
		projectFilteredNetworkInformer,
		projectFilteredGkeNetworkParamsInformer,
		hasSynced,
		cloud,
		zoneGetter,
		sharedContext.ClusterNamer,
		sharedContext.L4Namer,
		sharedContext.LpConfig,
		joinedStopCh,
		logger,
	)

	go negController.Run()

	return projectStopCh, nil
}

func createNEGController(
	kubeClient kubernetes.Interface,
	svcNegClient svcnegclient.Interface,
	eventRecorderClient kubernetes.Interface,
	kubeSystemUID types.UID,
	ingressInformer cache.SharedIndexInformer,
	serviceInformer cache.SharedIndexInformer,
	podInformer cache.SharedIndexInformer,
	nodeInformer cache.SharedIndexInformer,
	endpointSliceInformer cache.SharedIndexInformer,
	svcNegInformer cache.SharedIndexInformer,
	networkInformer cache.SharedIndexInformer,
	gkeNetworkParamsInformer cache.SharedIndexInformer,
	hasSynced func() bool,
	cloud *gce.Cloud,
	zoneGetter *zonegetter.ZoneGetter,
	clusterNamer *namer.Namer,
	l4Namer *namer.L4Namer,
	lpConfig labels.PodLabelPropagationConfig,
	stopCh <-chan struct{},
	logger klog.Logger,
) *neg.Controller {

	// The following adapter will use Network Selflink as Network Url instead of the NetworkUrl itself.
	// Network Selflink is always composed by the network name even if the cluster was initialized with Network Id.
	// All the components created from it will be consistent and always use the Url with network name and not the url with netowork Id
	adapter, err := network.NewAdapterNetworkSelfLink(cloud)
	if err != nil {
		logger.Error(err, "Failed to create network adapter with SelfLink")
		// if it was not possible to retrieve network information use standard context as cloud network provider
		adapter = cloud
	}

	noDefaultBackendServicePort := utils.ServicePort{} // we don't need default backend service port for standalone NEGs.

	var noNodeTopologyInformer cache.SharedIndexInformer = nil

	asmServiceNEGSkipNamespaces := []string{}
	enableASM := false

	negController := neg.NewController(
		kubeClient,
		svcNegClient,
		eventRecorderClient,
		kubeSystemUID,
		ingressInformer,
		serviceInformer,
		podInformer,
		nodeInformer,
		endpointSliceInformer,
		svcNegInformer,
		networkInformer,
		gkeNetworkParamsInformer,
		noNodeTopologyInformer,
		hasSynced,
		l4Namer,
		noDefaultBackendServicePort,
		negtypes.NewAdapterWithRateLimitSpecs(cloud, flags.F.GCERateLimit.Values(), adapter),
		zoneGetter,
		clusterNamer,
		flags.F.ResyncPeriod,
		flags.F.NegGCPeriod,
		flags.F.NumNegGCWorkers,
		flags.F.EnableReadinessReflector,
		flags.F.EnableL4NEG,
		flags.F.EnableNonGCPMode,
		flags.F.EnableDualStackNEG,
		enableASM,
		asmServiceNEGSkipNamespaces,
		lpConfig,
		flags.F.EnableMultiNetworking,
		flags.F.EnableIngressRegionalExternal,
		flags.F.EnableL4NetLBNEG,
		stopCh,
		logger,
	)

	return negController
}
