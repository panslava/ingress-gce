package filteredinformer

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestProductionLikeScenario simulates the production scenario more closely
func TestProductionLikeScenario(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create a fake client
	fakeClient := fake.NewSimpleClientset()

	// Create informer factory with 10 minute resync period (like production)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)

	// Simulate multiple provider configs starting at different times
	baseServiceInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Track events for each provider config
	var eventsConfigA []string
	var eventsConfigB []string
	var muA, muB sync.Mutex

	// Step 1: Start provider config A's filtered informer
	t.Log("Step 1: Starting provider config A")
	filteredInformerA := NewProviderConfigFilteredInformer(baseServiceInformer, "provider-config-a")
	
	// Start the informer (this actually starts the base informer)
	go filteredInformerA.Run(stopCh)
	
	// Wait for sync
	if !cache.WaitForCacheSync(stopCh, filteredInformerA.HasSynced) {
		t.Fatal("Failed to sync provider config A informer")
	}
	t.Log("Provider config A informer synced")

	// Add handler for provider config A
	filteredInformerA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muA.Lock()
			eventsConfigA = append(eventsConfigA, "add:"+svc.Name)
			muA.Unlock()
			t.Logf("Config A: Add event for %s", svc.Name)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			svc := newObj.(*v1.Service)
			muA.Lock()
			eventsConfigA = append(eventsConfigA, "update:"+svc.Name)
			muA.Unlock()
			t.Logf("Config A: Update event for %s", svc.Name)
		},
	})

	// Step 2: Create services for both provider configs
	t.Log("Step 2: Creating services")
	
	serviceA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-a",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "provider-config-a",
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
		},
	}

	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-b",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "provider-config-b",
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
		},
	}

	ctx := context.Background()
	_, err := fakeClient.CoreV1().Services("default").Create(ctx, serviceA, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create service A: %v", err)
	}
	
	_, err = fakeClient.CoreV1().Services("default").Create(ctx, serviceB, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create service B: %v", err)
	}

	// Wait for events to be processed
	time.Sleep(200 * time.Millisecond)

	// Verify config A received its service
	muA.Lock()
	if len(eventsConfigA) != 1 || eventsConfigA[0] != "add:service-a" {
		t.Errorf("Config A expected [add:service-a], got %v", eventsConfigA)
	}
	muA.Unlock()

	// Step 3: Simulate provider config B starting "hours later"
	t.Log("Step 3: Starting provider config B (simulating hours later)")
	
	// Create filtered informer for provider config B
	filteredInformerB := NewProviderConfigFilteredInformer(baseServiceInformer, "provider-config-b")
	
	// Try to run it (should log warning since base is already running)
	go filteredInformerB.Run(stopCh)
	
	// Check if it reports as synced immediately (since base is already synced)
	synced := filteredInformerB.HasSynced()
	t.Logf("Provider config B HasSynced() immediately returns: %v", synced)

	// Add handler for provider config B
	filteredInformerB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			muB.Lock()
			eventsConfigB = append(eventsConfigB, "add:"+svc.Name)
			muB.Unlock()
			t.Logf("Config B: Add event for %s", svc.Name)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			svc := newObj.(*v1.Service)
			muB.Lock()
			eventsConfigB = append(eventsConfigB, "update:"+svc.Name)
			muB.Unlock()
			t.Logf("Config B: Update event for %s", svc.Name)
		},
	})

	// Wait for synthetic events to be processed
	time.Sleep(500 * time.Millisecond)

	// Check if config B received its service
	muB.Lock()
	if len(eventsConfigB) == 0 {
		t.Error("BUG: Config B did not receive synthetic Add event for existing service")
	} else if eventsConfigB[0] != "add:service-b" {
		t.Errorf("Config B expected [add:service-b], got %v", eventsConfigB)
	} else {
		t.Log("Config B correctly received synthetic Add event")
	}
	muB.Unlock()

	// Step 4: Test if resync would help
	t.Log("Step 4: Testing if manual update triggers processing")
	
	// Simulate an update to service B (like what might happen during leader election change)
	serviceB.Annotations = map[string]string{"test": "update"}
	_, err = fakeClient.CoreV1().Services("default").Update(ctx, serviceB, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("Failed to update service B: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	muB.Lock()
	t.Logf("Config B events after update: %v", eventsConfigB)
	muB.Unlock()
}

// TestRaceConditionInHandlerRegistration tests for race conditions
func TestRaceConditionInHandlerRegistration(t *testing.T) {
	// Set up the provider config label key
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
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start first filtered informer
	filteredInformerA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredInformerA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredInformerA.HasSynced)

	// Add handler for A
	filteredInformerA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	// Now start many filtered informers concurrently to test for race conditions
	var wg sync.WaitGroup
	eventCounts := make([]int32, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			
			// Create filtered informer for config-b
			filtered := NewProviderConfigFilteredInformer(baseInformer, "config-b")
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
	time.Sleep(500 * time.Millisecond)

	// Check if all config-b informers received the event
	for i, count := range eventCounts {
		if count != 1 {
			t.Errorf("Filtered informer %d received %d events, expected 1", i, count)
		}
	}
}

// TestInformerWithSlowWatcher tests behavior when the watcher is slow
func TestInformerWithSlowWatcher(t *testing.T) {
	// Create a fake client with custom reactor to simulate slow API responses
	fakeClient := fake.NewSimpleClientset()
	
	// Add a watch reactor that simulates slow responses
	fakeClient.PrependWatchReactor("services", func(action clienttesting.Action) (handled bool, ret watch.Interface, err error) {
		// Simulate slow API response
		time.Sleep(100 * time.Millisecond)
		return false, nil, nil
	})

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
	}
	
	// Create service before starting informer
	ctx := context.Background()
	_, err := fakeClient.CoreV1().Services("default").Create(ctx, service, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}

	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	serviceInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start informer
	go serviceInformer.Run(stopCh)
	
	// Try to add handler while informer might still be starting
	received := int32(0)
	serviceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&received, 1)
		},
	})

	// Wait for sync
	cache.WaitForCacheSync(stopCh, serviceInformer.HasSynced)
	time.Sleep(200 * time.Millisecond)

	if atomic.LoadInt32(&received) != 1 {
		t.Errorf("Expected 1 event, got %d", received)
	}
}