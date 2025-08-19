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
	"k8s.io/ingress-gce/pkg/flags"
)

// TestResyncNotWorking tests why 10-minute resync doesn't help
func TestResyncNotWorking(t *testing.T) {
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
	
	// Use a very short resync period for testing
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 1*time.Second)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start config-a first
	t.Log("Starting config-a")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	
	configAResyncs := int32(0)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			t.Log("Config-A: Add event")
		},
		UpdateFunc: func(old, new interface{}) {
			atomic.AddInt32(&configAResyncs, 1)
			t.Log("Config-A: Update event (resync)")
		},
	})

	// Wait for at least one resync
	time.Sleep(2 * time.Second)
	
	resyncCountA := atomic.LoadInt32(&configAResyncs)
	t.Logf("Config-A had %d resyncs in 2 seconds", resyncCountA)

	// Now start config-b
	t.Log("Starting config-b after config-a is stable")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Track when things happen
	runStarted := time.Now()
	go filteredB.Run(stopCh)
	
	// HasSynced returns true immediately
	hasSyncedTime := time.Now()
	synced := filteredB.HasSynced()
	t.Logf("HasSynced returned %v after %v", synced, hasSyncedTime.Sub(runStarted))
	
	// Add handler
	addEvents := int32(0)
	updateEvents := int32(0)
	handlerAddedTime := time.Now()
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			count := atomic.AddInt32(&addEvents, 1)
			elapsed := time.Since(handlerAddedTime)
			t.Logf("Config-B: Add event #%d at %v after handler added", count, elapsed)
		},
		UpdateFunc: func(old, new interface{}) {
			count := atomic.AddInt32(&updateEvents, 1)
			elapsed := time.Since(handlerAddedTime)
			t.Logf("Config-B: Update event #%d (resync) at %v after handler added", count, elapsed)
		},
	})
	
	// Wait for multiple resync periods
	t.Log("Waiting for resync events...")
	time.Sleep(5 * time.Second)
	
	adds := atomic.LoadInt32(&addEvents)
	updates := atomic.LoadInt32(&updateEvents)
	
	t.Logf("Config-B results after 5 seconds:")
	t.Logf("  Add events: %d", adds)
	t.Logf("  Update events (resyncs): %d", updates)
	
	if adds == 0 && updates == 0 {
		t.Error("BUG: Config-B received NO events even after multiple resync periods")
		t.Log("This explains why resync doesn't help in production!")
	} else if adds == 0 && updates > 0 {
		t.Log("Config-B only gets resync updates, no initial Add")
		t.Log("If the controller needs Add events to initialize, resync won't help")
	}
}

// TestResyncWithHandlerTiming tests if handler registration timing affects resync
func TestResyncWithHandlerTiming(t *testing.T) {
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
	
	// Short resync for testing
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 500*time.Millisecond)
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

	// Wait for multiple resyncs to pass
	time.Sleep(2 * time.Second)

	// Start config-b
	t.Log("Starting config-b with different handler timing")
	
	// Test 1: Add handler immediately after Run
	t.Log("Test 1: Handler added immediately")
	filteredB1 := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB1.Run(stopCh)
	
	events1 := int32(0)
	filteredB1.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&events1, 1)
		},
		UpdateFunc: func(old, new interface{}) {
			atomic.AddInt32(&events1, 1)
		},
	})
	
	// Test 2: Add handler after a delay
	t.Log("Test 2: Handler added after delay")
	filteredB2 := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB2.Run(stopCh)
	
	time.Sleep(100 * time.Millisecond) // Delay handler registration
	
	events2 := int32(0)
	filteredB2.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&events2, 1)
		},
		UpdateFunc: func(old, new interface{}) {
			atomic.AddInt32(&events2, 1)
		},
	})
	
	// Wait for resyncs
	time.Sleep(2 * time.Second)
	
	count1 := atomic.LoadInt32(&events1)
	count2 := atomic.LoadInt32(&events2)
	
	t.Logf("Events with immediate handler: %d", count1)
	t.Logf("Events with delayed handler: %d", count2)
	
	if count1 == 0 || count2 == 0 {
		t.Log("Handler registration timing affects event delivery")
	}
}

// TestResyncPeriodPropagation tests if resync period is properly propagated
func TestResyncPeriodPropagation(t *testing.T) {
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
	
	// Create factory with 10 minute resync (production setting)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start config-a
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	
	// Add handler with custom resync period
	events := int32(0)
	filteredA.AddEventHandlerWithResyncPeriod(
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				atomic.AddInt32(&events, 1)
			},
			UpdateFunc: func(old, new interface{}) {
				atomic.AddInt32(&events, 1)
			},
		},
		1*time.Second, // Request 1-second resync
	)
	
	// Wait to see if custom resync works
	time.Sleep(3 * time.Second)
	
	eventCount := atomic.LoadInt32(&events)
	t.Logf("Events with custom resync period: %d", eventCount)
	
	if eventCount < 2 {
		t.Log("Custom resync period might not be working")
		t.Log("This could explain why 10-minute resync doesn't help")
	}
}

// TestWhyResyncDoesntHelp explains why resync doesn't solve the problem
func TestWhyResyncDoesntHelp(t *testing.T) {
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
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 1*time.Second)
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

	time.Sleep(2 * time.Second)

	// Start config-b exactly like production
	t.Log("Starting config-b like in production")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// HasSynced returns true immediately
	if filteredB.HasSynced() {
		t.Log("HasSynced=true immediately (workers would start now)")
	}
	
	// Simulate delay before handler registration (like createNEGController)
	time.Sleep(50 * time.Millisecond)
	
	addReceived := false
	updateReceived := false
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			addReceived = true
			svc := obj.(*v1.Service)
			t.Logf("Add event for: %s", svc.Name)
		},
		UpdateFunc: func(old, new interface{}) {
			updateReceived = true
			svc := new.(*v1.Service)
			t.Logf("Update event for: %s", svc.Name)
		},
	})
	
	// Wait for resync
	time.Sleep(3 * time.Second)
	
	t.Log("=== Analysis ===")
	if !addReceived && !updateReceived {
		t.Error("No events received at all - even resync doesn't help!")
		t.Log("REASON: Handler was added to already-running informer")
		t.Log("The filtered handler might not be in the resync list")
	} else if !addReceived && updateReceived {
		t.Log("Only Update events from resync, no Add event")
		t.Log("PROBLEM: NEG controller might need Add event to initialize")
		t.Log("Update events might be ignored without initial Add")
	} else if addReceived {
		t.Log("Add event was received (synthetic or resync)")
	}
	
	t.Log("\n=== Why 4+ hours instead of 10 minutes ===")
	t.Log("1. Resync might not trigger for late-added handlers")
	t.Log("2. Even if resync triggers, it sends Update not Add")
	t.Log("3. Controller might need Add event to create internal state")
	t.Log("4. Without initial state, Update events are ignored")
	t.Log("5. Only leader election or restart forces re-initialization")
}