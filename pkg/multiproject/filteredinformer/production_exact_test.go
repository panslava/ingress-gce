package filteredinformer

import (
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

// TestProductionExactScenario simulates the exact production scenario
func TestProductionExactScenario(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// In production, services might be created with labels already
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "production-service",
			Namespace: "production",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
				"app": "myapp",
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeClusterIP,
			Ports: []v1.ServicePort{
				{Port: 80},
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceB)
	
	// Production uses 10-minute resync
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	
	// Multiple informers in production
	serviceInformer := informerFactory.Core().V1().Services().Informer()
	podInformer := informerFactory.Core().V1().Pods().Informer()
	ingressInformer := informerFactory.Networking().V1().Ingresses().Informer()
	
	stopCh := make(chan struct{})
	defer close(stopCh)

	// === Config-A starts first (been running for hours) ===
	t.Log("=== Config-A starts (simulating hours of runtime) ===")
	
	// Wrap all informers for config-a
	filteredServiceA := NewProviderConfigFilteredInformer(serviceInformer, "config-a")
	filteredPodA := NewProviderConfigFilteredInformer(podInformer, "config-a")
	filteredIngressA := NewProviderConfigFilteredInformer(ingressInformer, "config-a")
	
	// Start all informers
	go filteredServiceA.Run(stopCh)
	go filteredPodA.Run(stopCh)
	go filteredIngressA.Run(stopCh)
	
	// Wait for sync
	cache.WaitForCacheSync(stopCh, 
		filteredServiceA.HasSynced,
		filteredPodA.HasSynced,
		filteredIngressA.HasSynced)
	
	// Add handlers
	filteredServiceA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})
	
	// Simulate hours of operation
	time.Sleep(200 * time.Millisecond)
	
	// === Config-B starts (hours later) ===
	t.Log("=== Config-B starts (hours after config-a) ===")
	
	// Exact initialization order from neg.go
	
	// 1. initializeInformers creates and runs informers
	filteredServiceB := NewProviderConfigFilteredInformer(serviceInformer, "config-b")
	filteredPodB := NewProviderConfigFilteredInformer(podInformer, "config-b")
	filteredIngressB := NewProviderConfigFilteredInformer(ingressInformer, "config-b")
	
	// Start informers (neg.go lines 187-191)
	go filteredServiceB.Run(stopCh)
	go filteredPodB.Run(stopCh)
	go filteredIngressB.Run(stopCh)
	
	// Create aggregate hasSynced
	hasSyncedList := []func() bool{
		filteredServiceB.HasSynced,
		filteredPodB.HasSynced,
		filteredIngressB.HasSynced,
	}
	aggregateHasSynced := func() bool {
		for _, fn := range hasSyncedList {
			if !fn() {
				return false
			}
		}
		return true
	}
	
	// Check if already synced
	immediateSyncStatus := aggregateHasSynced()
	t.Logf("Aggregate HasSynced immediately: %v", immediateSyncStatus)
	
	// 2. createNEGController adds handlers and creates controller
	// Simulate time taken to create NEG controller
	time.Sleep(20 * time.Millisecond)
	
	// Create work queue
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	
	// Get lister (this is what NEG controller uses)
	serviceLister := filteredServiceB.GetStore()
	
	// Add handler
	handlerCalls := int32(0)
	filteredServiceB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&handlerCalls, 1)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
			t.Logf("Handler: Add event for %s", key)
		},
		UpdateFunc: func(old, new interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(new)
			queue.Add(key)
			t.Logf("Handler: Update event for %s", key)
		},
	})
	
	// 3. negController.Run() waits for sync then starts workers
	syncStart := time.Now()
	if !cache.WaitForCacheSync(stopCh, aggregateHasSynced) {
		t.Fatal("Cache sync failed")
	}
	syncDuration := time.Since(syncStart)
	t.Logf("WaitForCacheSync took: %v", syncDuration)
	
	// Start workers
	processed := int32(0)
	workerErrors := int32(0)
	
	// Simulate NEG controller worker
	go func() {
		t.Log("Worker: Started")
		for {
			item, shutdown := queue.Get()
			if shutdown {
				return
			}
			defer queue.Done(item)
			
			key := item.(string)
			
			// This is what NEG controller does
			obj, exists, err := serviceLister.GetByKey(key)
			if err != nil {
				atomic.AddInt32(&workerErrors, 1)
				t.Logf("Worker: Error getting %s: %v", key, err)
				continue
			}
			if !exists {
				atomic.AddInt32(&workerErrors, 1)
				t.Logf("Worker: Service %s doesn't exist in lister", key)
				t.Log("Worker: Would call StopSyncer here")
				continue
			}
			
			service := obj.(*v1.Service)
			atomic.AddInt32(&processed, 1)
			t.Logf("Worker: Processing service %s/%s", service.Namespace, service.Name)
		}
	}()
	
	// Wait for processing
	time.Sleep(500 * time.Millisecond)
	
	// Check results
	handlers := atomic.LoadInt32(&handlerCalls)
	processedCount := atomic.LoadInt32(&processed)
	errors := atomic.LoadInt32(&workerErrors)
	
	t.Log("=== Results ===")
	t.Logf("Immediate HasSynced: %v", immediateSyncStatus)
	t.Logf("Handler calls: %d", handlers)
	t.Logf("Services processed: %d", processedCount)
	t.Logf("Worker errors: %d", errors)
	t.Logf("Queue length: %d", queue.Len())
	
	// Check lister directly
	key := "production/production-service"
	obj, exists, err := serviceLister.GetByKey(key)
	t.Logf("Direct lister check: key=%s, exists=%v, obj=%v, err=%v", key, exists, obj != nil, err)
	
	if immediateSyncStatus && syncDuration < 10*time.Millisecond {
		t.Log("BUG CONDITION: HasSynced was immediate, workers started before handlers")
	}
	
	if handlers > 0 && processedCount == 0 {
		t.Error("Handler fired but worker didn't process - check lister")
	}
	
	if errors > 0 {
		t.Errorf("Worker had %d errors - services not found in lister", errors)
	}
}

// TestResyncAfterHours tests what happens with resync after hours
func TestResyncAfterHours(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Service exists before config-b starts
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-service",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
			ResourceVersion: "1000", // High version, been around a while
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceB)
	
	// Use short resync for testing
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 2*time.Second)
	baseInformer := informerFactory.Core().V1().Services().Informer()
	
	stopCh := make(chan struct{})
	defer close(stopCh)

	// Config-A running for "hours"
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
		UpdateFunc: func(old, new interface{}) {},
	})
	
	// Wait for multiple resyncs
	time.Sleep(5 * time.Second)
	
	// Now config-b starts
	t.Log("Starting config-b after hours")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// Track events over time
	eventTimes := []time.Time{}
	eventTypes := []string{}
	startTime := time.Now()
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eventTimes = append(eventTimes, time.Now())
			eventTypes = append(eventTypes, "Add")
			elapsed := time.Since(startTime)
			t.Logf("Add event at %v", elapsed)
		},
		UpdateFunc: func(old, new interface{}) {
			eventTimes = append(eventTimes, time.Now())
			eventTypes = append(eventTypes, "Update")
			elapsed := time.Since(startTime)
			t.Logf("Update event at %v", elapsed)
		},
	})
	
	// Wait for multiple resync periods
	t.Log("Waiting for resync events...")
	time.Sleep(6 * time.Second)
	
	t.Logf("Total events: %d", len(eventTimes))
	for i, eventType := range eventTypes {
		elapsed := eventTimes[i].Sub(startTime)
		t.Logf("  Event %d: %s at %v", i+1, eventType, elapsed)
	}
	
	if len(eventTimes) == 0 {
		t.Error("No events received even after multiple resync periods")
		t.Log("This would cause 4+ hour delays in production")
	} else if eventTypes[0] != "Add" {
		t.Log("First event was not Add - might cause initialization issues")
	}
	
	// Check intervals between events
	if len(eventTimes) > 1 {
		for i := 1; i < len(eventTimes); i++ {
			interval := eventTimes[i].Sub(eventTimes[i-1])
			t.Logf("Interval between event %d and %d: %v", i, i+1, interval)
		}
	}
}