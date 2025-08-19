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

// TestActualBugScenario tests if the race causes actual processing failure
func TestActualBugScenario(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Just 10 services - not a massive cluster
	var services []runtime.Object
	for i := 0; i < 10; i++ {
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

	// Config-A running for hours
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	time.Sleep(100 * time.Millisecond)

	// Config-B starts - exact NEG controller pattern
	t.Log("Starting config-b")
	
	// This mimics initializeInformers in neg.go
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// This mimics the aggregate hasSynced function
	hasSynced := func() bool {
		return filteredB.HasSynced()
	}
	
	// This happens in negController.Run()
	if !cache.WaitForCacheSync(stopCh, hasSynced) {
		t.Fatal("Failed to sync")
	}
	t.Log("Cache synced, starting worker")
	
	// Create queue and start worker IMMEDIATELY
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	lister := filteredB.GetStore()
	processed := int32(0)
	
	// Worker starts immediately because HasSynced was true
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for i := 0; i < 100; i++ { // Try 100 times then give up
			key, quit := queue.Get()
			if quit {
				return
			}
			
			// Simulate processService
			obj, exists, err := lister.GetByKey(key.(string))
			if err != nil {
				t.Logf("Worker error: %v", err)
			} else if !exists {
				t.Logf("Worker: %s doesn't exist", key)
			} else {
				atomic.AddInt32(&processed, 1)
				t.Logf("Worker: Processed %s", key)
				_ = obj
			}
			queue.Done(key)
		}
		t.Log("Worker: Giving up after 100 attempts")
	}()
	
	// NOW add handlers (delayed like in createNEGController)
	time.Sleep(50 * time.Millisecond)
	
	handlerCalls := int32(0)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&handlerCalls, 1)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
		},
		UpdateFunc: func(old, new interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(new)
			queue.Add(key)
		},
	})
	
	// Wait a bit
	time.Sleep(500 * time.Millisecond)
	
	handlers := atomic.LoadInt32(&handlerCalls)
	processedCount := atomic.LoadInt32(&processed)
	
	t.Logf("Results: handlers=%d, processed=%d", handlers, processedCount)
	
	if handlers > 0 && processedCount == 0 {
		t.Error("BUG CONFIRMED: Handlers fired but nothing processed!")
		t.Log("This would cause the 4+ hour delay")
	} else if processedCount < 10 {
		t.Errorf("Only processed %d/10 services", processedCount)
	}
	
	// Now test if resync helps
	t.Log("\n=== Testing if resync helps ===")
	
	// Manually trigger a resync-like Update event
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("default/service-b-%d", i)
		obj, exists, _ := lister.GetByKey(key)
		if exists && obj != nil {
			// Simulate resync sending Update
			queue.Add(key)
		}
	}
	
	time.Sleep(500 * time.Millisecond)
	finalProcessed := atomic.LoadInt32(&processed)
	
	if finalProcessed > processedCount {
		t.Logf("Resync helped: processed increased from %d to %d", processedCount, finalProcessed)
	} else {
		t.Log("Resync didn't help - queue might be stuck")
	}
}

// TestWhatActuallyHappens tests what's really happening
func TestWhatActuallyHappens(t *testing.T) {
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

	// Config-B with detailed logging
	t.Log("=== Starting config-b with detailed tracking ===")
	
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Check state BEFORE Run
	beforeRun := filteredB.HasSynced()
	t.Logf("HasSynced before Run: %v", beforeRun)
	
	go filteredB.Run(stopCh)
	
	// Check state AFTER Run
	afterRun := filteredB.HasSynced()
	t.Logf("HasSynced after Run: %v", afterRun)
	
	// Check cache state
	lister := filteredB.GetStore()
	items := lister.List()
	t.Logf("Items in cache: %d", len(items))
	
	key := "default/service-b"
	obj, exists, err := lister.GetByKey(key)
	t.Logf("GetByKey(%s): exists=%v, obj=%v, err=%v", key, exists, obj != nil, err)
	
	// Now add handler
	eventLog := []string{}
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			msg := fmt.Sprintf("Add: %s/%s", svc.Namespace, svc.Name)
			eventLog = append(eventLog, msg)
			t.Log(msg)
		},
		UpdateFunc: func(old, new interface{}) {
			svc := new.(*v1.Service)
			msg := fmt.Sprintf("Update: %s/%s", svc.Namespace, svc.Name)
			eventLog = append(eventLog, msg)
			t.Log(msg)
		},
	})
	
	// Wait for events
	time.Sleep(200 * time.Millisecond)
	
	// Check cache again
	obj2, exists2, err2 := lister.GetByKey(key)
	t.Logf("GetByKey after handler: exists=%v, obj=%v, err=%v", exists2, obj2 != nil, err2)
	
	t.Logf("Total events: %d", len(eventLog))
	for i, event := range eventLog {
		t.Logf("  Event %d: %s", i+1, event)
	}
	
	// The key question
	t.Log("\n=== Key Question ===")
	if afterRun && len(eventLog) > 0 {
		t.Log("HasSynced=true AND events fired - so why 4+ hours?")
		t.Log("Possible explanations:")
		t.Log("1. In production, handler registration might be MUCH slower")
		t.Log("2. There might be a different race we haven't found")
		t.Log("3. The issue might be in a different component")
	}
}

// TestSlowHandlerRegistration simulates slow handler registration
func TestSlowHandlerRegistration(t *testing.T) {
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

	// Config-A
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	time.Sleep(100 * time.Millisecond)

	// Config-B with VERY slow handler registration
	t.Log("Starting config-b with slow handler registration")
	
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// Wait for sync
	if !cache.WaitForCacheSync(stopCh, filteredB.HasSynced) {
		t.Fatal("Failed to sync")
	}
	
	// Start worker immediately
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	workerStarted := time.Now()
	processed := int32(0)
	
	go func() {
		t.Log("Worker started")
		timeout := time.After(2 * time.Second)
		for {
			select {
			case <-timeout:
				t.Log("Worker timed out waiting for items")
				return
			default:
				// Non-blocking check
				if queue.Len() > 0 {
					key, _ := queue.Get()
					atomic.AddInt32(&processed, 1)
					t.Logf("Worker processed: %v", key)
					queue.Done(key)
				} else {
					time.Sleep(10 * time.Millisecond)
				}
			}
		}
	}()
	
	// Simulate VERY slow handler registration (like in production)
	t.Log("Simulating slow handler registration (500ms)...")
	time.Sleep(500 * time.Millisecond)
	
	handlerAdded := time.Now()
	delay := handlerAdded.Sub(workerStarted)
	t.Logf("Handler added after %v delay", delay)
	
	handlerCalled := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerCalled = true
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
			t.Logf("Handler enqueued: %s", key)
		},
	})
	
	// Wait for processing
	time.Sleep(1 * time.Second)
	
	finalProcessed := atomic.LoadInt32(&processed)
	
	t.Logf("Handler called: %v", handlerCalled)
	t.Logf("Items processed: %d", finalProcessed)
	
	if handlerCalled && finalProcessed == 0 {
		t.Error("BUG: Handler called but worker didn't process!")
		t.Logf("Worker started %v before handler", delay)
		t.Log("Worker might have timed out or be in wrong state")
	}
}