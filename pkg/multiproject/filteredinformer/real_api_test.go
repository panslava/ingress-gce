package filteredinformer

import (
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestWithDelayedWatchEvents simulates the real API behavior where watch events
// might be delayed or buffered
func TestWithDelayedWatchEvents(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

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
	
	// Add reactor to simulate delayed watch events (like real API)
	watchStarted := make(chan struct{})
	fakeClient.PrependWatchReactor("services", func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		close(watchStarted)
		// Simulate delay in watch establishment
		time.Sleep(50 * time.Millisecond)
		return false, nil, nil
	})

	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start config-a first
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	
	// Wait for watch to be established
	<-watchStarted
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

	// Now start config-b
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

	time.Sleep(500 * time.Millisecond)

	muB.Lock()
	if len(eventsB) == 0 {
		t.Error("Config-b didn't receive event with delayed watch")
	}
	muB.Unlock()
}

// TestWithCustomListWatch tests with a custom ListWatch that simulates real API behavior
func TestWithCustomListWatch(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

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
	
	// Track list calls
	listCallCount := 0
	var listCallMu sync.Mutex
	
	fakeClient.PrependReactor("list", "services", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
		listCallMu.Lock()
		listCallCount++
		count := listCallCount
		listCallMu.Unlock()
		
		t.Logf("List call #%d for services", count)
		// Only first list should happen (from config-a starting)
		// Config-b should NOT trigger a new list
		if count > 1 {
			t.Error("Unexpected second list call - config-b should reuse existing cache")
		}
		return false, nil, nil
	})

	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start config-a
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

	// Reset list call tracking
	listCallMu.Lock()
	beforeConfigB := listCallCount
	listCallMu.Unlock()

	// Start config-b
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

	time.Sleep(500 * time.Millisecond)

	// Verify no additional list calls
	listCallMu.Lock()
	afterConfigB := listCallCount
	listCallMu.Unlock()
	
	if afterConfigB > beforeConfigB {
		t.Errorf("Config-b triggered %d additional list calls", afterConfigB-beforeConfigB)
	}

	// Check if config-b received the service
	muB.Lock()
	if len(eventsB) == 0 {
		t.Error("Config-b didn't receive synthetic event for existing service")
		t.Log("This might indicate the bug is related to cache population timing")
	} else {
		t.Logf("Config-b received: %v", eventsB)
	}
	muB.Unlock()
}

// TestInformerCacheConsistency verifies cache consistency between multiple filtered informers
func TestInformerCacheConsistency(t *testing.T) {
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

	// Start base informer directly (not through filtered informer)
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Verify base cache has both services
	baseStore := baseInformer.GetStore()
	baseItems := baseStore.List()
	t.Logf("Base cache has %d items", len(baseItems))
	if len(baseItems) != 2 {
		t.Errorf("Expected 2 items in base cache, got %d", len(baseItems))
	}

	// Now create filtered informers for already-running base
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")

	// Both should report synced immediately
	if !filteredA.HasSynced() {
		t.Error("Filtered-A should be synced immediately")
	}
	if !filteredB.HasSynced() {
		t.Error("Filtered-B should be synced immediately")
	}

	// Add handlers and check for synthetic events
	eventsA := []string{}
	eventsB := []string{}
	var muA, muB sync.Mutex

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

	time.Sleep(200 * time.Millisecond)

	// Check results
	muA.Lock()
	if len(eventsA) != 1 || eventsA[0] != "service-a" {
		t.Errorf("Config-A expected [service-a], got %v", eventsA)
	}
	muA.Unlock()

	muB.Lock()
	if len(eventsB) != 1 || eventsB[0] != "service-b" {
		t.Errorf("Config-B expected [service-b], got %v", eventsB)
	}
	muB.Unlock()

	// Verify filtered caches
	filteredStoreA := filteredA.GetStore()
	itemsA := filteredStoreA.List()
	if len(itemsA) != 1 {
		t.Errorf("Filtered-A cache should have 1 item, got %d", len(itemsA))
	}

	filteredStoreB := filteredB.GetStore()
	itemsB := filteredStoreB.List()
	if len(itemsB) != 1 {
		t.Errorf("Filtered-B cache should have 1 item, got %d", len(itemsB))
	}
}