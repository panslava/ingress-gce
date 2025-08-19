package filteredinformer

import (
	"context"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestSyntheticAddEventsForRunningInformer verifies that when a handler is added
// to an already-running informer, it receives synthetic Add events for existing objects
func TestSyntheticAddEventsForRunningInformer(t *testing.T) {
	// Create fake client with some existing services
	service1 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service1",
			Namespace: "default",
		},
	}
	service2 := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service2",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewSimpleClientset(service1, service2)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0) // No resync for this test

	// Get the service informer
	serviceInformer := informerFactory.Core().V1().Services().Informer()

	// Start the informer and wait for it to sync
	stopCh := make(chan struct{})
	defer close(stopCh)

	go serviceInformer.Run(stopCh)

	// Wait for the informer to sync
	if !cache.WaitForCacheSync(stopCh, serviceInformer.HasSynced) {
		t.Fatal("Failed to sync informer")
	}

	// Now add a handler to the already-running informer
	receivedEvents := make([]string, 0)
	var mu sync.Mutex

	_, err := serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			mu.Lock()
			receivedEvents = append(receivedEvents, "add:"+svc.Name)
			mu.Unlock()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			svc := newObj.(*v1.Service)
			mu.Lock()
			receivedEvents = append(receivedEvents, "update:"+svc.Name)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Failed to add event handler: %v", err)
	}

	// Give some time for events to be processed
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Verify we received synthetic Add events for existing objects
	if len(receivedEvents) != 2 {
		t.Errorf("Expected 2 synthetic Add events, got %d: %v", len(receivedEvents), receivedEvents)
	}

	// Check that we got Add events (not Update events)
	for _, event := range receivedEvents {
		if event != "add:service1" && event != "add:service2" {
			t.Errorf("Unexpected event: %s", event)
		}
	}
}

// TestFilteredInformerWithAlreadyRunningBase tests the scenario where a filtered
// informer wraps an already-running base informer
func TestFilteredInformerWithAlreadyRunningBase(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.provider.config"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create services with different provider configs
	serviceA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-a",
			Namespace: "default",
			Labels: map[string]string{
				"test.provider.config": "config-a",
			},
		},
	}
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-b",
			Namespace: "default",
			Labels: map[string]string{
				"test.provider.config": "config-b",
			},
		},
	}
	serviceNoLabel := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-no-label",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceA, serviceB, serviceNoLabel)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)

	// Get the base service informer
	baseInformer := informerFactory.Core().V1().Services().Informer()

	// Create first filtered informer for config-a
	filteredInformerA := NewProviderConfigFilteredInformer(baseInformer, "config-a")

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start the first filtered informer (which starts the base informer)
	go filteredInformerA.Run(stopCh)

	// Wait for sync
	if !cache.WaitForCacheSync(stopCh, filteredInformerA.HasSynced) {
		t.Fatal("Failed to sync first filtered informer")
	}

	// Add handler for config-a
	receivedEventsA := make([]string, 0)
	var muA sync.Mutex

	filteredInformerA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muA.Lock()
			receivedEventsA = append(receivedEventsA, svc.Name)
			muA.Unlock()
		},
	})

	// Give time for events to process
	time.Sleep(100 * time.Millisecond)

	// Now create second filtered informer for config-b (base informer already running)
	filteredInformerB := NewProviderConfigFilteredInformer(baseInformer, "config-b")

	// Try to run it (should be no-op since base is already running)
	go filteredInformerB.Run(stopCh)

	// Add handler for config-b
	receivedEventsB := make([]string, 0)
	var muB sync.Mutex

	filteredInformerB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muB.Lock()
			receivedEventsB = append(receivedEventsB, svc.Name)
			muB.Unlock()
		},
	})

	// Give time for events to process
	time.Sleep(100 * time.Millisecond)

	// Verify config-a only received service-a
	muA.Lock()
	if len(receivedEventsA) != 1 || receivedEventsA[0] != "service-a" {
		t.Errorf("Config-a expected [service-a], got %v", receivedEventsA)
	}
	muA.Unlock()

	// Verify config-b received service-b (this is the bug - it might not receive it)
	muB.Lock()
	if len(receivedEventsB) != 1 || receivedEventsB[0] != "service-b" {
		t.Errorf("Config-b expected [service-b], got %v", receivedEventsB)
		t.Log("BUG REPRODUCED: Second filtered informer didn't receive synthetic Add event")
	}
	muB.Unlock()
}

// TestResyncWithFilteredInformers tests that resync works correctly with filtered informers
func TestResyncWithFilteredInformers(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.provider.config"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Labels: map[string]string{
				"test.provider.config": "config-a",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(service)

	// Create factory with short resync period
	resyncPeriod := 1 * time.Second
	informerFactory := informers.NewSharedInformerFactory(fakeClient, resyncPeriod)

	baseInformer := informerFactory.Core().V1().Services().Informer()
	filteredInformer := NewProviderConfigFilteredInformer(baseInformer, "config-a")

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start the filtered informer
	go filteredInformer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, filteredInformer.HasSynced) {
		t.Fatal("Failed to sync informer")
	}

	eventCount := 0
	var mu sync.Mutex

	filteredInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			eventCount++
			mu.Unlock()
			t.Logf("Add event received (count: %d)", eventCount)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			mu.Lock()
			eventCount++
			mu.Unlock()
			t.Logf("Update event received (count: %d)", eventCount)
		},
	})

	// Wait for at least 5 resync periods
	time.Sleep(resyncPeriod * 5)

	mu.Lock()
	defer mu.Unlock()

	// We should have received:
	// 1. Initial Add event
	// 2. At least 2 Update events from resync
	if eventCount < 3 {
		t.Errorf("Expected at least 3 events (1 Add + 2 resync Updates), got %d", eventCount)
		t.Log("Potential issue: Resync not working with filtered informer")
	}
}

// TestMultiProviderConfigScenario reproduces the exact bug scenario
func TestMultiProviderConfigScenario(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.provider.config"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Simulate the exact scenario:
	// 1. Provider config A starts
	// 2. Service for provider config B is created
	// 3. Provider config B starts later

	fakeClient := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute) // 10 min resync like production

	baseInformer := informerFactory.Core().V1().Services().Informer()

	// Step 1: Start provider config A
	filteredInformerA := NewProviderConfigFilteredInformer(baseInformer, "config-a")

	stopCh := make(chan struct{})
	defer close(stopCh)

	go filteredInformerA.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, filteredInformerA.HasSynced) {
		t.Fatal("Failed to sync config-a informer")
	}

	eventsA := make([]string, 0)
	var muA sync.Mutex

	filteredInformerA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muA.Lock()
			eventsA = append(eventsA, "add:"+svc.Name)
			muA.Unlock()
			t.Logf("Config-A received Add: %s", svc.Name)
		},
	})

	// Step 2: Create service for provider config B
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-for-b",
			Namespace: "default",
			Labels: map[string]string{
				"test.provider.config": "config-b",
			},
		},
	}

	ctx := context.Background()
	_, err := fakeClient.CoreV1().Services("default").Create(ctx, serviceB, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	// Give time for the create to be processed
	time.Sleep(100 * time.Millisecond)

	// Step 3: Start provider config B (simulating hours later)
	filteredInformerB := NewProviderConfigFilteredInformer(baseInformer, "config-b")

	// This Run should be a no-op since base informer is already running
	go filteredInformerB.Run(stopCh)

	eventsB := make([]string, 0)
	var muB sync.Mutex

	// Add handler for config B - this is where the bug happens
	filteredInformerB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muB.Lock()
			eventsB = append(eventsB, "add:"+svc.Name)
			muB.Unlock()
			t.Logf("Config-B received Add: %s", svc.Name)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			svc := newObj.(*v1.Service)
			muB.Lock()
			eventsB = append(eventsB, "update:"+svc.Name)
			muB.Unlock()
			t.Logf("Config-B received Update: %s", svc.Name)
		},
	})

	// Wait to see if config B receives the service
	time.Sleep(500 * time.Millisecond)

	// Check results
	muA.Lock()
	if len(eventsA) != 0 {
		t.Errorf("Config-A should not have received any events, got %v", eventsA)
	}
	muA.Unlock()

	muB.Lock()
	if len(eventsB) == 0 {
		t.Error("BUG CONFIRMED: Config-B did not receive synthetic Add event for existing service")
		t.Log("This reproduces the production bug where the second provider config doesn't process existing services")
	} else if eventsB[0] != "add:service-for-b" {
		t.Errorf("Config-B expected to receive 'add:service-for-b', got %v", eventsB)
	} else {
		t.Log("UNEXPECTED: Config-B correctly received the synthetic Add event")
		t.Log("The bug might be more complex than initially thought")
	}
	muB.Unlock()
}

// TestInformerStartOrder tests if the order of starting informers vs adding handlers matters
func TestInformerStartOrder(t *testing.T) {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
	}

	// Test 1: Add handler BEFORE starting informer
	t.Run("HandlerBeforeStart", func(t *testing.T) {
		fakeClient := fake.NewSimpleClientset(service)
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
		serviceInformer := informerFactory.Core().V1().Services().Informer()

		received := false
		serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				received = true
			},
		})

		stopCh := make(chan struct{})
		defer close(stopCh)

		go serviceInformer.Run(stopCh)
		cache.WaitForCacheSync(stopCh, serviceInformer.HasSynced)
		time.Sleep(100 * time.Millisecond)

		if !received {
			t.Error("Handler added before start didn't receive event")
		}
	})

	// Test 2: Add handler AFTER starting informer
	t.Run("HandlerAfterStart", func(t *testing.T) {
		fakeClient := fake.NewSimpleClientset(service)
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
		serviceInformer := informerFactory.Core().V1().Services().Informer()

		stopCh := make(chan struct{})
		defer close(stopCh)

		go serviceInformer.Run(stopCh)
		cache.WaitForCacheSync(stopCh, serviceInformer.HasSynced)

		received := false
		serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				received = true
			},
		})

		time.Sleep(100 * time.Millisecond)

		if !received {
			t.Error("Handler added after start didn't receive synthetic event")
		}
	})
}

// TestFilteringResourceEventHandler tests that the FilteringResourceEventHandler works correctly
func TestFilteringResourceEventHandler(t *testing.T) {
	matchingService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "matching",
			Namespace: "default",
			Labels: map[string]string{
				"filter": "match",
			},
		},
	}
	nonMatchingService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "non-matching",
			Namespace: "default",
			Labels: map[string]string{
				"filter": "no-match",
			},
		},
	}

	received := make([]string, 0)
	var mu sync.Mutex

	filterFunc := func(obj interface{}) bool {
		svc := obj.(*v1.Service)
		return svc.Labels["filter"] == "match"
	}

	handler := cache.FilteringResourceEventHandler{
		FilterFunc: filterFunc,
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				mu.Lock()
				received = append(received, svc.Name)
				mu.Unlock()
			},
		},
	}

	// Test Add events
	handler.OnAdd(matchingService, false)
	handler.OnAdd(nonMatchingService, false)

	mu.Lock()
	defer mu.Unlock()

	if len(received) != 1 || received[0] != "matching" {
		t.Errorf("FilteringResourceEventHandler didn't filter correctly, received: %v", received)
	}
}
