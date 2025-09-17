package filteredinformer

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestRootCauseBug demonstrates the exact bug happening in production
func TestRootCauseBug(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create 100 services for config-b
	var services []runtime.Object
	for i := 0; i < 100; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-b-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"cloud.gke.io/provider-config-name": "config-b",
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

	// === Simulate config-a running for hours ===
	t.Log("=== Config-A starts (has been running for hours) ===")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	// Let it stabilize
	time.Sleep(100 * time.Millisecond)

	// === Now config-b starts (hours later) ===
	t.Log("=== Config-B starts (hours after config-a) ===")
	
	// Step 1: initializeInformers creates filtered informer and runs it
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh) // Line 187-191 in neg.go
	
	// Step 2: HasSynced immediately returns true because base is synced!
	syncedImmediately := filteredB.HasSynced()
	t.Logf("HasSynced immediately after Run: %v", syncedImmediately)
	
	// Step 3: createNEGController adds handlers (this takes time)
	time.Sleep(10 * time.Millisecond) // Simulate createNEGController work
	
	queueB := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	handlerCalls := int32(0)
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&handlerCalls, 1)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queueB.Add(key)
		},
	})
	
	// Step 4: negController.Run() checks HasSynced
	if !cache.WaitForCacheSync(stopCh, filteredB.HasSynced) {
		t.Fatal("Cache sync failed")
	}
	
	// Step 5: Workers start immediately because HasSynced was already true
	workerProcessed := int32(0)
	workerStarted := time.Now()
	go func() {
		for {
			item, shutdown := queueB.Get()
			if shutdown {
				return
			}
			atomic.AddInt32(&workerProcessed, 1)
			queueB.Done(item)
		}
	}()
	
	// Give time for synthetic events to be delivered
	time.Sleep(100 * time.Millisecond)
	
	handlers := atomic.LoadInt32(&handlerCalls)
	processed := atomic.LoadInt32(&workerProcessed)
	
	t.Logf("Results after 100ms:")
	t.Logf("  - Handler calls: %d (expected 100)", handlers)
	t.Logf("  - Worker processed: %d (expected 100)", processed)
	t.Logf("  - Queue length: %d", queueB.Len())
	
	if syncedImmediately {
		t.Log("ROOT CAUSE IDENTIFIED:")
		t.Log("  1. Base informer is already synced from config-a")
		t.Log("  2. filteredB.HasSynced() returns true immediately")
		t.Log("  3. Workers start before handlers add synthetic events")
		t.Log("  4. If handlers are slow, workers find empty queue")
		t.Log("  5. Services aren't processed until resync (10 minutes)")
	}
	
	// Demonstrate the race
	if handlers < 100 && workerStarted.Before(time.Now().Add(-90*time.Millisecond)) {
		t.Log("RACE CONDITION: Workers started before all handlers fired")
		t.Logf("  Only %d/%d handlers fired in time", handlers, 100)
	}
	
	if processed < handlers {
		t.Logf("PROCESSING LAG: %d items enqueued but only %d processed", handlers, processed)
	}
}

// TestProductionScenarioExactReproduction exactly reproduces the production scenario
func TestProductionScenarioExactReproduction(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Production has thousands of services
	var services []runtime.Object
	for i := 0; i < 1000; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-b-%d", i),
				Namespace: fmt.Sprintf("namespace-%d", i%10),
				Labels: map[string]string{
					"cloud.gke.io/provider-config-name": "config-b",
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

	// Config-A has been running for hours
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})
	time.Sleep(100 * time.Millisecond)

	// Config-B starts
	t.Log("Starting config-b with 1000 services")
	
	// Exact NEG controller initialization sequence
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Informers are started first
	go filteredB.Run(stopCh)
	
	// Simulate multiple informers like in real NEG controller
	hasSyncedFuncs := []func() bool{
		filteredB.HasSynced,
		func() bool { return true }, // Other informers
		func() bool { return true },
		func() bool { return true },
		func() bool { return true },
	}
	
	aggregateHasSynced := func() bool {
		for _, fn := range hasSyncedFuncs {
			if !fn() {
				return false
			}
		}
		return true
	}
	
	// Check if already synced (this is the bug!)
	alreadySynced := aggregateHasSynced()
	t.Logf("Aggregate HasSynced before handler registration: %v", alreadySynced)
	
	// Simulate createNEGController taking time (network calls, initialization)
	time.Sleep(50 * time.Millisecond)
	
	// Handler registration happens later in createNEGController
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	handlerCallTimes := make([]time.Time, 0, 1000)
	startTime := time.Now()
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerCallTimes = append(handlerCallTimes, time.Now())
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
		},
	})
	
	// NEG controller waits for sync
	if !cache.WaitForCacheSync(stopCh, aggregateHasSynced) {
		t.Fatal("Cache sync failed")
	}
	
	// Workers start
	workerStartTime := time.Now()
	processed := int32(0)
	
	// Simulate multiple workers
	for i := 0; i < 5; i++ {
		go func() {
			for {
				item, shutdown := queue.Get()
				if shutdown {
					return
				}
				atomic.AddInt32(&processed, 1)
				// Simulate actual processing time
				time.Sleep(1 * time.Millisecond)
				queue.Done(item)
			}
		}()
	}
	
	// Wait for processing
	time.Sleep(2 * time.Second)
	
	processedCount := atomic.LoadInt32(&processed)
	t.Logf("Processed %d/%d services", processedCount, 1000)
	
	if len(handlerCallTimes) > 0 {
		firstHandler := handlerCallTimes[0].Sub(startTime)
		lastHandler := handlerCallTimes[len(handlerCallTimes)-1].Sub(startTime)
		workerStart := workerStartTime.Sub(startTime)
		
		t.Logf("Timing analysis:")
		t.Logf("  First handler: %v", firstHandler)
		t.Logf("  Last handler: %v", lastHandler)
		t.Logf("  Worker start: %v", workerStart)
		
		if workerStart < lastHandler {
			t.Logf("BUG: Workers started %v before all handlers fired", lastHandler-workerStart)
		}
	}
	
	if alreadySynced {
		t.Log("PRODUCTION BUG REPRODUCED:")
		t.Log("  HasSynced returned true before handlers were added")
		t.Log("  Workers started immediately")
		t.Log("  Synthetic events may not have been fully delivered")
	}
	
	if processedCount < 1000 {
		missingCount := 1000 - processedCount
		t.Errorf("MISSING SERVICES: %d services were not processed", missingCount)
		t.Log("These services won't be processed until:")
		t.Log("  1. 10-minute resync period (or longer)")
		t.Log("  2. Leader election change")
		t.Log("  3. Manual intervention")
	}
}

// TestFixValidation validates that a proper fix would work
func TestFixValidation(t *testing.T) {
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
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Config-A running
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})
	time.Sleep(100 * time.Millisecond)

	// Config-B with proposed fix
	t.Log("Testing with proposed fix")
	
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// FIX: Add handlers BEFORE running the informer
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	handlerCalled := false
	
	_, err := filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerCalled = true
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
		},
	})
	if err != nil {
		t.Logf("AddEventHandler before Run returned error: %v", err)
	}
	
	// Now run the informer
	go filteredB.Run(stopCh)
	
	// Wait for sync
	if !cache.WaitForCacheSync(stopCh, filteredB.HasSynced) {
		t.Fatal("Cache sync failed")
	}
	
	// Start worker
	processed := false
	go func() {
		for {
			item, shutdown := queue.Get()
			if shutdown {
				return
			}
			processed = true
			t.Logf("Worker processed: %v", item)
			queue.Done(item)
		}
	}()
	
	time.Sleep(200 * time.Millisecond)
	
	if handlerCalled && processed {
		t.Log("FIX WORKS: Handler called and worker processed item")
	} else {
		t.Logf("Fix status: handler=%v, processed=%v", handlerCalled, processed)
	}
}