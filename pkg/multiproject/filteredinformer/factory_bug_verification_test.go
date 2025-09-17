package filteredinformer

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestProductionBugVerification verifies if missing factory.Start() causes the bug
func TestProductionBugVerification(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create resources like in production
	var resources []runtime.Object
	
	// Services for config-b
	for i := 0; i < 5; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-b-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"cloud.gke.io/provider-config-name": "config-b",
				},
			},
		}
		resources = append(resources, service)
	}
	
	// Pods (no specific config)
	for i := 0; i < 10; i++ {
		pod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("pod-%d", i),
				Namespace: "default",
			},
		}
		resources = append(resources, pod)
	}
	
	// Ingresses for config-b
	for i := 0; i < 2; i++ {
		ingress := &networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("ingress-b-%d", i),
				Namespace: "default",
				Labels: map[string]string{
					"cloud.gke.io/provider-config-name": "config-b",
				},
			},
		}
		resources = append(resources, ingress)
	}
	
	fakeClient := fake.NewSimpleClientset(resources...)
	
	// Test both scenarios
	scenarios := []struct {
		name         string
		startFactory bool
	}{
		{"WithoutFactoryStart", false},
		{"WithFactoryStart", true},
	}
	
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			// Create factory with SHORT resync for testing (1 second instead of 10 minutes)
			informerFactory := informers.NewSharedInformerFactory(fakeClient, 1*time.Second)
			
			// Get base informers (like production does)
			serviceInformer := informerFactory.Core().V1().Services().Informer()
			podInformer := informerFactory.Core().V1().Pods().Informer()
			ingressInformer := informerFactory.Networking().V1().Ingresses().Informer()
			
			stopCh := make(chan struct{})
			defer close(stopCh)
			
			// Conditionally start factory
			if scenario.startFactory {
				t.Log("Starting factory with factory.Start()")
				informerFactory.Start(stopCh)
			} else {
				t.Log("NOT starting factory (production bug scenario)")
			}
			
			// === Simulate config-a running for hours ===
			t.Log("Starting config-a (simulating hours of runtime)")
			
			// Create filtered informers for config-a
			serviceFilteredA := NewProviderConfigFilteredInformer(serviceInformer, "config-a")
			podFilteredA := NewProviderConfigFilteredInformer(podInformer, "config-a")
			ingressFilteredA := NewProviderConfigFilteredInformer(ingressInformer, "config-a")
			
			// Run them individually (like production)
			go serviceFilteredA.Run(stopCh)
			go podFilteredA.Run(stopCh)
			go ingressFilteredA.Run(stopCh)
			
			// Wait for sync
			cache.WaitForCacheSync(stopCh,
				serviceFilteredA.HasSynced,
				podFilteredA.HasSynced,
				ingressFilteredA.HasSynced)
			
			// Add handlers for config-a
			serviceFilteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {},
			})
			
			// Simulate hours passing
			time.Sleep(100 * time.Millisecond)
			
			// === Now config-b starts (like production) ===
			t.Log("Starting config-b (hours later)")
			
			// Track exact timing
			configBStartTime := time.Now()
			
			// Create filtered informers for config-b
			serviceFilteredB := NewProviderConfigFilteredInformer(serviceInformer, "config-b")
			podFilteredB := NewProviderConfigFilteredInformer(podInformer, "config-b")
			ingressFilteredB := NewProviderConfigFilteredInformer(ingressInformer, "config-b")
			
			// Run them (initializeInformers in neg.go)
			go serviceFilteredB.Run(stopCh)
			go podFilteredB.Run(stopCh)
			go ingressFilteredB.Run(stopCh)
			
			// Create aggregate hasSynced
			hasSynced := func() bool {
				return serviceFilteredB.HasSynced() &&
					podFilteredB.HasSynced() &&
					ingressFilteredB.HasSynced()
			}
			
			// Check immediate sync status
			immediateSynced := hasSynced()
			t.Logf("HasSynced immediately: %v", immediateSynced)
			
			// Simulate NEG controller initialization
			queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
			lister := serviceFilteredB.GetStore()
			
			// Wait for sync (like negController.Run())
			if !cache.WaitForCacheSync(stopCh, hasSynced) {
				t.Fatal("Failed to sync")
			}
			
			// Track events
			serviceEvents := int32(0)
			serviceResyncs := int32(0)
			ingressEvents := int32(0)
			firstEventTime := time.Time{}
			var firstEventOnce sync.Once
			
			// Add handlers (happens in createNEGController)
			serviceFilteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					count := atomic.AddInt32(&serviceEvents, 1)
					firstEventOnce.Do(func() {
						firstEventTime = time.Now()
					})
					key, _ := cache.MetaNamespaceKeyFunc(obj)
					queue.Add(key)
					t.Logf("Service Add event %d: %s", count, key)
				},
				UpdateFunc: func(old, new interface{}) {
					atomic.AddInt32(&serviceResyncs, 1)
					key, _ := cache.MetaNamespaceKeyFunc(new)
					queue.Add(key)
					elapsed := time.Since(configBStartTime)
					t.Logf("Service Update (resync) at %v: %s", elapsed, key)
				},
			})
			
			ingressFilteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					atomic.AddInt32(&ingressEvents, 1)
				},
			})
			
			// Start worker
			processed := int32(0)
			go func() {
				for {
					item, shutdown := queue.Get()
					if shutdown {
						return
					}
					key := item.(string)
					obj, exists, err := lister.GetByKey(key)
					if err != nil {
						t.Logf("Worker error: %v", err)
					} else if !exists {
						t.Logf("Worker: %s doesn't exist in lister", key)
					} else if obj != nil {
						atomic.AddInt32(&processed, 1)
					}
					queue.Done(item)
				}
			}()
			
			// Wait for initial events
			time.Sleep(500 * time.Millisecond)
			
			initialServices := atomic.LoadInt32(&serviceEvents)
			initialIngresses := atomic.LoadInt32(&ingressEvents)
			initialProcessed := atomic.LoadInt32(&processed)
			
			t.Logf("Initial results (500ms):")
			t.Logf("  Service events: %d (expected 5)", initialServices)
			t.Logf("  Ingress events: %d (expected 2)", initialIngresses)
			t.Logf("  Processed: %d", initialProcessed)
			
			if !firstEventTime.IsZero() {
				delay := firstEventTime.Sub(configBStartTime)
				t.Logf("  First event delay: %v", delay)
			}
			
			// Wait for resync periods
			t.Log("Waiting for resync (3 seconds)...")
			time.Sleep(3 * time.Second)
			
			finalServices := atomic.LoadInt32(&serviceEvents)
			finalResyncs := atomic.LoadInt32(&serviceResyncs)
			finalProcessed := atomic.LoadInt32(&processed)
			
			t.Logf("Final results after resync:")
			t.Logf("  Service events: %d", finalServices)
			t.Logf("  Service resyncs: %d", finalResyncs)
			t.Logf("  Total processed: %d", finalProcessed)
			
			// Analyze results
			if scenario.startFactory {
				if finalServices == 0 {
					t.Error("Even WITH factory.Start(), no service events!")
				}
				if finalResyncs == 0 {
					t.Log("No resyncs even with factory.Start()")
				}
			} else {
				if finalServices == 0 {
					t.Error("BUG CONFIRMED: No service events without factory.Start()")
					t.Log("This would cause the 4+ hour delay in production")
				} else if finalServices < 5 {
					t.Errorf("Only %d/5 services received", finalServices)
				}
				
				if finalResyncs == 0 {
					t.Log("NO RESYNCS without factory.Start()")
					t.Log("This explains why 10-minute resync doesn't help")
				}
			}
			
			// Check if services are in the lister
			t.Log("Checking lister contents:")
			for i := 0; i < 5; i++ {
				key := fmt.Sprintf("default/service-b-%d", i)
				obj, exists, _ := lister.GetByKey(key)
				if !exists {
					t.Errorf("Service %s not in lister", key)
				} else if obj == nil {
					t.Errorf("Service %s in lister but obj is nil", key)
				}
			}
		})
	}
}

// TestResyncWithoutFactoryStart specifically tests resync behavior
func TestResyncWithoutFactoryStart(t *testing.T) {
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
	
	t.Run("ResyncWithoutFactoryStart", func(t *testing.T) {
		// Create factory with 500ms resync
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 500*time.Millisecond)
		baseInformer := informerFactory.Core().V1().Services().Informer()
		
		stopCh := make(chan struct{})
		defer close(stopCh)
		
		// DO NOT call factory.Start()
		
		// Start config-a
		filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
		go filteredA.Run(stopCh)
		cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
		
		// Track config-a resyncs
		configAResyncs := int32(0)
		filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
			UpdateFunc: func(old, new interface{}) {
				atomic.AddInt32(&configAResyncs, 1)
				t.Log("Config-A resync")
			},
		})
		
		// Wait for multiple resync periods
		t.Log("Waiting for config-a resyncs...")
		time.Sleep(2 * time.Second)
		
		resyncCountA := atomic.LoadInt32(&configAResyncs)
		t.Logf("Config-A resyncs in 2 seconds: %d (expected ~4)", resyncCountA)
		
		// Now start config-b
		filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		go filteredB.Run(stopCh)
		
		configBEvents := []string{}
		var mu sync.Mutex
		
		filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				mu.Lock()
				configBEvents = append(configBEvents, "Add")
				mu.Unlock()
				t.Log("Config-B: Add event")
			},
			UpdateFunc: func(old, new interface{}) {
				mu.Lock()
				configBEvents = append(configBEvents, "Update")
				mu.Unlock()
				t.Log("Config-B: Update event (resync)")
			},
		})
		
		// Wait for resyncs
		t.Log("Waiting for config-b events...")
		time.Sleep(2 * time.Second)
		
		mu.Lock()
		eventCount := len(configBEvents)
		mu.Unlock()
		
		t.Logf("Config-B events: %d", eventCount)
		
		if eventCount == 0 {
			t.Error("Config-B received NO events - not even resync!")
			t.Log("This confirms resync is broken without factory.Start()")
		}
		
		// Check if resync period is actually being used
		if resyncCountA < 2 {
			t.Log("Resync period not working properly without factory.Start()")
		}
	})
}

// TestFactoryStartFixesIssue verifies that calling factory.Start() fixes the issue
func TestFactoryStartFixesIssue(t *testing.T) {
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
	
	// THE FIX: Call factory.Start()
	t.Log("THE FIX: Calling factory.Start()")
	informerFactory.Start(stopCh)
	
	// Rest of the flow as in production
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})
	
	time.Sleep(100 * time.Millisecond)
	
	// Config-B starts
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	received := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
			t.Log("Config-B received event WITH factory.Start() fix")
		},
	})
	
	time.Sleep(200 * time.Millisecond)
	
	if !received {
		t.Error("Fix didn't work - still no events")
	} else {
		t.Log("SUCCESS: factory.Start() fixes the issue!")
	}
}