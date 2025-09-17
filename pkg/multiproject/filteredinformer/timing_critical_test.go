package filteredinformer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestSlowAPIServer simulates slow API server responses
func TestSlowAPIServer(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-b",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceB)
	
	// Add reactor to simulate slow API responses
	listCallCount := int32(0)
	fakeClient.PrependReactor("list", "services", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		count := atomic.AddInt32(&listCallCount, 1)
		t.Logf("List call #%d", count)
		// First list is fast (config-a), second list is slow (config-b)
		if count > 1 {
			t.Log("Simulating slow API response for second list")
			time.Sleep(200 * time.Millisecond)
		}
		return false, nil, nil
	})

	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start first provider config
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)

	eventsA := []string{}
	var muA sync.Mutex
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muA.Lock()
			eventsA = append(eventsA, svc.Name)
			muA.Unlock()
		},
	})

	time.Sleep(100 * time.Millisecond)

	// Now start second provider config
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	eventsB := []string{}
	var muB sync.Mutex
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muB.Lock()
			eventsB = append(eventsB, svc.Name)
			muB.Unlock()
			t.Logf("Config-b received: %s", svc.Name)
		},
	})

	// Wait for events
	time.Sleep(500 * time.Millisecond)

	muB.Lock()
	if len(eventsB) == 0 {
		t.Error("Config-b didn't receive event even with slow API")
	} else {
		t.Logf("Config-b received %d events: %v", len(eventsB), eventsB)
	}
	muB.Unlock()
}

// TestBlockedEventProcessing simulates blocked event processing
func TestBlockedEventProcessing(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	serviceA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-a",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-a",
			},
		},
	}
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-b",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceA, serviceB)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start first provider config with a BLOCKING handler
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)

	blockingDone := make(chan struct{})
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			t.Log("Config-a handler blocking...")
			// Block for a while to simulate slow processing
			time.Sleep(300 * time.Millisecond)
			close(blockingDone)
			t.Log("Config-a handler unblocked")
		},
	})

	// Immediately start second provider config
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	eventsB := []string{}
	var muB sync.Mutex
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muB.Lock()
			eventsB = append(eventsB, svc.Name)
			muB.Unlock()
			t.Logf("Config-b received: %s", svc.Name)
		},
	})

	// Wait for blocking to complete
	<-blockingDone
	time.Sleep(200 * time.Millisecond)

	muB.Lock()
	if len(eventsB) == 0 {
		t.Error("Config-b didn't receive event when config-a handler was blocking")
	} else {
		t.Logf("Config-b correctly received %d events: %v", len(eventsB), eventsB)
	}
	muB.Unlock()
}

// TestInformerBufferOverflow tests what happens with many events
func TestInformerBufferOverflow(t *testing.T) {
	// Create many services
	var services []runtime.Object
	for i := 0; i < 1000; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"provider": "config-b",
				},
			},
		}
		services = append(services, service)
	}

	fakeClient := fake.NewSimpleClientset(services...)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start first provider config
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Add first handler
	baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// Do nothing, just to have a handler
		},
	})

	// Now add second handler (simulating second provider config)
	receivedCount := int32(0)
	baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&receivedCount, 1)
		},
	})

	// Wait for events to be processed
	time.Sleep(1 * time.Second)

	count := atomic.LoadInt32(&receivedCount)
	t.Logf("Received %d synthetic events out of 1000", count)
	if count != 1000 {
		t.Errorf("Expected 1000 synthetic events, got %d", count)
	}
}

// TestConcurrentProviderConfigs tests many provider configs starting concurrently
func TestConcurrentProviderConfigs(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create services for different provider configs
	var services []runtime.Object
	for i := 0; i < 10; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"cloud.gke.io/provider-config-name": fmt.Sprintf("config-%d", i),
				},
			},
		}
		services = append(services, service)
	}

	fakeClient := fake.NewSimpleClientset(services...)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start all provider configs concurrently
	var wg sync.WaitGroup
	eventCounts := make([]int32, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			
			configName := fmt.Sprintf("config-%d", index)
			filtered := NewProviderConfigFilteredInformer(baseInformer, configName)
			
			// Try to run (only first will actually start the base)
			go filtered.Run(stopCh)
			
			// Add handler
			filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					atomic.AddInt32(&eventCounts[index], 1)
				},
			})
		}(i)
	}

	wg.Wait()
	time.Sleep(1 * time.Second)

	// Check that each provider config received its service
	for i, count := range eventCounts {
		if count != 1 {
			t.Errorf("Config-%d received %d events, expected 1", i, count)
		}
	}
}

// TestHasSyncedRaceCondition specifically tests HasSynced race conditions
func TestHasSyncedRaceCondition(t *testing.T) {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start the base informer in background
	go baseInformer.Run(stopCh)

	// Create filtered informer while base is starting
	filtered := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	
	// Check HasSynced in a tight loop (simulating what NEG controller does)
	syncCheckCount := 0
	for !filtered.HasSynced() {
		syncCheckCount++
		time.Sleep(1 * time.Millisecond)
		if syncCheckCount > 1000 {
			t.Fatal("HasSynced never returned true")
		}
	}
	t.Logf("HasSynced returned true after %d checks", syncCheckCount)

	// Now add handler after HasSynced is true
	received := false
	filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
		},
	})

	time.Sleep(100 * time.Millisecond)

	if !received {
		t.Error("Handler added after HasSynced didn't receive synthetic event")
	}
}

// TestResyncPeriodInheritance tests if resync period is properly inherited
func TestResyncPeriodInheritance(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-a",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	
	// Create factory with 1 second resync
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 1*time.Second)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start first provider config
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)

	eventTypes := []string{}
	var mu sync.Mutex

	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			eventTypes = append(eventTypes, "add")
			mu.Unlock()
			t.Log("Add event received")
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			mu.Lock()
			eventTypes = append(eventTypes, "update")
			mu.Unlock()
			t.Log("Update (resync) event received")
		},
	})

	// Wait for at least 2 resync periods
	time.Sleep(2500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Should have: 1 Add + at least 2 Updates from resync
	addCount := 0
	updateCount := 0
	for _, et := range eventTypes {
		if et == "add" {
			addCount++
		} else if et == "update" {
			updateCount++
		}
	}

	t.Logf("Received %d Add events and %d Update events", addCount, updateCount)
	if addCount != 1 {
		t.Errorf("Expected 1 Add event, got %d", addCount)
	}
	if updateCount < 2 {
		t.Errorf("Expected at least 2 Update events from resync, got %d", updateCount)
		t.Log("This indicates resync is not working properly")
	}

	// Now add second provider config and see if it also gets resync
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredB.Run(stopCh)

	eventTypesB := []string{}
	var muB sync.Mutex

	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			muB.Lock()
			eventTypesB = append(eventTypesB, "add")
			muB.Unlock()
			t.Log("Config-B: Add event received")
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			muB.Lock()
			eventTypesB = append(eventTypesB, "update")
			muB.Unlock()
			t.Log("Config-B: Update (resync) event received")
		},
	})

	// Wait for potential resync
	time.Sleep(2 * time.Second)

	muB.Lock()
	t.Logf("Config-B received events: %v", eventTypesB)
	muB.Unlock()
}