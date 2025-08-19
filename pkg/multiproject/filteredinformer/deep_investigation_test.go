package filteredinformer

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestHasSyncedBehavior tests if HasSynced() behaves differently for fresh vs reused informers
func TestHasSyncedBehavior(t *testing.T) {
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
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Test 1: Fresh informer
	t.Run("FreshInformer", func(t *testing.T) {
		filtered := NewProviderConfigFilteredInformer(baseInformer, "config-a")
		
		// Check HasSynced before starting
		if filtered.HasSynced() {
			t.Error("Fresh informer should not be synced before starting")
		}
		
		// Start and check sync
		go filtered.Run(stopCh)
		
		// Wait for sync with timeout
		synced := cache.WaitForCacheSync(stopCh, filtered.HasSynced)
		if !synced {
			t.Error("Failed to sync fresh informer")
		}
		t.Log("Fresh informer synced successfully")
	})

	// Test 2: Reused informer (base already running)
	t.Run("ReusedInformer", func(t *testing.T) {
		filtered2 := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		
		// Check HasSynced before "starting" (base already running)
		syncedBefore := filtered2.HasSynced()
		t.Logf("Reused informer HasSynced() before Run(): %v", syncedBefore)
		
		// Try to start (should be no-op)
		go filtered2.Run(stopCh)
		
		// Check HasSynced immediately after
		syncedAfter := filtered2.HasSynced()
		t.Logf("Reused informer HasSynced() after Run(): %v", syncedAfter)
		
		if !syncedAfter {
			t.Error("Reused informer should report as synced since base is already synced")
		}
	})
}

// TestFactoryStartVsIndividualRun tests the difference between factory.Start() and individual Run()
func TestFactoryStartVsIndividualRun(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.provider.config"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

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

	// Test 1: Using factory.Start()
	t.Run("UsingFactoryStart", func(t *testing.T) {
		fakeClient := fake.NewSimpleClientset(serviceA, serviceB)
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
		
		stopCh := make(chan struct{})
		defer close(stopCh)
		
		// Get informers BEFORE starting factory
		baseInformer := informerFactory.Core().V1().Services().Informer()
		filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
		filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		
		eventsA := []string{}
		eventsB := []string{}
		var muA, muB sync.Mutex
		
		// Add handlers BEFORE starting
		filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				muA.Lock()
				eventsA = append(eventsA, svc.Name)
				muA.Unlock()
			},
		})
		
		filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				muB.Lock()
				eventsB = append(eventsB, svc.Name)
				muB.Unlock()
			},
		})
		
		// Start using factory.Start()
		informerFactory.Start(stopCh)
		
		// Wait for sync
		informerFactory.WaitForCacheSync(stopCh)
		time.Sleep(100 * time.Millisecond)
		
		muA.Lock()
		muB.Lock()
		if len(eventsA) != 1 || eventsA[0] != "service-a" {
			t.Errorf("With factory.Start(), config-a expected [service-a], got %v", eventsA)
		}
		if len(eventsB) != 1 || eventsB[0] != "service-b" {
			t.Errorf("With factory.Start(), config-b expected [service-b], got %v", eventsB)
		}
		muB.Unlock()
		muA.Unlock()
	})

	// Test 2: Using individual Run() calls
	t.Run("UsingIndividualRun", func(t *testing.T) {
		fakeClient := fake.NewSimpleClientset(serviceA, serviceB)
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
		
		stopCh := make(chan struct{})
		defer close(stopCh)
		
		baseInformer := informerFactory.Core().V1().Services().Informer()
		filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
		filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		
		eventsA := []string{}
		eventsB := []string{}
		var muA, muB sync.Mutex
		
		// Start first filtered informer
		go filteredA.Run(stopCh)
		cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
		
		// Add handler for A after starting
		filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				muA.Lock()
				eventsA = append(eventsA, svc.Name)
				muA.Unlock()
			},
		})
		
		time.Sleep(100 * time.Millisecond)
		
		// Now start second filtered informer
		go filteredB.Run(stopCh)
		
		// Add handler for B after "starting" (base already running)
		filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				muB.Lock()
				eventsB = append(eventsB, svc.Name)
				muB.Unlock()
			},
		})
		
		time.Sleep(100 * time.Millisecond)
		
		muA.Lock()
		muB.Lock()
		if len(eventsA) != 1 || eventsA[0] != "service-a" {
			t.Errorf("With individual Run(), config-a expected [service-a], got %v", eventsA)
		}
		if len(eventsB) != 1 || eventsB[0] != "service-b" {
			t.Errorf("With individual Run(), config-b expected [service-b], got %v", eventsB)
		}
		muB.Unlock()
		muA.Unlock()
	})
}

// TestSyntheticEventDeliveryTiming tests exactly when synthetic events are delivered
func TestSyntheticEventDeliveryTiming(t *testing.T) {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	serviceInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start informer
	go serviceInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, serviceInformer.HasSynced)

	// Add handler and track when event is received
	eventReceived := make(chan time.Time, 1)
	startTime := time.Now()
	
	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eventReceived <- time.Now()
		},
	})
	
	// Wait for event with timeout
	select {
	case receivedTime := <-eventReceived:
		elapsed := receivedTime.Sub(startTime)
		t.Logf("Synthetic event received after %v", elapsed)
		if elapsed > 100*time.Millisecond {
			t.Errorf("Synthetic event took too long: %v", elapsed)
		}
	case <-time.After(1 * time.Second):
		t.Error("Synthetic event not received within 1 second")
	}
}

// TestHandlerAddedInGoroutineRace tests if there's a race when handler is added in a goroutine
func TestHandlerAddedInGoroutineRace(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.provider.config"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Labels: map[string]string{
				"test.provider.config": "config-b",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start first provider config
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)

	// Now simulate the multiproject flow more closely
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Start in a goroutine (like in initializeInformers)
	go filteredB.Run(stopCh)
	
	// Simulate the pattern from the actual code where handlers might be added
	// slightly after Run() is called
	eventReceived := int32(0)
	
	// Add a small delay to simulate real-world timing
	time.Sleep(10 * time.Millisecond)
	
	// Now add handler (like in createNEGController)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&eventReceived, 1)
			t.Log("Event received in race test")
		},
	})
	
	// Wait for potential event
	time.Sleep(500 * time.Millisecond)
	
	if atomic.LoadInt32(&eventReceived) != 1 {
		t.Error("Handler added after Run() in goroutine didn't receive event")
	}
}

// TestFilteredCacheListBehavior tests the filtered cache's List() behavior
func TestFilteredCacheListBehavior(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.provider.config"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

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
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start base informer
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Create filtered informers
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")

	// Test base informer's cache
	baseList := baseInformer.GetStore().List()
	t.Logf("Base informer cache has %d items", len(baseList))
	if len(baseList) != 3 {
		t.Errorf("Base informer should have 3 items, got %d", len(baseList))
	}

	// Test filtered caches
	filteredListA := filteredA.GetStore().List()
	filteredListB := filteredB.GetStore().List()
	
	t.Logf("Filtered A cache has %d items", len(filteredListA))
	t.Logf("Filtered B cache has %d items", len(filteredListB))
	
	if len(filteredListA) != 1 {
		t.Errorf("Filtered A should have 1 item, got %d", len(filteredListA))
	}
	if len(filteredListB) != 1 {
		t.Errorf("Filtered B should have 1 item, got %d", len(filteredListB))
	}
	
	// Verify the correct items are in each cache
	if len(filteredListA) > 0 {
		svc := filteredListA[0].(*v1.Service)
		if svc.Name != "service-a" {
			t.Errorf("Filtered A should have service-a, got %s", svc.Name)
		}
	}
	if len(filteredListB) > 0 {
		svc := filteredListB[0].(*v1.Service)
		if svc.Name != "service-b" {
			t.Errorf("Filtered B should have service-b, got %s", svc.Name)
		}
	}
}

// TestWaitForCacheSyncBehavior tests how WaitForCacheSync behaves with filtered informers
func TestWaitForCacheSyncBehavior(t *testing.T) {
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

	// Start base informer
	go baseInformer.Run(stopCh)
	
	// Create filtered informer for already-running base
	filtered := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	
	// Try to run it (should be no-op)
	go filtered.Run(stopCh)
	
	// Test WaitForCacheSync behavior
	t.Run("WaitForCacheSync", func(t *testing.T) {
		// This simulates what the NEG controller does
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		
		synced := false
		err := wait.PollUntilContextCancel(ctx, 100*time.Millisecond, true, func(ctx context.Context) (bool, error) {
			synced = filtered.HasSynced()
			t.Logf("Checking HasSynced: %v", synced)
			return synced, nil
		})
		
		if err != nil {
			t.Errorf("Failed to wait for sync: %v", err)
		}
		if !synced {
			t.Error("Filtered informer never reported as synced")
		}
	})
}

// TestInformerStateAfterMultipleRuns tests the internal state after multiple Run() calls
func TestInformerStateAfterMultipleRuns(t *testing.T) {
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

	// Track how many times handlers are called
	handlerCalls := make(map[string]int32)
	var mu sync.Mutex

	// Run the base informer multiple times and add handlers each time
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("handler-%d", i)
		
		// Try to run (only first should actually start it)
		go baseInformer.Run(stopCh)
		
		// Add a handler
		baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				mu.Lock()
				handlerCalls[name]++
				mu.Unlock()
				t.Logf("Handler %s received event", name)
			},
		})
		
		// Give time for processing
		time.Sleep(100 * time.Millisecond)
	}

	// Check that all handlers received events
	mu.Lock()
	defer mu.Unlock()
	
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("handler-%d", i)
		if handlerCalls[name] != 1 {
			t.Errorf("Handler %s received %d events, expected 1", name, handlerCalls[name])
		}
	}
}

// TestProviderConfigLabelMissing tests what happens when the label is missing initially
func TestProviderConfigLabelMissing(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.provider.config"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create service WITHOUT the label initially
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
			Labels:    map[string]string{}, // No provider config label
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
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
			eventsA = append(eventsA, "add:"+svc.Name)
			muA.Unlock()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			svc := newObj.(*v1.Service)
			muA.Lock()
			eventsA = append(eventsA, "update:"+svc.Name)
			muA.Unlock()
		},
	})

	time.Sleep(100 * time.Millisecond)

	// Config A should not have received the service (no label)
	muA.Lock()
	if len(eventsA) != 0 {
		t.Errorf("Config A should not have received unlabeled service, got %v", eventsA)
	}
	muA.Unlock()

	// Now add the label to the service
	ctx := context.Background()
	service.Labels["test.provider.config"] = "config-a"
	_, err := fakeClient.CoreV1().Services("default").Update(ctx, service, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to update service: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	// Now config A should receive an Add event (not Update) because it's new to the filter
	muA.Lock()
	if len(eventsA) != 1 || eventsA[0] != "add:test-service" {
		t.Errorf("Config A expected [add:test-service] after label added, got %v", eventsA)
	}
	muA.Unlock()

	// Now start provider config B
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	eventsB := []string{}
	var muB sync.Mutex

	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muB.Lock()
			eventsB = append(eventsB, "add:"+svc.Name)
			muB.Unlock()
		},
	})

	time.Sleep(100 * time.Millisecond)

	// Config B should not receive the service (wrong label value)
	muB.Lock()
	if len(eventsB) != 0 {
		t.Errorf("Config B should not have received service with config-a label, got %v", eventsB)
	}
	muB.Unlock()
}