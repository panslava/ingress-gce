package filteredinformer

import (
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestCriticalRaceCondition tests the exact race condition suspected in production
func TestCriticalRaceCondition(t *testing.T) {
	// This test simulates the exact sequence:
	// 1. Config-A starts informers and waits for sync
	// 2. Config-B starts (base already running)
	// 3. Config-B's hasSynced returns true immediately
	// 4. Config-B's controller starts workers
	// 5. Config-B adds handlers AFTER workers started

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

	// === PHASE 1: Config-A starts ===
	t.Log("PHASE 1: Config-A starting")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")

	// Start informer
	go filteredA.Run(stopCh)

	// Create hasSynced function (like in initializeInformers)
	hasSyncedA := func() bool {
		return filteredA.HasSynced()
	}

	// Wait for sync (like NEG controller does)
	wait.PollUntil(100*time.Millisecond, func() (bool, error) {
		synced := hasSyncedA()
		if synced {
			t.Log("Config-A: hasSynced returned true")
		}
		return synced, nil
	}, stopCh)

	// Simulate NEG controller creation adding handlers
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			t.Log("Config-A: Received event")
		},
	})

	// Simulate workers starting
	t.Log("Config-A: Workers would start here")

	// === PHASE 2: Config-B starts much later ===
	time.Sleep(200 * time.Millisecond)
	t.Log("PHASE 2: Config-B starting")

	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")

	// Try to start (will be no-op)
	go filteredB.Run(stopCh)

	// Create hasSynced function
	hasSyncedB := func() bool {
		synced := filteredB.HasSynced()
		t.Logf("Config-B: HasSynced = %v", synced)
		return synced
	}

	// Check if it's immediately synced (this is the potential bug!)
	immediatelySynced := hasSyncedB()
	t.Logf("Config-B: Immediately synced = %v", immediatelySynced)

	if immediatelySynced {
		t.Log("CRITICAL: Config-B reports synced immediately!")
		t.Log("This means workers would start BEFORE handlers are added")
	}

	// Simulate what happens in NEG controller
	workersStarted := false
	handlersAdded := false

	// In production, this happens in Run()
	wait.PollUntil(100*time.Millisecond, func() (bool, error) {
		synced := hasSyncedB()
		if synced && !workersStarted {
			t.Log("Config-B: Starting workers (hasSynced=true)")
			workersStarted = true
			// Workers start processing queue here
		}
		return synced, nil
	}, stopCh)

	// Handlers are added in NewController AFTER hasSynced check
	time.Sleep(50 * time.Millisecond) // Simulate delay

	eventsReceived := int32(0)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&eventsReceived, 1)
			t.Log("Config-B: Handler received event")
		},
	})
	handlersAdded = true

	t.Logf("Config-B: Workers started = %v, Handlers added = %v", workersStarted, handlersAdded)

	// Wait to see if synthetic events are received
	time.Sleep(200 * time.Millisecond)

	received := atomic.LoadInt32(&eventsReceived)
	if received == 0 {
		t.Error("BUG CONFIRMED: No synthetic events received")
		t.Log("Workers started before handlers were added!")
		t.Log("This explains why services aren't processed")
	} else {
		t.Logf("Received %d synthetic events", received)
	}
}

// TestHasSyncedTiming tests the exact timing of HasSynced
func TestHasSyncedTiming(t *testing.T) {
	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-service",
			Namespace: "default",
		},
	}

	fakeClient := fake.NewSimpleClientset(service)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start base informer
	go baseInformer.Run(stopCh)

	// Track when it becomes synced
	syncTime := time.Time{}
	go func() {
		for !baseInformer.HasSynced() {
			time.Sleep(1 * time.Millisecond)
		}
		syncTime = time.Now()
		t.Log("Base informer synced")
	}()

	// Wait a bit
	time.Sleep(100 * time.Millisecond)

	// Now create filtered informer
	filtered := NewProviderConfigFilteredInformer(baseInformer, "config-a")

	// Check HasSynced immediately
	immediatelySynced := filtered.HasSynced()
	t.Logf("Filtered informer immediately synced: %v", immediatelySynced)

	if immediatelySynced && !syncTime.IsZero() {
		elapsed := time.Since(syncTime)
		t.Logf("Filtered informer created %v after base synced", elapsed)
		t.Log("This confirms filtered informer reports synced immediately when base is already synced")
	}
}

// TestControllerStartupRace simulates the exact controller startup sequence
func TestControllerStartupRace(t *testing.T) {
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

	globalStopCh := make(chan struct{})
	defer close(globalStopCh)

	// Simulate Config-A starting first
	t.Log("Starting Config-A")
	configAStopCh := simulateNEGControllerStartup(t, informerFactory, "config-a", globalStopCh, true)

	time.Sleep(200 * time.Millisecond)

	// Simulate Config-B starting later
	t.Log("Starting Config-B")
	configBEvents := &eventCounter{}
	configBStopCh := simulateNEGControllerStartupWithTracking(t, informerFactory, "config-b", globalStopCh, configBEvents)

	// Wait for potential events
	time.Sleep(500 * time.Millisecond)

	if configBEvents.getCount() == 0 {
		t.Error("Config-B didn't receive any events")
		t.Log("This confirms the race condition in controller startup")
	} else {
		t.Logf("Config-B received %d events", configBEvents.getCount())
	}

	close(configAStopCh)
	close(configBStopCh)
}

type eventCounter struct {
	count int32
}

func (e *eventCounter) increment() {
	atomic.AddInt32(&e.count, 1)
}

func (e *eventCounter) getCount() int32 {
	return atomic.LoadInt32(&e.count)
}

func simulateNEGControllerStartup(t *testing.T, factory informers.SharedInformerFactory, configName string, globalStopCh <-chan struct{}, isFirst bool) chan struct{} {
	return simulateNEGControllerStartupWithTracking(t, factory, configName, globalStopCh, nil)
}

func simulateNEGControllerStartupWithTracking(t *testing.T, factory informers.SharedInformerFactory, configName string, globalStopCh <-chan struct{}, events *eventCounter) chan struct{} {
	providerConfigStopCh := make(chan struct{})

	// Create joined stop channel
	joinedStopCh := make(chan struct{})
	go func() {
		select {
		case <-globalStopCh:
		case <-providerConfigStopCh:
		}
		close(joinedStopCh)
	}()

	// Initialize informers (like initializeInformers)
	baseInformer := factory.Core().V1().Services().Informer()
	filtered := NewProviderConfigFilteredInformer(baseInformer, configName)

	// Start informer
	go filtered.Run(joinedStopCh)

	// Create hasSynced
	hasSynced := func() bool {
		return filtered.HasSynced()
	}

	// Simulate NEG controller Run()
	go func() {
		// Wait for sync (like controller.Run does)
		wait.PollUntil(100*time.Millisecond, func() (bool, error) {
			synced := hasSynced()
			if synced {
				t.Logf("[%s] hasSynced=true, would start workers", configName)
			}
			return synced, nil
		}, joinedStopCh)

		// Workers would start here
		t.Logf("[%s] Workers started", configName)
	}()

	// Simulate delay before adding handlers (this happens in NewController)
	time.Sleep(10 * time.Millisecond)

	// Add handlers (like in NewController)
	filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			t.Logf("[%s] Handler received: %s", configName, svc.Name)
			if events != nil {
				events.increment()
			}
		},
	})

	return providerConfigStopCh
}

// TestWorkQueueProcessing tests if the issue is related to work queue
func TestWorkQueueProcessing(t *testing.T) {
	// Even if synthetic events are sent, they need to be processed by workers
	// If workers start before handlers are added, the queue might be empty

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

	// Start base informer (config-a)
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Add first handler
	baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	time.Sleep(100 * time.Millisecond)

	// Now simulate config-b
	filtered := NewProviderConfigFilteredInformer(baseInformer, "config-b")

	// Check if synced immediately
	if !filtered.HasSynced() {
		t.Error("Expected filtered informer to be synced immediately")
	}

	// Simulate work queue
	queue := make(chan interface{}, 100)

	// Start worker BEFORE adding handler (this is the suspected bug)
	workerStarted := make(chan struct{})
	go func() {
		close(workerStarted)
		for {
			select {
			case item := <-queue:
				t.Logf("Worker processing: %v", item)
			case <-stopCh:
				return
			}
		}
	}()

	<-workerStarted
	t.Log("Worker started before handler added")

	// Now add handler (too late - worker already running with empty queue)
	filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			svc := obj.(*v1.Service)
			t.Logf("Handler queuing: %s", svc.Name)
			queue <- svc.Name
		},
	})

	// Check if anything was queued
	time.Sleep(200 * time.Millisecond)

	if len(queue) == 0 {
		t.Error("Nothing in queue - synthetic events were sent but not queued")
		t.Log("This could explain why services aren't processed")
	} else {
		t.Logf("Queue has %d items", len(queue))
	}
}
