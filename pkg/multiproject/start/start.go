package start

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"

	"k8s.io/apimachinery/pkg/types"
	informers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/ingress-gce/cmd/glbc/app"
	ingresscontext "k8s.io/ingress-gce/pkg/context"
	"k8s.io/ingress-gce/pkg/flags"
	_ "k8s.io/ingress-gce/pkg/klog"
	pccontroller "k8s.io/ingress-gce/pkg/multiproject/controller"
	"k8s.io/ingress-gce/pkg/multiproject/manager"
	"k8s.io/ingress-gce/pkg/neg/syncers/labels"
	providerconfigclient "k8s.io/ingress-gce/pkg/providerconfig/client/clientset/versioned"
	providerconfiginformers "k8s.io/ingress-gce/pkg/providerconfig/client/informers/externalversions"
	"k8s.io/ingress-gce/pkg/recorders"
	svcnegclient "k8s.io/ingress-gce/pkg/svcneg/client/clientset/versioned"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/klog/v2"
)

const multiProjectLeaderElectionLockName = "ingress-gce-multi-project-lock"

func StartWithLeaderElection(
	ctx context.Context,
	leaderElectKubeClient kubernetes.Interface,
	hostname string,
	kubeConfig *rest.Config,
	logger klog.Logger,
	kubeClient kubernetes.Interface,
	svcNegClient svcnegclient.Interface,
	kubeSystemUID types.UID,
	eventRecorderKubeClient kubernetes.Interface,
	rootNamer *namer.Namer,
	stopCh <-chan struct{},
) error {
	recordersManager := recorders.NewManager(eventRecorderKubeClient, logger)

	leConfig, err := makeLeaderElectionConfig(leaderElectKubeClient, hostname, recordersManager, kubeConfig, logger, kubeClient, svcNegClient, kubeSystemUID, eventRecorderKubeClient, rootNamer, stopCh)
	if err != nil {
		return err
	}

	leaderelection.RunOrDie(ctx, *leConfig)
	logger.Info("Multi-project controller exited.")

	return nil
}

func makeLeaderElectionConfig(
	leaderElectKubeClient kubernetes.Interface,
	hostname string,
	recordersManager *recorders.Manager,
	kubeConfig *rest.Config,
	logger klog.Logger,
	kubeClient kubernetes.Interface,
	svcNegClient svcnegclient.Interface,
	kubeSystemUID types.UID,
	eventRecorderKubeClient kubernetes.Interface,
	rootNamer *namer.Namer,
	stopCh <-chan struct{},
) (*leaderelection.LeaderElectionConfig, error) {
	recorder := recordersManager.Recorder(flags.F.LeaderElection.LockObjectNamespace)
	// add a uniquifier so that two processes on the same host don't accidentally both become active
	id := fmt.Sprintf("%v_%x", hostname, rand.Intn(1e6))

	rl, err := resourcelock.New(resourcelock.LeasesResourceLock,
		flags.F.LeaderElection.LockObjectNamespace,
		multiProjectLeaderElectionLockName,
		leaderElectKubeClient.CoreV1(),
		leaderElectKubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: recorder,
		})
	if err != nil {
		return nil, fmt.Errorf("couldn't create resource lock: %v", err)
	}

	return &leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: flags.F.LeaderElection.LeaseDuration.Duration,
		RenewDeadline: flags.F.LeaderElection.RenewDeadline.Duration,
		RetryPeriod:   flags.F.LeaderElection.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(context.Context) {
				start(kubeConfig, logger, kubeClient, svcNegClient, kubeSystemUID, eventRecorderKubeClient, rootNamer, stopCh)
			},
			OnStoppedLeading: func() {
				logger.Info("Stop running multi-project leader election")
			},
		},
	}, nil
}

func start(
	kubeConfig *rest.Config,
	logger klog.Logger,
	kubeClient kubernetes.Interface,
	svcNegClient svcnegclient.Interface,
	kubeSystemUID types.UID,
	eventRecorderKubeClient kubernetes.Interface,
	rootNamer *namer.Namer,
	stopCh <-chan struct{},
) {
	providerConfigClient, err := providerconfigclient.NewForConfig(kubeConfig)
	if err != nil {
		klog.Fatalf("Failed to create ProviderConfig client: %v", err)
	}

	lpConfig := labels.PodLabelPropagationConfig{}
	if flags.F.EnableNEGLabelPropagation {
		lpConfigEnvVar := os.Getenv("LABEL_PROPAGATION_CONFIG")
		if err := json.Unmarshal([]byte(lpConfigEnvVar), &lpConfig); err != nil {
			logger.Error(err, "Failed to retrieve pod label propagation config")
		}
	}

	ctxConfig := ingresscontext.ControllerContextConfig{
		ResyncPeriod: flags.F.ResyncPeriod,
	}

	defaultGCEConfig, err := app.GCEConfString(logger)
	if err != nil {
		klog.Fatalf("Error getting default cluster GCE config: %v", err)
	}

	informersFactory := informers.NewSharedInformerFactoryWithOptions(kubeClient, ctxConfig.ResyncPeriod)

	providerConfigInformer := providerconfiginformers.NewSharedInformerFactory(providerConfigClient, ctxConfig.ResyncPeriod).Flags().V1().ProviderConfigs().Informer()

	manager := manager.NewProviderConfigControllerManager(
		kubeClient,
		informersFactory,
		providerConfigClient,
		svcNegClient,
		eventRecorderKubeClient,
		kubeSystemUID,
		rootNamer,
		namer.NewL4Namer(string(kubeSystemUID), rootNamer),
		lpConfig,
		defaultGCEConfig,
		stopCh,
		logger,
	)

	pcController := pccontroller.NewProviderConfigController(manager, providerConfigInformer, stopCh, logger)

	pcController.Run()
}
