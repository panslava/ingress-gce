package filteredinformer

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestSlowAPIServerCondition simulates slow API server responses
func TestSlowAPIServerCondition(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create services
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
	
	// Make API calls slow
	fakeClient.PrependReactor("list", "services", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		t.Log("API: List services called (simulating slow response)")
		time.Sleep(200 * time.Millisecond) // Simulate slow API
		return false, nil, nil // Let the default handler handle it
	})
	
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
	
	// Start config-b
	t.Log("Starting config-b with slow API")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	events := int32(0)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&events, 1)
		},
	})
	
	time.Sleep(1 * time.Second)
	
	eventCount := atomic.LoadInt32(&events)
	t.Logf("Events received with slow API: %d", eventCount)
	
	if eventCount == 0 {
		t.Log("Slow API might prevent events")
	}
}

// TestWatchTimeout simulates watch timeouts
func TestWatchTimeout(t *testing.T) {
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
	
	// Simulate watch timeout
	watchCount := int32(0)
	fakeClient.PrependWatchReactor("services", func(action k8stesting.Action) (handled bool, ret watch.Interface, err error) {
		count := atomic.AddInt32(&watchCount, 1)
		t.Logf("Watch #%d started", count)
		
		if count > 1 {
			// Second watch (after config-a) might have issues
			t.Log("Second watch might be problematic")
		}
		
		return false, nil, nil // Let default handle it
	})
	
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
	
	// Start config-b
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	received := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
		},
	})
	
	time.Sleep(500 * time.Millisecond)
	
	watches := atomic.LoadInt32(&watchCount)
	t.Logf("Total watches: %d", watches)
	t.Logf("Event received: %v", received)
}

// TestLeaderElectionScenario simulates leader election changes
func TestLeaderElectionScenario(t *testing.T) {
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
	
	// First leader election term
	t.Log("=== First leader election term ===")
	informerFactory1 := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer1 := informerFactory1.Core().V1().Services().Informer()
	
	stopCh1 := make(chan struct{})
	
	filteredA1 := NewProviderConfigFilteredInformer(baseInformer1, "config-a")
	go filteredA1.Run(stopCh1)
	cache.WaitForCacheSync(stopCh1, filteredA1.HasSynced)
	filteredA1.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})
	
	time.Sleep(100 * time.Millisecond)
	
	// Config-b starts but doesn't process
	filteredB1 := NewProviderConfigFilteredInformer(baseInformer1, "config-b")
	go filteredB1.Run(stopCh1)
	
	events1 := int32(0)
	filteredB1.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&events1, 1)
		},
	})
	
	time.Sleep(200 * time.Millisecond)
	
	count1 := atomic.LoadInt32(&events1)
	t.Logf("Events in first term: %d", count1)
	
	// Simulate leader election change
	t.Log("=== Leader election change ===")
	close(stopCh1) // Stop everything
	
	time.Sleep(100 * time.Millisecond)
	
	// New leader election term
	t.Log("=== Second leader election term ===")
	informerFactory2 := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer2 := informerFactory2.Core().V1().Services().Informer()
	
	stopCh2 := make(chan struct{})
	defer close(stopCh2)
	
	// Both configs restart
	filteredA2 := NewProviderConfigFilteredInformer(baseInformer2, "config-a")
	go filteredA2.Run(stopCh2)
	
	filteredB2 := NewProviderConfigFilteredInformer(baseInformer2, "config-b")
	go filteredB2.Run(stopCh2)
	
	events2 := int32(0)
	filteredB2.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&events2, 1)
		},
	})
	
	time.Sleep(200 * time.Millisecond)
	
	count2 := atomic.LoadInt32(&events2)
	t.Logf("Events after leader change: %d", count2)
	
	if count1 == 0 && count2 > 0 {
		t.Log("Leader election change fixes the issue!")
		t.Log("This explains why it works after leader change")
	}
}

// TestQueueDepth tests if queue depth affects processing
func TestQueueDepth(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Many services to stress the queue
	var services []runtime.Object
	for i := 0; i < 1000; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-b-%d", i),
				Namespace: fmt.Sprintf("ns-%d", i%10),
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
	
	// Start config-b with a small queue
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// Small queue that might overflow
	queue := workqueue.NewRateLimitingQueue(
		workqueue.NewMaxOfRateLimiter(
			workqueue.NewItemExponentialFailureRateLimiter(5*time.Millisecond, 1000*time.Second),
		),
	)
	
	enqueued := int32(0)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			queue.Add(key)
			atomic.AddInt32(&enqueued, 1)
		},
	})
	
	// Slow worker
	processed := int32(0)
	go func() {
		for {
			item, shutdown := queue.Get()
			if shutdown {
				return
			}
			time.Sleep(10 * time.Millisecond) // Slow processing
			atomic.AddInt32(&processed, 1)
			queue.Done(item)
		}
	}()
	
	time.Sleep(2 * time.Second)
	
	enqueuedCount := atomic.LoadInt32(&enqueued)
	processedCount := atomic.LoadInt32(&processed)
	queueLen := queue.Len()
	
	t.Logf("Enqueued: %d", enqueuedCount)
	t.Logf("Processed: %d", processedCount)
	t.Logf("Queue length: %d", queueLen)
	
	if enqueuedCount == 1000 && processedCount < 100 {
		t.Log("Queue is backed up - this could cause delays")
		backlog := enqueuedCount - processedCount
		processingRate := float64(processedCount) / 2.0 // per second
		if processingRate > 0 {
			timeToProcess := float64(backlog) / processingRate / 60.0
			t.Logf("At current rate, would take %.1f minutes to process backlog", timeToProcess)
			
			if timeToProcess > 240 {
				t.Log("This could explain 4+ hour delays!")
			}
		}
	}
}

// TestResourceVersion tests if resource version conflicts cause issues
func TestResourceVersion(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-b",
			Namespace: "default",
			ResourceVersion: "1000", // High resource version
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceB)
	
	// Simulate resource version issues
	fakeClient.PrependReactor("list", "services", func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
		t.Log("List called with possible resource version conflicts")
		return false, nil, nil
	})
	
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()
	
	stopCh := make(chan struct{})
	defer close(stopCh)
	
	// Config-a has been running, has seen many resource versions
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	
	// Simulate updates increasing resource version
	go func() {
		for i := 0; i < 5; i++ {
			time.Sleep(100 * time.Millisecond)
			serviceB.ResourceVersion = fmt.Sprintf("%d", 1001+i)
			fakeClient.CoreV1().Services("default").Update(context.Background(), serviceB, metav1.UpdateOptions{})
		}
	}()
	
	time.Sleep(600 * time.Millisecond)
	
	// Now config-b starts with stale expectations
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	received := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
			svc := obj.(*v1.Service)
			t.Logf("Config-B received service with RV: %s", svc.ResourceVersion)
		},
	})
	
	time.Sleep(500 * time.Millisecond)
	
	if !received {
		t.Log("Resource version mismatch might prevent events")
	}
}