package filteredinformer

import (
	"fmt"
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

// TestListerCacheTiming tests if the lister cache is populated when handlers fire
func TestListerCacheTiming(t *testing.T) {
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

	// Start config-b
	t.Log("Starting config-b")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// Get the lister (filtered cache)
	lister := filteredB.GetStore()
	
	// Check cache before handler
	key := "default/service-b"
	objBefore, existsBefore, errBefore := lister.GetByKey(key)
	t.Logf("Cache before handler: exists=%v, obj=%v, err=%v", existsBefore, objBefore != nil, errBefore)
	
	// Add handler
	handlerCalled := false
	var handlerObj interface{}
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerCalled = true
			handlerObj = obj
			
			// Check cache when handler fires
			objInHandler, existsInHandler, errInHandler := lister.GetByKey(key)
			t.Logf("Cache in handler: exists=%v, obj=%v, err=%v", existsInHandler, objInHandler != nil, errInHandler)
			
			if !existsInHandler {
				t.Error("BUG: Object not in cache when handler fires!")
				t.Log("This would cause NEG controller to skip the service")
			}
		},
	})
	
	// Wait for handler
	time.Sleep(200 * time.Millisecond)
	
	// Check cache after handler
	objAfter, existsAfter, errAfter := lister.GetByKey(key)
	t.Logf("Cache after handler: exists=%v, obj=%v, err=%v", existsAfter, objAfter != nil, errAfter)
	
	if handlerCalled {
		t.Log("Handler was called")
		if handlerObj != nil {
			svc := handlerObj.(*v1.Service)
			t.Logf("Handler received: %s/%s", svc.Namespace, svc.Name)
		}
	} else {
		t.Error("Handler was not called")
	}
	
	if !existsAfter {
		t.Error("Object not in filtered cache even after handler!")
		t.Log("This explains why services aren't processed")
	}
}

// TestFilteredCacheVsBaseCache compares filtered cache with base cache
func TestFilteredCacheVsBaseCache(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	var services []runtime.Object
	for i := 0; i < 5; i++ {
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

	// Start config-b
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	handlerCount := 0
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			handlerCount++
		},
	})
	
	time.Sleep(200 * time.Millisecond)
	
	// Compare caches
	baseCache := baseInformer.GetStore()
	filteredCache := filteredB.GetStore()
	
	baseItems := baseCache.List()
	filteredItems := filteredCache.List()
	
	t.Logf("Base cache has %d items", len(baseItems))
	t.Logf("Filtered cache has %d items", len(filteredItems))
	t.Logf("Handler received %d events", handlerCount)
	
	// Check specific items
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("default/service-b-%d", i)
		
		baseObj, baseExists, _ := baseCache.GetByKey(key)
		filteredObj, filteredExists, _ := filteredCache.GetByKey(key)
		
		if baseExists && !filteredExists {
			t.Errorf("Service %s in base cache but not in filtered cache", key)
		}
		
		if baseObj != nil && filteredObj == nil {
			t.Errorf("Service %s object exists in base but nil in filtered", key)
		}
	}
	
	if handlerCount != len(filteredItems) {
		t.Errorf("Handler count (%d) doesn't match filtered items (%d)", handlerCount, len(filteredItems))
		t.Log("This could cause cache inconsistency")
	}
}

// TestListerAfterWorkerStart simulates NEG controller's exact flow
func TestListerAfterWorkerStart(t *testing.T) {
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

	// Start config-b with NEG controller pattern
	t.Log("Starting config-b with NEG controller pattern")
	
	// 1. Create and run informer
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)
	
	// 2. Create lister (used by NEG controller)
	lister := filteredB.GetStore()
	
	// 3. Wait for sync (returns immediately!)
	if !cache.WaitForCacheSync(stopCh, filteredB.HasSynced) {
		t.Fatal("Cache sync failed")
	}
	t.Log("Cache synced (HasSynced=true)")
	
	// 4. Add handler (happens in createNEGController)
	workQueue := make(chan string, 10)
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, _ := cache.MetaNamespaceKeyFunc(obj)
			workQueue <- key
			t.Logf("Handler enqueued: %s", key)
		},
	})
	
	// 5. Worker starts (because HasSynced was true)
	go func() {
		t.Log("Worker started")
		for key := range workQueue {
			// Simulate NEG controller's processService
			obj, exists, err := lister.GetByKey(key)
			if err != nil {
				t.Errorf("Worker: error getting %s: %v", key, err)
				continue
			}
			if !exists {
				t.Errorf("Worker: %s doesn't exist in lister!", key)
				t.Log("BUG: This is what happens in production!")
				t.Log("Worker tries to process but lister says it doesn't exist")
				continue
			}
			if obj == nil {
				t.Errorf("Worker: %s exists but obj is nil", key)
				continue
			}
			t.Logf("Worker: successfully got %s from lister", key)
		}
	}()
	
	// Wait for processing
	time.Sleep(500 * time.Millisecond)
	
	// Final check
	key := "default/service-b"
	obj, exists, err := lister.GetByKey(key)
	t.Logf("Final lister check: key=%s, exists=%v, obj=%v, err=%v", key, exists, obj != nil, err)
	
	if !exists {
		t.Error("Service not in lister even after everything!")
		t.Log("This explains the 4+ hour delay")
	}
}