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
	"k8s.io/klog/v2"
)

// TestFindTheRealBug tries to find what's actually broken
func TestFindTheRealBug(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Let's trace EXACTLY what happens
	
	// Services that exist BEFORE config-b starts
	var services []runtime.Object
	for i := 0; i < 3; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("existing-service-b-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"cloud.gke.io/provider-config-name": "config-b",
				},
				ResourceVersion: "100", // These exist already
			},
		}
		services = append(services, service)
	}

	fakeClient := fake.NewSimpleClientset(services...)
	
	// Use 10 minute resync like production
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()
	
	stopCh := make(chan struct{})
	defer close(stopCh)
	
	// === Config-A has been running for hours ===
	t.Log("=== Config-A running (hours) ===")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	
	// Check initial state
	t.Logf("Before Run - filteredA.HasSynced(): %v", filteredA.HasSynced())
	
	go filteredA.Run(stopCh)
	
	// Check after Run
	t.Logf("After Run - filteredA.HasSynced(): %v", filteredA.HasSynced())
	
	// Wait for sync
	synced := cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	t.Logf("Config-A WaitForCacheSync returned: %v", synced)
	
	// Add handler
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			t.Logf("Config-A Add: %s/%s", svc.Namespace, svc.Name)
		},
	})
	
	// Let it stabilize
	time.Sleep(200 * time.Millisecond)
	
	// === Config-B starts hours later ===
	t.Log("=== Config-B starts (hours later) ===")
	
	// This is EXACTLY what happens in production
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Check state BEFORE anything
	t.Logf("Before Run - filteredB.HasSynced(): %v", filteredB.HasSynced())
	
	// Check the underlying informer state
	baseStarted := baseInformer.HasSynced()
	t.Logf("Base informer HasSynced: %v", baseStarted)
	
	// Run the filtered informer
	go filteredB.Run(stopCh)
	
	// Immediately after Run
	t.Logf("After Run - filteredB.HasSynced(): %v", filteredB.HasSynced())
	
	// Check the store/lister
	lister := filteredB.GetStore()
	items := lister.List()
	t.Logf("Items in lister immediately: %d", len(items))
	for _, item := range items {
		svc := item.(*v1.Service)
		t.Logf("  Lister has: %s/%s", svc.Namespace, svc.Name)
	}
	
	// Now add handler
	events := []string{}
	var mu sync.Mutex
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			mu.Lock()
			events = append(events, fmt.Sprintf("Add:%s", key))
			mu.Unlock()
			t.Logf("Config-B Add: %s/%s (RV:%s)", svc.Namespace, svc.Name, svc.ResourceVersion)
		},
		UpdateFunc: func(old, new interface{}) {
			svc := new.(*v1.Service)
			key, _ := cache.MetaNamespaceKeyFunc(new)
			mu.Lock()
			events = append(events, fmt.Sprintf("Update:%s", key))
			mu.Unlock()
			t.Logf("Config-B Update: %s/%s (RV:%s)", svc.Namespace, svc.Name, svc.ResourceVersion)
		},
	})
	
	// Wait for events
	time.Sleep(500 * time.Millisecond)
	
	// Check what happened
	mu.Lock()
	eventCount := len(events)
	mu.Unlock()
	
	t.Logf("Events received: %d", eventCount)
	for i, evt := range events {
		t.Logf("  Event %d: %s", i+1, evt)
	}
	
	// Check lister again
	finalItems := lister.List()
	t.Logf("Final items in lister: %d", len(finalItems))
	
	// Check specific keys
	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("default/existing-service-b-%d", i)
		obj, exists, err := lister.GetByKey(key)
		t.Logf("GetByKey(%s): exists=%v, obj=%v, err=%v", key, exists, obj != nil, err)
	}
	
	// The question is: why would this work in test but not in production?
	if eventCount == 3 {
		t.Log("✓ All events received in test")
		t.Log("So why doesn't it work in production?")
		t.Log("Differences between test and production:")
		t.Log("  1. Real API server vs fake client")
		t.Log("  2. Network latency")
		t.Log("  3. Actual leader election")
		t.Log("  4. Multiple controllers running")
		t.Log("  5. Real GCE API calls")
	} else if eventCount == 0 {
		t.Error("NO EVENTS! This reproduces the bug")
	}
}

// TestSharedInformerInternals looks at the internals
func TestSharedInformerInternals(t *testing.T) {
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
	
	// Track handler registration
	handlerIDs := []cache.ResourceEventHandlerRegistration{}
	
	reg1, err1 := filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})
	if err1 == nil && reg1 != nil {
		handlerIDs = append(handlerIDs, reg1)
		t.Logf("Config-A handler registered: %v", reg1)
	}
	
	time.Sleep(100 * time.Millisecond)
	
	// Start config-b
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	received := int32(0)
	reg2, err2 := filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&received, 1)
		},
	})
	if err2 == nil && reg2 != nil {
		handlerIDs = append(handlerIDs, reg2)
		t.Logf("Config-B handler registered: %v", reg2)
	}
	
	time.Sleep(500 * time.Millisecond)
	
	count := atomic.LoadInt32(&received)
	t.Logf("Config-B received %d events", count)
	
	// Check if there's something about handler registration
	t.Logf("Total handlers registered: %d", len(handlerIDs))
	
	if count == 0 {
		t.Log("Possible issues:")
		t.Log("  1. Handler registration failed silently")
		t.Log("  2. Filter function not working correctly")
		t.Log("  3. Base informer state corrupted")
	}
}

// TestWhatIfFactoryIsNeverStarted checks if factory.Start() matters
func TestWhatIfFactoryIsNeverStarted(t *testing.T) {
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
	
	// DO NOT call informerFactory.Start(stopCh)
	// This is what happens in multiproject mode
	
	baseInformer := informerFactory.Core().V1().Services().Informer()
	
	stopCh := make(chan struct{})
	defer close(stopCh)
	
	// Try to use it directly
	t.Log("Testing without factory.Start()")
	
	// What's the state of the base informer?
	t.Logf("Base informer HasSynced before any Run: %v", baseInformer.HasSynced())
	
	// Start filtered informer
	filtered := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filtered.Run(stopCh)
	
	// Add handler and see what happens
	received := false
	filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
			t.Log("Received event without factory.Start()!")
		},
	})
	
	time.Sleep(500 * time.Millisecond)
	
	if !received {
		t.Error("No event without factory.Start()")
		t.Log("This could be the issue!")
	} else {
		t.Log("Event received even without factory.Start()")
		t.Log("So factory.Start() might not be the issue")
	}
}

// TestMultipleProviderConfigs simulates multiple provider configs
func TestMultipleProviderConfigs(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Services for different configs
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
	serviceC := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-c",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-c",
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(serviceA, serviceB, serviceC)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()
	
	stopCh := make(chan struct{})
	defer close(stopCh)
	
	// Start multiple configs in sequence
	configs := []string{"config-a", "config-b", "config-c"}
	results := make(map[string]int)
	var wg sync.WaitGroup
	
	for i, config := range configs {
		config := config // capture
		
		// Delay between starting configs
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		
		t.Logf("Starting %s", config)
		
		filtered := NewProviderConfigFilteredInformer(baseInformer, config)
		go filtered.Run(stopCh)
		
		wg.Add(1)
		go func() {
			defer wg.Done()
			
			events := 0
			filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					events++
					svc := obj.(*v1.Service)
					t.Logf("%s received: %s", config, svc.Name)
				},
			})
			
			time.Sleep(500 * time.Millisecond)
			results[config] = events
		}()
	}
	
	wg.Wait()
	
	// Check results
	for _, config := range configs {
		t.Logf("%s: %d events", config, results[config])
		if results[config] == 0 {
			t.Errorf("%s received no events", config)
		}
	}
	
	// If later configs don't receive events, that's the bug
	if results["config-a"] > 0 && results["config-b"] == 0 {
		t.Error("BUG: Second config doesn't receive events!")
	}
}

// TestLoggerOutput checks if logging might reveal the issue
func TestLoggerOutput(t *testing.T) {
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
	
	// Enable verbose logging to see what's happening
	logger := klog.FromContext(nil)
	
	// Config-A
	logger.V(2).Info("Starting config-a")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			logger.V(3).Info("Config-A add event", "obj", obj)
		},
	})
	
	time.Sleep(100 * time.Millisecond)
	
	// Config-B
	logger.V(2).Info("Starting config-b")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	received := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
			logger.V(3).Info("Config-B add event", "obj", obj)
		},
	})
	
	time.Sleep(500 * time.Millisecond)
	
	if !received {
		logger.Error(nil, "Config-B didn't receive event")
		t.Log("Check klog output for more details")
	}
}