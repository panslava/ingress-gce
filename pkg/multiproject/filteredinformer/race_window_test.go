package filteredinformer

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestRaceWindowInProduction demonstrates the exact race condition
func TestRaceWindowInProduction(t *testing.T) {
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

	// === This is the exact sequence in production ===
	t.Log("=== Starting config-b (exact production sequence) ===")
	
	// 1. initializeInformers creates and runs informers
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// 2. Create work queue (happens in createNEGController)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	
	// 3. Check HasSynced (aggregate function in production)
	hasSynced := func() bool {
		return filteredB.HasSynced() // Returns true immediately!
	}
	
	// 4. Controller waits for sync
	syncedImmediately := hasSynced()
	t.Logf("HasSynced before adding handlers: %v", syncedImmediately)
	
	// 5. Workers start because HasSynced is true
	workerStarted := false
	itemsProcessed := int32(0)
	var workerMutex sync.Mutex
	
	if syncedImmediately {
		workerStarted = true
		go func() {
			t.Log("Worker: Started BEFORE handlers added!")
			for {
				// Try to get from queue
				item, shutdown := queue.Get()
				if shutdown {
					return
				}
				atomic.AddInt32(&itemsProcessed, 1)
				t.Logf("Worker: Processing %v", item)
				queue.Done(item)
			}
		}()
	}
	
	// 6. Simulate delay in createNEGController (network calls, initialization)
	t.Log("Simulating createNEGController delay...")
	time.Sleep(50 * time.Millisecond)
	
	// 7. NOW handlers are added (too late!)
	handlerCalls := int32(0)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			count := atomic.AddInt32(&handlerCalls, 1)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
			t.Logf("Handler %d: Enqueued %s", count, key)
		},
	})
	
	// 8. If worker wasn't started yet, start it now
	workerMutex.Lock()
	if !workerStarted {
		t.Log("Worker: Starting after handlers (this shouldn't happen)")
		go func() {
			for {
				item, shutdown := queue.Get()
				if shutdown {
					return
				}
				atomic.AddInt32(&itemsProcessed, 1)
				queue.Done(item)
			}
		}()
	}
	workerMutex.Unlock()
	
	// Wait for processing
	time.Sleep(200 * time.Millisecond)
	
	handlers := atomic.LoadInt32(&handlerCalls)
	processed := atomic.LoadInt32(&itemsProcessed)
	
	t.Log("=== Race Condition Analysis ===")
	t.Logf("HasSynced was immediate: %v", syncedImmediately)
	t.Logf("Worker started before handlers: %v", workerStarted)
	t.Logf("Handler calls: %d", handlers)
	t.Logf("Items processed: %d", processed)
	t.Logf("Queue length: %d", queue.Len())
	
	if syncedImmediately && workerStarted {
		t.Log("RACE CONDITION CONFIRMED:")
		t.Log("  1. HasSynced returned true immediately")
		t.Log("  2. Worker started with empty queue")
		t.Log("  3. Handlers added later")
		t.Log("  4. Synthetic Add events delivered to handlers")
		t.Log("  5. Items enqueued AFTER worker already polling empty queue")
		
		if processed == 0 && handlers > 0 {
			t.Error("BUG: Handlers fired but worker didn't process!")
			t.Log("In production, worker might have given up or be in a bad state")
		}
	}
	
	// Check if resync would help
	t.Log("\n=== Would resync help? ===")
	t.Log("Resync sends Update events to existing handlers")
	t.Log("Since handlers ARE registered (just late), resync SHOULD work")
	t.Log("But in production, the 4+ hour delay suggests resync isn't helping")
	t.Log("Possible reasons:")
	t.Log("  1. Worker goroutine might be stuck or terminated")
	t.Log("  2. Queue might have a bug under certain conditions")
	t.Log("  3. There might be another race we haven't identified")
}

// TestWorkerQueueBehavior tests if the worker queue behaves correctly
func TestWorkerQueueBehavior(t *testing.T) {
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	
	// Start worker before adding items
	processed := int32(0)
	done := make(chan struct{})
	
	go func() {
		t.Log("Worker: Started with empty queue")
		startTime := time.Now()
		for {
			item, shutdown := queue.Get()
			if shutdown {
				close(done)
				return
			}
			elapsed := time.Since(startTime)
			atomic.AddInt32(&processed, 1)
			t.Logf("Worker: Got item %v after %v", item, elapsed)
			queue.Done(item)
			
			if atomic.LoadInt32(&processed) >= 3 {
				queue.ShutDown()
			}
		}
	}()
	
	// Add items after worker starts
	time.Sleep(100 * time.Millisecond)
	t.Log("Adding items to queue...")
	queue.Add("item1")
	
	time.Sleep(50 * time.Millisecond)
	queue.Add("item2")
	
	time.Sleep(50 * time.Millisecond)
	queue.Add("item3")
	
	// Wait for completion
	<-done
	
	finalCount := atomic.LoadInt32(&processed)
	t.Logf("Processed %d items", finalCount)
	
	if finalCount != 3 {
		t.Errorf("Expected 3 items, processed %d", finalCount)
	} else {
		t.Log("Queue works correctly even when worker starts first")
	}
}