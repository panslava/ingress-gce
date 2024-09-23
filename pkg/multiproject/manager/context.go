package manager

import (
	"context"
	"fmt"

	firewallcrclient "github.com/GoogleCloudPlatform/gke-networking-api/client/gcpfirewall/clientset/versioned"
	networkclient "github.com/GoogleCloudPlatform/gke-networking-api/client/network/clientset/versioned"
	nodetopologyclient "github.com/GoogleCloudPlatform/gke-networking-api/client/nodetopology/clientset/versioned"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	backendconfigclient "k8s.io/ingress-gce/pkg/backendconfig/client/clientset/versioned"
	ingresscontext "k8s.io/ingress-gce/pkg/context"
	frontendconfigclient "k8s.io/ingress-gce/pkg/frontendconfig/client/clientset/versioned"
	ingparamsclient "k8s.io/ingress-gce/pkg/ingparams/client/clientset/versioned"
	serviceattachmentclient "k8s.io/ingress-gce/pkg/serviceattachment/client/clientset/versioned"
	svcnegclient "k8s.io/ingress-gce/pkg/svcneg/client/clientset/versioned"
	"k8s.io/klog/v2"

	"k8s.io/ingress-gce/cmd/glbc/app"
	"k8s.io/ingress-gce/pkg/crd"
	"k8s.io/ingress-gce/pkg/firewalls"
	"k8s.io/ingress-gce/pkg/flags"
)

func newProjectControllerContext(proj *crd.Project, rootLogger klog.Logger) (*ingresscontext.ControllerContext, error) {
	// Create kube-config for the project
	kubeConfig, err := app.NewKubeConfig(rootLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client config: %v", err)
	}

	kubeClient, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	backendConfigClient, err := backendconfigclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create BackendConfig client: %v", err)
	}

	frontendConfigClient, err := frontendconfigclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create FrontendConfig client: %v", err)
	}

	firewallCRClient, err := firewallcrclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Firewall client: %v", err)
	}

	svcNegClient, err := svcnegclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create NetworkEndpointGroup client: %v", err)
	}

	ingParamsClient, err := ingparamsclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCPIngressParams client: %v", err)
	}

	svcAttachmentClient, err := serviceattachmentclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create ServiceAttachment client: %v", err)
	}

	networkClient, err := networkclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Network client: %v", err)
	}

	nodeTopologyClient, err := nodetopologyclient.NewForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create Node Topology Client: %v", err)
	}

	cloud := app.NewGCEClient(rootLogger)

	namer, err := app.NewNamer(kubeClient, flags.F.ClusterName, firewalls.DefaultFirewallName, rootLogger)
	if err != nil {
		return nil, fmt.Errorf("app.NewNamer(ctx.KubeClient, %q, %q) = %v", flags.F.ClusterName, firewalls.DefaultFirewallName, err)
	}

	kubeSystemNS, err := kubeClient.CoreV1().Namespaces().Get(context.TODO(), "kube-system", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("error getting kube-system namespace: %v", err)
	}
	kubeSystemUID := kubeSystemNS.GetUID()

	ctxConfig := ingresscontext.ControllerContextConfig{
		Namespace:                     flags.F.WatchNamespace,
		ResyncPeriod:                  flags.F.ResyncPeriod,
		NumL4Workers:                  flags.F.NumL4Workers,
		NumL4NetLBWorkers:             flags.F.NumL4NetLBWorkers,
		DefaultBackendSvcPort:         app.DefaultBackendServicePort(kubeClient, rootLogger),
		HealthCheckPath:               flags.F.HealthCheckPath,
		FrontendConfigEnabled:         flags.F.EnableFrontendConfig,
		EnableASMConfigMap:            flags.F.EnableASMConfigMapBasedConfig,
		ASMConfigMapNamespace:         flags.F.ASMConfigMapBasedConfigNamespace,
		ASMConfigMapName:              flags.F.ASMConfigMapBasedConfigCMName,
		MaxIGSize:                     flags.F.MaxIGSize,
		EnableL4ILBDualStack:          flags.F.EnableL4ILBDualStack,
		EnableL4NetLBDualStack:        flags.F.EnableL4NetLBDualStack,
		EnableL4StrongSessionAffinity: flags.F.EnableL4StrongSessionAffinity,
		EnableMultinetworking:         flags.F.EnableMultiNetworking,
		EnableIngressRegionalExternal: flags.F.EnableIngressRegionalExternal,
		EnableWeightedL4ILB:           flags.F.EnableWeightedL4ILB,
		EnableWeightedL4NetLB:         flags.F.EnableWeightedL4NetLB,
		DisableL4LBFirewall:           flags.F.DisableL4LBFirewall,
	}

	return ingresscontext.NewControllerContext(kubeConfig, kubeClient, backendConfigClient, frontendConfigClient, firewallCRClient, svcNegClient, ingParamsClient, svcAttachmentClient, networkClient, nodeTopologyClient, eventRecorderKubeClient, cloud, namer, kubeSystemUID, ctxConfig, rootLogger), nil
}
