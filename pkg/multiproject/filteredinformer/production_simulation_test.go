package filteredinformer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestExactProductionSimulation simulates the exact production scenario
func TestExactProductionSimulation(t *testing.T) {
	// In production:
	// 1. Services exist in the cluster
	// 2. Provider config A is created, starts controllers
	// 3. Hours later, provider config B is created
	// 4. Config B's controller doesn't process existing services for hours
	
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Services exist BEFORE any provider config
	serviceA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-a",
			Namespace: "production",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "prod-config-a",
			},
			Annotations: map[string]string{
				"cloud.google.com/neg": `{"exposed_ports": {"80": {}}}`,
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
				},
			},
		},
	}
	
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-b",
			Namespace: "production",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "prod-config-b",
			},
			Annotations: map[string]string{
				"cloud.google.com/neg": `{"exposed_ports": {"443": {}}}`,
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "https",
					Port:       443,
					TargetPort: intstr.FromInt(8443),
				},
			},
		},
	}

	// Both services exist from the beginning
	fakeClient := fake.NewSimpleClientset(serviceA, serviceB)
	
	// Single shared informer factory (like production)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	
	globalStopCh := make(chan struct{})
	defer close(globalStopCh)

	// === PHASE 1: Provider Config A is created ===
	t.Log("=== PHASE 1: Provider Config A created ===")
	
	// Simulate StartControllersForProviderConfig for config A
	configAResults := simulateProviderConfigController(t, "prod-config-a", informerFactory, globalStopCh)
	
	// Give time for processing
	time.Sleep(200 * time.Millisecond)
	
	// Check that config A processed its service
	if configAResults.servicesProcessed.Load() != 1 {
		t.Errorf("Config A should have processed 1 service, got %d", configAResults.servicesProcessed.Load())
	}
	t.Logf("Config A processed services: %v", configAResults.getProcessedServices())
	
	// === PHASE 2: Hours later, Provider Config B is created ===
	t.Log("=== PHASE 2: Provider Config B created (simulating hours later) ===")
	
	// In production, this happens hours later when base informers are fully running
	// and have been processing other configs for a long time
	time.Sleep(100 * time.Millisecond) // Simulate time passing
	
	// Simulate StartControllersForProviderConfig for config B
	configBResults := simulateProviderConfigController(t, "prod-config-b", informerFactory, globalStopCh)
	
	// Give time for processing
	time.Sleep(500 * time.Millisecond)
	
	// Check if config B processed its service
	processedCount := configBResults.servicesProcessed.Load()
	if processedCount == 0 {
		t.Error("BUG REPRODUCED: Config B didn't process any services")
		t.Log("This matches the production bug where second provider config doesn't process existing services")
		
		// Debug information
		t.Logf("Config B work queue size: %d", len(configBResults.workQueue))
		t.Logf("Config B events received: %d", configBResults.eventsReceived.Load())
		t.Logf("Config B hasSynced: %v", configBResults.hasSynced())
	} else {
		t.Logf("Config B processed %d services: %v", processedCount, configBResults.getProcessedServices())
		t.Log("Bug NOT reproduced in test - synthetic events worked")
	}
}

type controllerResults struct {
	servicesProcessed atomic.Int32
	eventsReceived    atomic.Int32
	processedList     []string
	mu                sync.Mutex
	workQueue         chan string
	hasSynced         func() bool
}

func (cr *controllerResults) getProcessedServices() []string {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return append([]string{}, cr.processedList...)
}

func simulateProviderConfigController(t *testing.T, configName string, factory informers.SharedInformerFactory, globalStopCh <-chan struct{}) *controllerResults {
	results := &controllerResults{
		workQueue: make(chan string, 100),
	}
	
	// Provider config specific stop channel
	providerConfigStopCh := make(chan struct{})
	
	// Joined stop channel
	joinedStopCh := make(chan struct{})
	go func() {
		select {
		case <-globalStopCh:
		case <-providerConfigStopCh:
		}
		close(joinedStopCh)
	}()
	
	// === Initialize Informers (like initializeInformers in neg.go) ===
	baseServiceInformer := factory.Core().V1().Services().Informer()
	serviceInformer := NewProviderConfigFilteredInformer(baseServiceInformer, configName)
	
	// Start the informer
	go serviceInformer.Run(joinedStopCh)
	
	// Create hasSynced function
	results.hasSynced = func() bool {
		return serviceInformer.HasSynced()
	}
	
	// === Simulate NEG Controller ===
	
	// Wait for sync (like controller.Run does)
	go func() {
		for !results.hasSynced() {
			time.Sleep(10 * time.Millisecond)
		}
		t.Logf("[%s] hasSynced=true, starting workers", configName)
		
		// Start worker
		go func() {
			for key := range results.workQueue {
				// Simulate processService
				results.mu.Lock()
				results.processedList = append(results.processedList, key)
				results.mu.Unlock()
				results.servicesProcessed.Add(1)
				t.Logf("[%s] Worker processed: %s", configName, key)
			}
		}()
	}()
	
	// Add handlers (like in NewController)
	// This happens AFTER informer.Run() but potentially before hasSynced
	time.Sleep(20 * time.Millisecond) // Simulate delay in controller creation
	
	lister := serviceInformer.GetIndexer()
	
	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			results.eventsReceived.Add(1)
			t.Logf("[%s] Handler enqueuing: %s", configName, key)
			
			// Simulate enqueueService
			results.workQueue <- key
			
			// Check if lister has it (like processService does)
			_, exists, _ := lister.GetByKey(key)
			if !exists {
				t.Errorf("[%s] CRITICAL: Service %s not in lister when processing", configName, key)
			}
		},
		UpdateFunc: func(old, cur interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(cur)
			results.eventsReceived.Add(1)
			t.Logf("[%s] Handler enqueuing update: %s", configName, key)
			results.workQueue <- key
		},
	})
	
	return results
}

// TestResyncDelayInProduction tests if resync is delayed in production scenarios
func TestResyncDelayInProduction(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	
	// Use shorter resync for testing
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 2*time.Second)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start config-a first
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	// Record when config-a started
	configAStartTime := time.Now()
	
	// Wait a bit (simulating hours in production)
	time.Sleep(1 * time.Second)
	
	// Start config-b
	configBStartTime := time.Now()
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	eventsReceived := []string{}
	var mu sync.Mutex
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			eventsReceived = append(eventsReceived, "add")
			mu.Unlock()
			t.Logf("Config-B: Add event at %v", time.Since(configBStartTime))
		},
		UpdateFunc: func(old, cur interface{}) {
			mu.Lock()
			eventsReceived = append(eventsReceived, "update")
			mu.Unlock()
			t.Logf("Config-B: Update (resync) event at %v", time.Since(configBStartTime))
		},
	})
	
	// Wait for potential resync
	time.Sleep(4 * time.Second)
	
	mu.Lock()
	defer mu.Unlock()
	
	t.Logf("Config-A ran for: %v", time.Since(configAStartTime))
	t.Logf("Config-B ran for: %v", time.Since(configBStartTime))
	t.Logf("Config-B received events: %v", eventsReceived)
	
	if len(eventsReceived) < 2 {
		t.Error("Config-B didn't receive expected resync events")
		t.Log("This could explain why resync takes hours instead of minutes in production")
	}
}