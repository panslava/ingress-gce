package filteredinformer

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestFilteredListerGetByKey tests if the filtered cache's GetByKey works correctly
func TestFilteredListerGetByKey(t *testing.T) {
	// Set up the provider config label key
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

	// Start base informer
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)

	// Create filtered informers
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")

	// Get the listers (like NEG controller does)
	listerA := filteredA.GetIndexer()
	listerB := filteredB.GetIndexer()

	// Test GetByKey for config-a
	keyA := "default/service-a"
	objA, existsA, errA := listerA.GetByKey(keyA)
	if errA != nil {
		t.Errorf("Config-A GetByKey error: %v", errA)
	}
	if !existsA {
		t.Error("Config-A: service-a should exist")
	}
	if objA == nil {
		t.Error("Config-A: service-a object is nil")
	} else {
		svc := objA.(*v1.Service)
		if svc.Name != "service-a" {
			t.Errorf("Config-A: expected service-a, got %s", svc.Name)
		}
	}

	// Test GetByKey for service-b from config-a (should not exist)
	keyB := "default/service-b"
	objAB, existsAB, errAB := listerA.GetByKey(keyB)
	if errAB != nil {
		t.Errorf("Config-A GetByKey error for service-b: %v", errAB)
	}
	if existsAB {
		t.Error("Config-A: service-b should NOT exist (wrong label)")
	}
	if objAB != nil {
		t.Error("Config-A: service-b object should be nil")
	}

	// Test GetByKey for config-b
	objB, existsB, errB := listerB.GetByKey(keyB)
	if errB != nil {
		t.Errorf("Config-B GetByKey error: %v", errB)
	}
	if !existsB {
		t.Error("Config-B: service-b should exist")
	}
	if objB == nil {
		t.Error("Config-B: service-b object is nil")
	} else {
		svc := objB.(*v1.Service)
		if svc.Name != "service-b" {
			t.Errorf("Config-B: expected service-b, got %s", svc.Name)
		}
	}
}

// TestListerAfterHandlerAdded tests if the lister works after handlers are added
func TestListerAfterHandlerAdded(t *testing.T) {
	// Set up the provider config label key
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

	// === Phase 1: Start config-a ===
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {},
	})

	time.Sleep(100 * time.Millisecond)

	// === Phase 2: Start config-b (base already running) ===
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Try to run (no-op)
	go filteredB.Run(stopCh)
	
	// Check if synced
	if !filteredB.HasSynced() {
		t.Error("Config-B should be synced immediately")
	}

	// Get lister BEFORE adding handler
	listerBBefore := filteredB.GetIndexer()
	keyB := "default/service-b"
	
	// Check if service exists in lister before handler
	objBefore, existsBefore, errBefore := listerBBefore.GetByKey(keyB)
	t.Logf("BEFORE handler: exists=%v, obj=%v, err=%v", existsBefore, objBefore != nil, errBefore)

	// Add handler
	handlerReceived := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerReceived = true
			t.Log("Handler received synthetic event")
		},
	})

	time.Sleep(200 * time.Millisecond)

	// Get lister AFTER adding handler
	listerBAfter := filteredB.GetIndexer()
	
	// Check if service exists in lister after handler
	objAfter, existsAfter, errAfter := listerBAfter.GetByKey(keyB)
	t.Logf("AFTER handler: exists=%v, obj=%v, err=%v", existsAfter, objAfter != nil, errAfter)

	if !handlerReceived {
		t.Error("Handler didn't receive synthetic event")
	}

	if !existsAfter {
		t.Error("BUG: Service not found in lister after handler added")
		t.Log("This could explain why processService fails - lister doesn't have the service")
	}

	// Double-check the base cache
	baseStore := baseInformer.GetStore()
	baseObj, baseExists, baseErr := baseStore.GetByKey(keyB)
	t.Logf("Base cache: exists=%v, obj=%v, err=%v", baseExists, baseObj != nil, baseErr)
	
	if baseExists && !existsAfter {
		t.Error("CRITICAL: Service exists in base cache but not in filtered lister")
		t.Log("This confirms the filtering is the issue")
	}
}

// TestListerCacheCoherence tests if the lister and event handlers are coherent
func TestListerCacheCoherence(t *testing.T) {
	// This test checks if there's a timing issue where:
	// 1. Handler receives synthetic event
	// 2. Handler enqueues the key
	// 3. Worker processes the key
	// 4. Lister doesn't have the object yet
	
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

	// Start config-a first
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

	// Simulate work queue
	workQueue := make(chan string, 10)
	
	// Get lister (like NEG controller does)
	lister := filteredB.GetIndexer()
	
	// Add handler that enqueues
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			t.Logf("Handler enqueuing: %s", key)
			workQueue <- key
			
			// Immediately check if lister has it
			obj2, exists, err := lister.GetByKey(key)
			if !exists {
				t.Error("RACE CONDITION: Handler received object but lister doesn't have it")
			} else {
				t.Logf("Lister has object immediately: %v (err=%v)", obj2 != nil, err)
			}
		},
	})

	// Simulate worker
	go func() {
		for key := range workQueue {
			t.Logf("Worker processing: %s", key)
			
			// This is what processService does
			obj, exists, err := lister.GetByKey(key)
			if err != nil {
				t.Errorf("Worker: GetByKey error: %v", err)
			}
			if !exists {
				t.Error("Worker: Service doesn't exist in lister")
				t.Log("This would cause processService to skip the service")
			} else {
				t.Logf("Worker: Found service in lister: %v", obj.(*v1.Service).Name)
			}
		}
	}()

	// Wait for processing
	time.Sleep(500 * time.Millisecond)
	
	// Final check
	finalObj, finalExists, _ := lister.GetByKey("default/service-b")
	if !finalExists {
		t.Error("Final check: Service still not in lister")
	} else {
		t.Logf("Final check: Service in lister: %v", finalObj.(*v1.Service).Name)
	}
}