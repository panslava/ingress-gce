package neg

import (
	ingresscontext "k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/flags"
)

func NewNEGContext() *ingresscontext.ControllerContext {
	ctxConfig := ingresscontext.ControllerContextConfig{
		ResyncPeriod: flags.F.ResyncPeriod,
	}

	ctx := ingctx.NewControllerContext(kubeClient, backendConfigClient, frontendConfigClient, firewallCRClient, svcNegClient, ingParamsClient, svcAttachmentClient, networkClient, nodeTopologyClient, eventRecorderKubeClient, cloud, namer, kubeSystemUID, ctxConfig, rootLogger)

}
