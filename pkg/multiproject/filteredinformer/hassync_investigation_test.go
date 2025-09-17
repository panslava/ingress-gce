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
	"k8s.io/ingress-gce/pkg/flags"
)

// TestHasSyncedVsHandlerProcessing tests if HasSynced can return true
// before handlers have processed synthetic events
func TestHasSyncedVsHandlerProcessing(t *testing.T) {
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

	// Now start config-b
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	// Track timing
	var hasSyncedTime, handlerProcessedTime time.Time
	handlerProcessed := make(chan struct{})

	// Add handler with slow processing
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// Simulate slow handler processing
			time.Sleep(200 * time.Millisecond)
			handlerProcessedTime = time.Now()
			close(handlerProcessed)
			t.Log("Handler finished processing")
		},
	})

	// Check HasSynced in a loop
	go func() {
		for !filteredB.HasSynced() {
			time.Sleep(1 * time.Millisecond)
		}
		hasSyncedTime = time.Now()
		t.Log("HasSynced returned true")
	}()

	// Wait for both
	select {
	case <-handlerProcessed:
	case <-time.After(2 * time.Second):
		t.Fatal("Handler didn't process in time")
	}

	if !hasSyncedTime.IsZero() && !handlerProcessedTime.IsZero() {
		if hasSyncedTime.Before(handlerProcessedTime) {
			diff := handlerProcessedTime.Sub(hasSyncedTime)
			t.Logf("HasSynced returned true %v BEFORE handler finished processing", diff)
			t.Log("This could be the issue - controller starts workers before handlers process events")
		} else {
			t.Log("Handler processed before HasSynced (expected)")
		}
	}
}

// TestWorkerStartTiming tests if workers start before handlers process events
func TestWorkerStartTiming(t *testing.T) {
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

	// Simulate config-b with work queue
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	workQueue := make(chan string, 10)
	workerStarted := int32(0)
	itemsProcessed := int32(0)

	// Simulate controller waiting for HasSynced then starting worker
	go func() {
		for !filteredB.HasSynced() {
			time.Sleep(1 * time.Millisecond)
		}
		atomic.StoreInt32(&workerStarted, 1)
		t.Log("Worker started (HasSynced=true)")

		// Process queue
		for item := range workQueue {
			atomic.AddInt32(&itemsProcessed, 1)
			t.Logf("Worker processed: %s", item)
		}
	}()

	// Add handler that enqueues
	handlerCalled := int32(0)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&handlerCalled, 1)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			t.Logf("Handler enqueuing: %s (worker started: %v)", key, atomic.LoadInt32(&workerStarted) == 1)
			workQueue <- key
		},
	})

	// Wait a bit
	time.Sleep(500 * time.Millisecond)

	if atomic.LoadInt32(&handlerCalled) == 0 {
		t.Error("Handler never called")
	}
	if atomic.LoadInt32(&itemsProcessed) == 0 {
		t.Error("Worker never processed items")
		if len(workQueue) > 0 {
			t.Errorf("Queue has %d items but worker didn't process them", len(workQueue))
		}
	}
}

// TestQueueBuffering tests if the work queue can drop events
func TestQueueBuffering(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create many services
	var services []runtime.Object
	for i := 0; i < 100; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-%d", i),
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

	// Start config-a
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	time.Sleep(100 * time.Millisecond)

	// Start config-b with small queue
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	// Small queue that might overflow
	workQueue := make(chan string, 5) // Only 5 slots for 100 items
	enqueued := int32(0)
	dropped := int32(0)

	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			select {
			case workQueue <- key:
				atomic.AddInt32(&enqueued, 1)
			default:
				// Queue full, item dropped
				atomic.AddInt32(&dropped, 1)
				t.Logf("Dropped: %s (queue full)", key)
			}
		},
	})

	// Slow worker
	go func() {
		for item := range workQueue {
			time.Sleep(10 * time.Millisecond) // Slow processing
			_ = item
		}
	}()

	time.Sleep(1 * time.Second)

	enqueuedCount := atomic.LoadInt32(&enqueued)
	droppedCount := atomic.LoadInt32(&dropped)

	t.Logf("Enqueued: %d, Dropped: %d", enqueuedCount, droppedCount)

	if droppedCount > 0 {
		t.Logf("Queue overflow detected: %d items dropped", droppedCount)
		t.Log("This could explain missing events in production")
	}
}

// TestHandlerPanic tests if a panic in one handler affects others
func TestHandlerPanic(t *testing.T) {
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

	// Start config-a with panicking handler
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)

	panicRecovered := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicRecovered = true
				t.Logf("Recovered from panic: %v", r)
			}
		}()

		filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				svc := obj.(*v1.Service)
				if svc.Name == "service-a" {
					panic("intentional panic in handler")
				}
			},
		})
	}()

	time.Sleep(100 * time.Millisecond)

	// Now start config-b - does it still work?
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	received := false
	var mu sync.Mutex
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			received = true
			mu.Unlock()
		},
	})

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	if !received {
		t.Error("Config-B didn't receive event after config-A handler panicked")
		t.Log("Panic in one handler might affect others")
	}
	mu.Unlock()

	if !panicRecovered {
		t.Log("Note: Panic was not recovered, might crash in production")
	}
}