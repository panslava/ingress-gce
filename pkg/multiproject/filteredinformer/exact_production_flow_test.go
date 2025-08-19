package filteredinformer

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
	"k8s.io/klog/v2"
)

// This test mimics the exact production flow from multiproject/neg/neg.go
func TestExactProductionFlow(t *testing.T) {
	// Set up the provider config label key (same as production)
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create service for provider-config-b (exists before config-b starts)
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-for-config-b",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "provider-config-b",
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{Port: 80},
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceB)
	
	// Create shared informer factory with 10 minute resync (like production)
	informersFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)

	globalStopCh := make(chan struct{})
	defer close(globalStopCh)

	// Simulate first provider config (config-a) starting
	t.Log("=== Starting provider-config-a ===")
	configAStopCh, configAEvents := simulateProviderConfig(t, "provider-config-a", informersFactory, globalStopCh)
	
	// Wait a bit to ensure config-a is fully running
	time.Sleep(200 * time.Millisecond)

	// Now simulate second provider config (config-b) starting
	t.Log("=== Starting provider-config-b ===")
	configBStopCh, configBEvents := simulateProviderConfig(t, "provider-config-b", informersFactory, globalStopCh)

	// Wait for events to be processed
	time.Sleep(500 * time.Millisecond)

	// Check results
	configAEvents.mu.Lock()
	if len(configAEvents.events) != 0 {
		t.Errorf("Config-a should not have received any events, got %v", configAEvents.events)
	}
	configAEvents.mu.Unlock()

	configBEvents.mu.Lock()
	if len(configBEvents.events) == 0 {
		t.Error("BUG REPRODUCED: Config-b did not receive the service")
		t.Log("This matches the production bug where second provider config doesn't process existing services")
	} else {
		t.Logf("Config-b received events: %v", configBEvents.events)
		if configBEvents.events[0] != "add:service-for-config-b" {
			t.Errorf("Expected add:service-for-config-b, got %v", configBEvents.events[0])
		}
	}
	configBEvents.mu.Unlock()

	// Clean up
	close(configAStopCh)
	close(configBStopCh)
}

// simulateProviderConfig mimics what happens in multiproject/neg/neg.go StartNEGController
func simulateProviderConfig(t *testing.T, providerConfigName string, informersFactory informers.SharedInformerFactory, globalStopCh <-chan struct{}) (chan struct{}, *eventTracker) {
	logger := klog.TODO()
	logger.V(2).Info("Initializing NEG controller", "providerConfig", providerConfigName)

	// Provider config specific stop channel
	providerConfigStopCh := make(chan struct{})

	// joinedStopCh (closes when either global or provider-specific closes)
	joinedStopCh := make(chan struct{})
	go func() {
		defer close(joinedStopCh)
		select {
		case <-globalStopCh:
			logger.V(2).Info("Global stop channel triggered")
		case <-providerConfigStopCh:
			logger.V(2).Info("Provider config stop channel triggered")
		}
	}()

	// Initialize informers (mimics initializeInformers function)
	informers, hasSynced := initializeInformersLikeProduction(t, informersFactory, providerConfigName, joinedStopCh)

	// Create NEG controller (mimics createNEGController)
	events := createNEGControllerLikeProduction(t, informers, hasSynced, joinedStopCh, logger)

	logger.V(2).Info("Starting NEG controller run loop", "providerConfig", providerConfigName)
	// In production: go negController.Run()
	// We'll simulate by just waiting for sync
	go func() {
		wait.PollUntil(5*time.Second, func() (bool, error) {
			logger.V(2).Info("Waiting for initial sync")
			return hasSynced(), nil
		}, joinedStopCh)
		logger.V(2).Info("NEG controller started")
	}()

	return providerConfigStopCh, events
}

type eventTracker struct {
	events []string
	mu     sync.Mutex
}

type negInformersLikeProduction struct {
	serviceInformer cache.SharedIndexInformer
}

// initializeInformersLikeProduction mimics the initializeInformers function
func initializeInformersLikeProduction(t *testing.T, informersFactory informers.SharedInformerFactory, providerConfigName string, joinedStopCh <-chan struct{}) (*negInformersLikeProduction, func() bool) {
	// Get base informer (this returns the same instance for all provider configs)
	baseServiceInformer := informersFactory.Core().V1().Services().Informer()
	
	// Wrap in filtered informer
	serviceInformer := NewProviderConfigFilteredInformer(baseServiceInformer, providerConfigName)

	// Start the informer (this is what happens in the actual code)
	// Lines 187-191 in pkg/multiproject/neg/neg.go
	go serviceInformer.Run(joinedStopCh)

	// Prepare hasSynced
	hasSyncedList := []func() bool{
		serviceInformer.HasSynced,
	}

	t.Logf("[%s] NEG informers initialized", providerConfigName)
	
	informers := &negInformersLikeProduction{
		serviceInformer: serviceInformer,
	}
	
	hasSynced := func() bool {
		for _, hs := range hasSyncedList {
			if !hs() {
				return false
			}
		}
		return true
	}

	return informers, hasSynced
}

// createNEGControllerLikeProduction mimics the NEG controller creation
func createNEGControllerLikeProduction(t *testing.T, informers *negInformersLikeProduction, hasSynced func() bool, stopCh <-chan struct{}, logger klog.Logger) *eventTracker {
	events := &eventTracker{
		events: make([]string, 0),
	}

	// This mimics what happens in neg.NewController (pkg/neg/controller.go line 301)
	informers.serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			events.mu.Lock()
			events.events = append(events.events, "add:"+svc.Name)
			events.mu.Unlock()
			t.Logf("Service Add event: %s", svc.Name)
		},
		DeleteFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			events.mu.Lock()
			events.events = append(events.events, "delete:"+svc.Name)
			events.mu.Unlock()
			t.Logf("Service Delete event: %s", svc.Name)
		},
		UpdateFunc: func(old, cur interface{}) {
			svc := cur.(*v1.Service)
			events.mu.Lock()
			events.events = append(events.events, "update:"+svc.Name)
			events.mu.Unlock()
			t.Logf("Service Update event: %s", svc.Name)
		},
	})

	return events
}

// TestWithDelayBetweenRunAndHandler tests if a delay between Run() and AddEventHandler matters
func TestWithDelayBetweenRunAndHandler(t *testing.T) {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
	}

	testCases := []struct {
		name  string
		delay time.Duration
	}{
		{"NoDelay", 0},
		{"10ms", 10 * time.Millisecond},
		{"50ms", 50 * time.Millisecond},
		{"100ms", 100 * time.Millisecond},
		{"500ms", 500 * time.Millisecond},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fakeClient := fake.NewSimpleClientset(service)
			informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
			baseInformer := informerFactory.Core().V1().Services().Informer()

			stopCh := make(chan struct{})
			defer close(stopCh)

			// First provider config starts the base informer
			go baseInformer.Run(stopCh)
			cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)
			
			// Add first handler
			baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {},
			})

			// Now try to add second handler with delay
			filtered := NewProviderConfigFilteredInformer(baseInformer, "config-b")
			go filtered.Run(stopCh) // This will be no-op

			// Add delay before adding handler
			time.Sleep(tc.delay)

			received := false
			filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					received = true
				},
			})

			// Wait for potential event
			time.Sleep(200 * time.Millisecond)

			if !received {
				t.Errorf("With delay %v, handler didn't receive synthetic event", tc.delay)
			}
		})
	}
}

// TestInformerStartedFlagTiming tests the timing of when s.started is set
func TestInformerStartedFlagTiming(t *testing.T) {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	
	// We'll test with multiple goroutines trying to start and add handlers
	for i := 0; i < 10; i++ {
		t.Run(fmt.Sprintf("Iteration%d", i), func(t *testing.T) {
			baseInformer := informerFactory.Core().V1().Services().Informer()
			stopCh := make(chan struct{})
			defer close(stopCh)

			var wg sync.WaitGroup
			received := make([]bool, 3)

			// Start 3 goroutines that try to start the informer and add handlers
			for j := 0; j < 3; j++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					
					// Try to start (only first will succeed)
					go baseInformer.Run(stopCh)
					
					// Immediately add handler
					baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
						AddFunc: func(obj interface{}) {
							received[index] = true
						},
					})
				}(j)
			}

			wg.Wait()
			time.Sleep(200 * time.Millisecond)

			// All handlers should have received the event
			for j, r := range received {
				if !r {
					t.Errorf("Handler %d didn't receive event", j)
				}
			}
		})
	}
}

// TestListWatchTiming tests if there's an issue with the ListWatch timing
func TestListWatchTiming(t *testing.T) {
	// Create a service that will exist before informer starts
	existingService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-service",
			Namespace: "default",
			Labels: map[string]string{
				"provider": "config-b",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(existingService)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start first informer
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Verify the cache has the service
	items := baseInformer.GetStore().List()
	t.Logf("Cache has %d items after sync", len(items))
	if len(items) != 1 {
		t.Errorf("Expected 1 item in cache, got %d", len(items))
	}

	// Now add a new service while informer is running
	ctx := context.Background()
	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "new-service",
			Namespace: "default",
			Labels: map[string]string{
				"provider": "config-b",
			},
		},
	}
	_, err := fakeClient.CoreV1().Services("default").Create(ctx, newService, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	// Add handler to already-running informer
	eventsReceived := []string{}
	var mu sync.Mutex
	
	baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			mu.Lock()
			eventsReceived = append(eventsReceived, svc.Name)
			mu.Unlock()
			t.Logf("Received Add event for: %s", svc.Name)
		},
	})

	// Wait for events
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Should have received synthetic Add for existing-service and real Add for new-service
	if len(eventsReceived) < 1 {
		t.Errorf("Expected at least 1 event, got %d", len(eventsReceived))
	}
	
	// Check if we got the synthetic event for the existing service
	foundExisting := false
	for _, name := range eventsReceived {
		if name == "existing-service" {
			foundExisting = true
			break
		}
	}
	if !foundExisting {
		t.Error("Did not receive synthetic Add event for existing service")
	}
}