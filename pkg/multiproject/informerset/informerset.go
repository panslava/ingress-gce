package informerset

import (
	"fmt"

	networkclient "github.com/GoogleCloudPlatform/gke-networking-api/client/network/clientset/versioned"
	informernetwork "github.com/GoogleCloudPlatform/gke-networking-api/client/network/informers/externalversions/network/v1"
	nodetopologyclient "github.com/GoogleCloudPlatform/gke-networking-api/client/nodetopology/clientset/versioned"
	informernodetopology "github.com/GoogleCloudPlatform/gke-networking-api/client/nodetopology/informers/externalversions/nodetopology/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	informerv1 "k8s.io/client-go/informers/core/v1"
	discoveryinformer "k8s.io/client-go/informers/discovery/v1"
	informernetworking "k8s.io/client-go/informers/networking/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/multiproject/filteredinformer"
	svcnegclient "k8s.io/ingress-gce/pkg/svcneg/client/clientset/versioned"
	informersvcneg "k8s.io/ingress-gce/pkg/svcneg/client/informers/externalversions/svcneg/v1beta1"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/endpointslices"
	"k8s.io/klog/v2"
)

// InformerSet manages all shared informers used by multiproject controllers.
// It provides centralized initialization and lifecycle management.
type InformerSet struct {
	// Core Kubernetes informers (always present)
	Ingress       cache.SharedIndexInformer
	Service       cache.SharedIndexInformer
	Pod           cache.SharedIndexInformer
	Node          cache.SharedIndexInformer
	EndpointSlice cache.SharedIndexInformer

	// Custom resource informers (may be nil)
	SvcNeg           cache.SharedIndexInformer // ServiceNetworkEndpointGroups CRD
	Network          cache.SharedIndexInformer // GKE Network CRD
	GkeNetworkParams cache.SharedIndexInformer // GKENetworkParamSets CRD
	NodeTopology     cache.SharedIndexInformer // NodeTopology CRD

	// State tracking
	started bool
}

// NewInformerSet creates and initializes a new InformerSet with all required informers.
func NewInformerSet(
	kubeClient kubernetes.Interface,
	svcNegClient svcnegclient.Interface,
	networkClient networkclient.Interface,
	nodeTopologyClient nodetopologyclient.Interface,
	resyncPeriod metav1.Duration,
) *InformerSet {
	informers := &InformerSet{}

	// Create core Kubernetes informers
	informers.Ingress = informernetworking.NewIngressInformer(
		kubeClient,
		metav1.NamespaceAll,
		resyncPeriod.Duration,
		utils.NewNamespaceIndexer(),
	)

	informers.Service = informerv1.NewServiceInformer(
		kubeClient,
		metav1.NamespaceAll,
		resyncPeriod.Duration,
		utils.NewNamespaceIndexer(),
	)

	informers.Pod = informerv1.NewPodInformer(
		kubeClient,
		metav1.NamespaceAll,
		resyncPeriod.Duration,
		utils.NewNamespaceIndexer(),
	)

	informers.Node = informerv1.NewNodeInformer(
		kubeClient,
		resyncPeriod.Duration,
		utils.NewNamespaceIndexer(),
	)

	// EndpointSlice informer with custom indexers for NEG controller
	informers.EndpointSlice = discoveryinformer.NewEndpointSliceInformer(
		kubeClient,
		metav1.NamespaceAll,
		resyncPeriod.Duration,
		cache.Indexers{
			cache.NamespaceIndex:                        cache.MetaNamespaceIndexFunc,
			endpointslices.EndpointSlicesByServiceIndex: endpointslices.EndpointSlicesByServiceFunc,
		},
	)

	// Create CRD informers if clients are available
	if svcNegClient != nil {
		informers.SvcNeg = informersvcneg.NewServiceNetworkEndpointGroupInformer(
			svcNegClient,
			metav1.NamespaceAll,
			resyncPeriod.Duration,
			utils.NewNamespaceIndexer(),
		)
	}

	if networkClient != nil {
		informers.Network = informernetwork.NewNetworkInformer(
			networkClient,
			resyncPeriod.Duration,
			utils.NewNamespaceIndexer(),
		)

		informers.GkeNetworkParams = informernetwork.NewGKENetworkParamSetInformer(
			networkClient,
			resyncPeriod.Duration,
			utils.NewNamespaceIndexer(),
		)
	}

	if nodeTopologyClient != nil {
		informers.NodeTopology = informernodetopology.NewNodeTopologyInformer(
			nodeTopologyClient,
			resyncPeriod.Duration,
			utils.NewNamespaceIndexer(),
		)
	}

	return informers
}

// Start starts all informers and waits for their caches to sync.
// It returns an error if the informers are already started.
func (i *InformerSet) Start(stopCh <-chan struct{}, logger klog.Logger) error {
	if i.started {
		return nil // Already started
	}

	// Start all core informers
	go i.Ingress.Run(stopCh)
	go i.Service.Run(stopCh)
	go i.Pod.Run(stopCh)
	go i.Node.Run(stopCh)
	go i.EndpointSlice.Run(stopCh)

	// Start optional informers
	if i.SvcNeg != nil {
		go i.SvcNeg.Run(stopCh)
	}
	if i.Network != nil {
		go i.Network.Run(stopCh)
	}
	if i.GkeNetworkParams != nil {
		go i.GkeNetworkParams.Run(stopCh)
	}
	if i.NodeTopology != nil {
		go i.NodeTopology.Run(stopCh)
	}

	i.started = true

	// Wait for initial sync
    logger.Info("Waiting for informer caches to sync")
    if !cache.WaitForCacheSync(stopCh, i.CombinedHasSynced()) {
        err := fmt.Errorf("failed to sync informer caches")
        logger.Error(err, "Failed to sync informer caches")
        return err
    }

	logger.Info("Informer caches synced successfully")
	return nil
}

// FilterByProviderConfig creates a new InformerSet with all informers wrapped in a ProviderConfig filter.
// This is used for provider-config-specific controllers.
func (i *InformerSet) FilterByProviderConfig(providerConfigName string) *InformerSet {
	filteredInformers := &InformerSet{
		started: true, // Wrapped informers are already started
	}

	// Wrap core informers
	if i.Ingress != nil {
		filteredInformers.Ingress = newProviderConfigFilteredInformer(i.Ingress, providerConfigName)
	}
	if i.Service != nil {
		filteredInformers.Service = newProviderConfigFilteredInformer(i.Service, providerConfigName)
	}
	if i.Pod != nil {
		filteredInformers.Pod = newProviderConfigFilteredInformer(i.Pod, providerConfigName)
	}
	if i.Node != nil {
		filteredInformers.Node = newProviderConfigFilteredInformer(i.Node, providerConfigName)
	}
	if i.EndpointSlice != nil {
		filteredInformers.EndpointSlice = newProviderConfigFilteredInformer(i.EndpointSlice, providerConfigName)
	}

	// Wrap optional informers
	if i.SvcNeg != nil {
		filteredInformers.SvcNeg = newProviderConfigFilteredInformer(i.SvcNeg, providerConfigName)
	}
	if i.Network != nil {
		filteredInformers.Network = newProviderConfigFilteredInformer(i.Network, providerConfigName)
	}
	if i.GkeNetworkParams != nil {
		filteredInformers.GkeNetworkParams = newProviderConfigFilteredInformer(i.GkeNetworkParams, providerConfigName)
	}
	if i.NodeTopology != nil {
		filteredInformers.NodeTopology = newProviderConfigFilteredInformer(i.NodeTopology, providerConfigName)
	}

	return filteredInformers
}

// newProviderConfigFilteredInformer wraps an informer with a provider config filter.
func newProviderConfigFilteredInformer(informer cache.SharedIndexInformer, providerConfigName string) cache.SharedIndexInformer {
	return filteredinformer.NewProviderConfigFilteredInformer(informer, providerConfigName)
}

// CombinedHasSynced returns a function that checks if all informers have synced.
func (i *InformerSet) CombinedHasSynced() func() bool {
	syncFuncs := i.hasSyncedFuncs()
	return func() bool {
		for _, hasSynced := range syncFuncs {
			if !hasSynced() {
				return false
			}
		}
		return true
	}
}

// hasSyncedFuncs returns a list of HasSynced functions for all non-nil informers.
func (i *InformerSet) hasSyncedFuncs() []func() bool {
	var funcs []func() bool

	// Core informers (always present)
	if i.Ingress != nil {
		funcs = append(funcs, i.Ingress.HasSynced)
	}
	if i.Service != nil {
		funcs = append(funcs, i.Service.HasSynced)
	}
	if i.Pod != nil {
		funcs = append(funcs, i.Pod.HasSynced)
	}
	if i.Node != nil {
		funcs = append(funcs, i.Node.HasSynced)
	}
	if i.EndpointSlice != nil {
		funcs = append(funcs, i.EndpointSlice.HasSynced)
	}

	// Optional informers
	if i.SvcNeg != nil {
		funcs = append(funcs, i.SvcNeg.HasSynced)
	}
	if i.Network != nil {
		funcs = append(funcs, i.Network.HasSynced)
	}
	if i.GkeNetworkParams != nil {
		funcs = append(funcs, i.GkeNetworkParams.HasSynced)
	}
	if i.NodeTopology != nil {
		funcs = append(funcs, i.NodeTopology.HasSynced)
	}

	return funcs
}
