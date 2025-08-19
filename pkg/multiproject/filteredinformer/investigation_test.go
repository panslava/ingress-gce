package filteredinformer

import (
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

// TestInvestigateProductionBug tries to reproduce the exact conditions that cause the bug
func TestInvestigateProductionBug(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create service BEFORE any informer starts (this is key!)
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-for-config-b",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceB)
	
	// Create factory with 10 minute resync (like production)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// === PHASE 1: Config-A starts first ===
	t.Log("PHASE 1: Starting config-a")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	
	// Start the filtered informer (this starts the base informer)
	go filteredA.Run(stopCh)
	
	// Wait for sync
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	t.Log("Config-a synced")

	// Add handler for config-a
	eventsA := []string{}
	var muA sync.Mutex
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muA.Lock()
			eventsA = append(eventsA, svc.Name)
			muA.Unlock()
			t.Logf("Config-a received: %s", svc.Name)
		},
	})

	// Wait a bit
	time.Sleep(200 * time.Millisecond)

	// Config-a should NOT have received the service (different label)
	muA.Lock()
	if len(eventsA) != 0 {
		t.Errorf("Config-a should not have received any events, got %v", eventsA)
	}
	muA.Unlock()

	// === PHASE 2: Config-B starts much later ===
	t.Log("PHASE 2: Starting config-b (simulating hours later)")
	
	// Key insight: In production, this happens hours later
	// The base informer has been running for hours
	// The cache is fully populated
	// HasSynced() returns true immediately
	
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Check HasSynced BEFORE Run
	syncedBeforeRun := filteredB.HasSynced()
	t.Logf("Config-b HasSynced() BEFORE Run(): %v", syncedBeforeRun)
	
	// Try to run (should be no-op)
	go filteredB.Run(stopCh)
	
	// Check HasSynced AFTER Run
	syncedAfterRun := filteredB.HasSynced()
	t.Logf("Config-b HasSynced() AFTER Run(): %v", syncedAfterRun)
	
	// Now add handler
	eventsB := []string{}
	var muB sync.Mutex
	registrationStartTime := time.Now()
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muB.Lock()
			eventsB = append(eventsB, svc.Name)
			muB.Unlock()
			elapsed := time.Since(registrationStartTime)
			t.Logf("Config-b received: %s (after %v)", svc.Name, elapsed)
		},
	})

	// Wait for potential synthetic events
	time.Sleep(500 * time.Millisecond)

	// Check if config-b received the service
	muB.Lock()
	if len(eventsB) == 0 {
		t.Error("BUG REPRODUCED: Config-b did not receive the service")
		t.Log("This matches the production bug!")
		
		// Let's investigate the cache state
		store := baseInformer.GetStore()
		items := store.List()
		t.Logf("Base informer cache has %d items", len(items))
		for _, item := range items {
			if svc, ok := item.(*v1.Service); ok {
				t.Logf("  - Service in cache: %s (labels: %v)", svc.Name, svc.Labels)
			}
		}
		
		// Check filtered store
		filteredStore := filteredB.GetStore()
		filteredItems := filteredStore.List()
		t.Logf("Filtered-B cache has %d items", len(filteredItems))
		for _, item := range filteredItems {
			if svc, ok := item.(*v1.Service); ok {
				t.Logf("  - Service in filtered cache: %s", svc.Name)
			}
		}
	} else {
		t.Logf("Config-b received events: %v", eventsB)
		t.Log("Synthetic events worked correctly in test")
	}
	muB.Unlock()

	// === PHASE 3: Test manual cache inspection ===
	t.Log("PHASE 3: Manual cache inspection")
	
	// Try to manually list items through the filtered informer
	filteredStore := filteredB.GetStore()
	items := filteredStore.List()
	t.Logf("Filtered store List() returned %d items", len(items))
	
	// Try GetByKey
	key := "default/service-for-config-b"
	item, exists, err := filteredStore.GetByKey(key)
	if err != nil {
		t.Logf("GetByKey error: %v", err)
	} else if !exists {
		t.Logf("GetByKey: key %s does not exist", key)
	} else {
		svc := item.(*v1.Service)
		t.Logf("GetByKey found: %s", svc.Name)
	}
}

// TestInformerInternalState examines the internal state of informers
func TestInformerInternalState(t *testing.T) {
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

	// Start the informer
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Try to examine if we can detect the informer's started state
	// This is implementation-specific and might help us understand the issue
	
	// Check HasSynced
	if !baseInformer.HasSynced() {
		t.Error("Informer not synced after WaitForCacheSync")
	}

	// Try running again (should log warning)
	t.Log("Attempting to run informer again...")
	done := make(chan struct{})
	go func() {
		baseInformer.Run(stopCh)
		close(done)
	}()

	select {
	case <-done:
		t.Log("Second Run() returned immediately (as expected)")
	case <-time.After(100 * time.Millisecond):
		t.Error("Second Run() did not return immediately")
	}

	// Add handler to already-running informer
	received := false
	baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
			t.Log("Handler received synthetic event")
		},
	})

	time.Sleep(200 * time.Millisecond)

	if !received {
		t.Error("Handler didn't receive synthetic event")
	}
}

// TestTheoryAboutRunTiming tests if the timing of Run() affects synthetic events
func TestTheoryAboutRunTiming(t *testing.T) {
	// Theory: Maybe there's a race condition where if Run() is called
	// but returns immediately (because already started), the handler
	// registration doesn't trigger synthetic events properly

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

	// Start the informer
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Now simulate what happens with second provider config
	// It calls Run() which returns immediately
	runReturned := make(chan struct{})
	go func() {
		baseInformer.Run(stopCh) // This should return immediately
		close(runReturned)
	}()

	// Wait for Run to return
	<-runReturned
	t.Log("Second Run() returned")

	// IMMEDIATELY add handler (simulating race condition)
	received := false
	baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
		},
	})

	// Check if event was received
	time.Sleep(100 * time.Millisecond)
	if !received {
		t.Error("Race condition confirmed: Handler added immediately after Run() returns didn't get event")
	} else {
		t.Log("No race condition: Handler received event even when added immediately after Run()")
	}
}