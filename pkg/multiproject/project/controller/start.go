package controller

import (
	ingresscontext "k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/flags"
	"k8s.io/ingress-gce/pkg/instancegroups"
	"k8s.io/ingress-gce/pkg/metrics"
	"k8s.io/ingress-gce/pkg/translator"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/endpointslices"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/ingress-gce/pkg/utils/zonegetter"
	"k8s.io/klog/v2"
)

type MultiProjectControllerContext struct {
}

func StartMultiProjectController(ctx *ingresscontext.ControllerContext, stopCh <-chan struct{}, logger klog.Logger) error {

}

// NewControllerContext returns a new shared set of informers.
func NewControllerContext(
	kubeClient kubernetes.Interface,
	svcnegClient svcnegclient.Interface,
	ingParamsClient ingparamsclient.Interface,
	saClient serviceattachmentclient.Interface,
	networkClient networkclient.Interface,
	nodeTopologyClient nodetopologyclient.Interface,
	eventRecorderClient kubernetes.Interface,
	cloud *gce.Cloud,
	clusterNamer *namer.Namer,
	kubeSystemUID types.UID,
	config ControllerContextConfig,
	logger klog.Logger) *ControllerContext {
	logger = logger.WithName("ControllerContext")

	podInformer := informerv1.NewPodInformer(kubeClient, config.Namespace, config.ResyncPeriod, utils.NewNamespaceIndexer())
	nodeInformer := informerv1.NewNodeInformer(kubeClient, config.ResyncPeriod, utils.NewNamespaceIndexer())

	// Error in SetTransform() doesn't affect the correctness but the memory efficiency
	if err := podInformer.SetTransform(preserveNeeded); err != nil {
		logger.Error(err, "unable to SetTransForm")
	}
	if err := nodeInformer.SetTransform(preserveNeeded); err != nil {
		logger.Error(err, "unable to SetTransForm")
	}

	context := &ControllerContext{
		KubeClient:              kubeClient,
		FirewallClient:          firewallClient,
		SvcNegClient:            svcnegClient,
		SAClient:                saClient,
		EventRecorderClient:     eventRecorderClient,
		NodeTopologyClient:      nodeTopologyClient,
		Cloud:                   cloud,
		ClusterNamer:            clusterNamer,
		L4Namer:                 namer.NewL4Namer(string(kubeSystemUID), clusterNamer),
		KubeSystemUID:           kubeSystemUID,
		ControllerMetrics:       metrics.NewControllerMetrics(flags.F.MetricsExportInterval, flags.F.L4NetLBProvisionDeadline, flags.F.EnableL4NetLBDualStack, flags.F.EnableL4ILBDualStack, logger),
		ControllerContextConfig: config,
		IngressInformer:         informernetworking.NewIngressInformer(kubeClient, config.Namespace, config.ResyncPeriod, utils.NewNamespaceIndexer()),
		ServiceInformer:         informerv1.NewServiceInformer(kubeClient, config.Namespace, config.ResyncPeriod, utils.NewNamespaceIndexer()),
		BackendConfigInformer:   informerbackendconfig.NewBackendConfigInformer(backendConfigClient, config.Namespace, config.ResyncPeriod, utils.NewNamespaceIndexer()),
		PodInformer:             podInformer,
		NodeInformer:            nodeInformer,
		SvcNegInformer:          informersvcneg.NewServiceNetworkEndpointGroupInformer(svcnegClient, config.Namespace, config.ResyncPeriod, utils.NewNamespaceIndexer()),
		recorders:               map[string]record.EventRecorder{},
		healthChecks:            make(map[string]func() error),
		logger:                  logger,
	}
	if firewallClient != nil {
		context.FirewallInformer = informerfirewall.NewGCPFirewallInformer(firewallClient, config.ResyncPeriod, utils.NewNamespaceIndexer())
	}
	if config.FrontendConfigEnabled {
		context.FrontendConfigInformer = informerfrontendconfig.NewFrontendConfigInformer(frontendConfigClient, config.Namespace, config.ResyncPeriod, utils.NewNamespaceIndexer())
	}
	if ingParamsClient != nil {
		context.IngClassInformer = informernetworking.NewIngressClassInformer(kubeClient, config.ResyncPeriod, utils.NewNamespaceIndexer())
		context.IngParamsInformer = informeringparams.NewGCPIngressParamsInformer(ingParamsClient, config.ResyncPeriod, utils.NewNamespaceIndexer())
	}

	if saClient != nil {
		context.SAInformer = informerserviceattachment.NewServiceAttachmentInformer(saClient, config.Namespace, config.ResyncPeriod, utils.NewNamespaceIndexer())
	}

	if networkClient != nil {
		context.NetworkInformer = informernetwork.NewNetworkInformer(networkClient, config.ResyncPeriod, utils.NewNamespaceIndexer())
		context.GKENetworkParamsInformer = informernetwork.NewGKENetworkParamSetInformer(networkClient, config.ResyncPeriod, utils.NewNamespaceIndexer())
	}

	if flags.F.GKEClusterType == ClusterTypeRegional {
		context.RegionalCluster = true
	}

	if flags.F.EnableMultiSubnetClusterPhase1 {
		if nodeTopologyClient != nil {
			context.NodeTopologyInformer = informernodetopology.NewNodeTopologyInformer(nodeTopologyClient, config.ResyncPeriod, utils.NewNamespaceIndexer())
		}
	}

	// Do not trigger periodic resync on EndpointSlices object.
	// This aims improve NEG controller performance by avoiding unnecessary NEG sync that triggers for each NEG syncer.
	// As periodic resync may temporary starve NEG API ratelimit quota.
	context.EndpointSliceInformer = discoveryinformer.NewEndpointSliceInformer(kubeClient, config.Namespace, 0,
		cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, endpointslices.EndpointSlicesByServiceIndex: endpointslices.EndpointSlicesByServiceFunc})

	context.Translator = translator.NewTranslator(
		context.ServiceInformer,
		context.BackendConfigInformer,
		context.NodeInformer,
		context.PodInformer,
		context.EndpointSliceInformer,
		context.KubeClient,
		context,
		flags.F.EnableTransparentHealthChecks,
		context.EnableIngressRegionalExternal,
		logger,
	)
	// The subnet specified in gce.conf is considered as the default subnet.
	context.ZoneGetter = zonegetter.NewZoneGetter(context.NodeInformer, context.Cloud.SubnetworkURL())
	context.InstancePool = instancegroups.NewManager(&instancegroups.ManagerConfig{
		Cloud:      context.Cloud,
		Namer:      context.ClusterNamer,
		Recorders:  context,
		BasePath:   utils.GetBasePath(context.Cloud),
		ZoneGetter: context.ZoneGetter,
		MaxIGSize:  config.MaxIGSize,
	})

	return context
}
