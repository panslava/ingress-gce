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
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/ingress-gce/pkg/flags"
	"k8s.io/klog/v2"
)

// TestNEGControllerSimulation simulates the exact NEG controller initialization flow
func TestNEGControllerSimulation(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create services for both configs
	var services []runtime.Object
	for i := 0; i < 10; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-a-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"cloud.gke.io/provider-config-name": "config-a",
				},
			},
		}
		services = append(services, service)
	}
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

	// Simulate config-a initialization (running for hours)
	t.Log("=== Starting config-a NEG controller ===")
	queueA := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	
	// Start informer BEFORE adding handler (like in neg.go line 187-191)
	go filteredA.Run(stopCh)
	
	// Wait for sync
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	
	// Then add handler (like createNEGController does)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queueA.Add(key)
		},
	})

	// Simulate worker
	workerACount := int32(0)
	go func() {
		for {
			item, shutdown := queueA.Get()
			if shutdown {
				return
			}
			atomic.AddInt32(&workerACount, 1)
			queueA.Done(item)
		}
	}()

	// Let it run for a bit
	time.Sleep(200 * time.Millisecond)
	countA := atomic.LoadInt32(&workerACount)
	t.Logf("Config-A processed %d items", countA)

	// Now simulate config-b starting hours later
	t.Log("=== Starting config-b NEG controller (hours later) ===")
	queueB := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Track timing
	informerStartTime := time.Now()
	var hasSyncedTime, firstHandlerCallTime, firstWorkerProcessTime time.Time
	var hasSyncedOnce, handlerOnce, workerOnce sync.Once
	
	// Start informer BEFORE adding handler (like in neg.go)
	go filteredB.Run(stopCh)
	
	// Create hasSynced function that tracks when it returns true
	hasSyncedB := func() bool {
		synced := filteredB.HasSynced()
		if synced {
			hasSyncedOnce.Do(func() {
				hasSyncedTime = time.Now()
				t.Logf("HasSynced returned true after %v", hasSyncedTime.Sub(informerStartTime))
			})
		}
		return synced
	}
	
	// Wait for sync (like neg.go does via the hasSynced aggregate function)
	cache.WaitForCacheSync(stopCh, hasSyncedB)
	
	// Add handler AFTER waiting for sync (but handlers get added in createNEGController)
	handlerAdded := time.Now()
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerOnce.Do(func() {
				firstHandlerCallTime = time.Now()
				t.Logf("First handler called after %v from informer start", firstHandlerCallTime.Sub(informerStartTime))
				t.Logf("First handler called after %v from handler added", firstHandlerCallTime.Sub(handlerAdded))
			})
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queueB.Add(key)
		},
	})

	// Start worker AFTER HasSynced (like neg.Controller.Run does)
	workerBCount := int32(0)
	workerStarted := time.Now()
	go func() {
		t.Logf("Worker started after %v from informer start", workerStarted.Sub(informerStartTime))
		for {
			item, shutdown := queueB.Get()
			if shutdown {
				return
			}
			workerOnce.Do(func() {
				firstWorkerProcessTime = time.Now()
				t.Logf("First item processed after %v from worker start", firstWorkerProcessTime.Sub(workerStarted))
			})
			atomic.AddInt32(&workerBCount, 1)
			queueB.Done(item)
		}
	}()

	// Wait to see if items get processed
	time.Sleep(500 * time.Millisecond)
	
	countB := atomic.LoadInt32(&workerBCount)
	t.Logf("Config-B processed %d items out of 10 expected", countB)
	
	// Check timing
	if !hasSyncedTime.IsZero() && !firstHandlerCallTime.IsZero() {
		if hasSyncedTime.Before(firstHandlerCallTime) {
			t.Logf("WARNING: HasSynced returned true %v BEFORE first handler call", firstHandlerCallTime.Sub(hasSyncedTime))
			t.Log("This means workers could start before handlers enqueue items!")
		}
	}
	
	if countB == 0 {
		t.Error("BUG REPRODUCED: Config-B didn't process ANY services")
		t.Log("Even though there are 10 services with the correct label")
		
		// Check if items are in queue
		queueLen := queueB.Len()
		t.Logf("Queue length: %d", queueLen)
		
		if queueLen == 0 {
			t.Log("Queue is empty - handlers never enqueued items")
			if firstHandlerCallTime.IsZero() {
				t.Log("Handler was never called with synthetic events!")
			}
		}
	} else if countB < 10 {
		t.Errorf("Config-B only processed %d services out of 10", countB)
	}
}

// TestNEGControllerWithSlowHandlerInit simulates slow handler initialization
func TestNEGControllerWithSlowHandlerInit(t *testing.T) {
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

	// Start config-a
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	time.Sleep(100 * time.Millisecond)

	// Start config-b with exact NEG controller pattern
	t.Log("Starting config-b with NEG controller pattern")
	
	// 1. Create filtered informer
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// 2. Run informer
	go filteredB.Run(stopCh)
	
	// 3. Create aggregate hasSynced (like in neg.go initializeInformers)
	hasSyncedList := []func() bool{
		filteredB.HasSynced,
		// In real code there are multiple informers
	}
	hasSynced := func() bool {
		for _, hs := range hasSyncedList {
			if !hs() {
				return false
			}
		}
		return true
	}
	
	// 4. Simulate createNEGController being slow
	time.Sleep(50 * time.Millisecond)
	
	// 5. Add handler (happens in createNEGController via neg.NewController)
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	handlerCalled := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerCalled = true
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
			t.Logf("Handler enqueued: %s", key)
		},
	})
	
	// 6. Now wait for sync (like negController.Run does)
	synced := cache.WaitForCacheSync(stopCh, hasSynced)
	if !synced {
		t.Fatal("Cache never synced")
	}
	
	// 7. Start workers
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
	
	if !handlerCalled {
		t.Error("Handler never called - synthetic events not delivered")
	}
	if !processed {
		t.Error("Worker never processed item")
		t.Logf("Queue length: %d", queue.Len())
	}
}

// TestRaceConditionInNEGInit tests for race conditions in NEG initialization
func TestRaceConditionInNEGInit(t *testing.T) {
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

	// Start config-a
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	time.Sleep(100 * time.Millisecond)

	// Test what happens if HasSynced is checked before Run
	t.Log("Testing race condition: HasSynced before Run")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Check HasSynced BEFORE running (this might happen in parallel execution)
	earlySync := filteredB.HasSynced()
	t.Logf("HasSynced before Run: %v", earlySync)
	
	if earlySync {
		t.Log("HasSynced returned true before Run() was called!")
		t.Log("This could cause workers to start immediately")
	}
	
	// Now run it
	go filteredB.Run(stopCh)
	
	// Add handler
	received := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
		},
	})
	
	time.Sleep(200 * time.Millisecond)
	
	if earlySync && !received {
		t.Error("HasSynced was true early but handler didn't receive event")
		t.Log("This is the race condition - workers start but no events queued")
	}
}

// MockNEGController simulates the NEG controller structure
type MockNEGController struct {
	queue      workqueue.RateLimitingInterface
	lister     cache.Store
	hasSynced  func() bool
	logger     klog.Logger
	processed  int32
}

func (c *MockNEGController) Run(stopCh <-chan struct{}) {
	// This mimics neg.Controller.Run()
	c.logger.Info("Waiting for initial sync")
	if !cache.WaitForCacheSync(stopCh, c.hasSynced) {
		c.logger.Error(nil, "Timed out waiting for caches to sync")
		return
	}
	
	c.logger.Info("Starting workers")
	go c.worker()
	<-stopCh
}

func (c *MockNEGController) worker() {
	for c.processNextWorkItem() {
	}
}

func (c *MockNEGController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	
	// Simulate processing like neg.Controller.processService
	obj, exists, err := c.lister.GetByKey(key.(string))
	if err != nil {
		c.logger.Error(err, "Error getting object from store", "key", key)
		return true
	}
	
	if !exists {
		c.logger.Info("Service doesn't exist, stopping syncer", "key", key)
		return true
	}
	
	atomic.AddInt32(&c.processed, 1)
	c.logger.Info("Processed service", "key", key, "service", obj)
	return true
}

// TestFullNEGControllerFlow tests the complete NEG controller flow
func TestFullNEGControllerFlow(t *testing.T) {
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

	// Start config-a with mock NEG controller
	t.Log("Starting config-a NEG controller")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	
	storeA := cache.NewStore(cache.MetaNamespaceKeyFunc)
	queueA := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			storeA.Add(obj)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queueA.Add(key)
		},
	})
	
	controllerA := &MockNEGController{
		queue:     queueA,
		lister:    storeA,
		hasSynced: filteredA.HasSynced,
		logger:    klog.FromContext(nil),
	}
	go controllerA.Run(stopCh)
	
	time.Sleep(200 * time.Millisecond)
	
	// Now start config-b
	t.Log("Starting config-b NEG controller (simulating hours later)")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Run informer first (like initializeInformers does)
	go filteredB.Run(stopCh)
	
	// Create store and queue
	storeB := cache.NewStore(cache.MetaNamespaceKeyFunc)
	queueB := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	
	// Add handler (happens in createNEGController)
	handlerCalls := int32(0)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&handlerCalls, 1)
			storeB.Add(obj)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queueB.Add(key)
			t.Logf("Handler called for: %s", key)
		},
	})
	
	// Create and run controller
	controllerB := &MockNEGController{
		queue:     queueB,
		lister:    storeB,
		hasSynced: filteredB.HasSynced,
		logger:    klog.FromContext(nil),
	}
	go controllerB.Run(stopCh)
	
	// Wait for processing
	time.Sleep(500 * time.Millisecond)
	
	handlers := atomic.LoadInt32(&handlerCalls)
	processed := atomic.LoadInt32(&controllerB.processed)
	
	t.Logf("Config-B: handlers called %d times, processed %d items", handlers, processed)
	
	if handlers == 0 {
		t.Error("BUG: Handler never called - synthetic events not delivered")
	}
	if processed == 0 {
		t.Error("BUG: Worker never processed items")
		t.Logf("Queue length: %d", queueB.Len())
		t.Logf("Store has %d items", len(storeB.List()))
	}
}