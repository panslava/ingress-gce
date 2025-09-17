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

// TestFactoryNotStarted tests what happens when factory.Start() is not called
func TestFactoryNotStarted(t *testing.T) {
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
	
	t.Run("WithoutFactoryStart", func(t *testing.T) {
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 1*time.Second)
		baseInformer := informerFactory.Core().V1().Services().Informer()
		
		stopCh := make(chan struct{})
		defer close(stopCh)
		
		// DO NOT call informerFactory.Start(stopCh)
		
		// Start config-a
		filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
		go filteredA.Run(stopCh)
		
		eventsA := int32(0)
		resyncsA := int32(0)
		filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				atomic.AddInt32(&eventsA, 1)
			},
			UpdateFunc: func(old, new interface{}) {
				atomic.AddInt32(&resyncsA, 1)
			},
		})
		
		// Wait for potential resync
		time.Sleep(3 * time.Second)
		
		// Start config-b
		filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		go filteredB.Run(stopCh)
		
		eventsB := int32(0)
		resyncsB := int32(0)
		filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				atomic.AddInt32(&eventsB, 1)
				t.Log("Config-B: Add event")
			},
			UpdateFunc: func(old, new interface{}) {
				atomic.AddInt32(&resyncsB, 1)
				t.Log("Config-B: Update event (resync)")
			},
		})
		
		// Wait for resync periods
		time.Sleep(3 * time.Second)
		
		eventCountA := atomic.LoadInt32(&eventsA)
		resyncCountA := atomic.LoadInt32(&resyncsA)
		eventCountB := atomic.LoadInt32(&eventsB)
		resyncCountB := atomic.LoadInt32(&resyncsB)
		
		t.Logf("Without factory.Start():")
		t.Logf("  Config-A: %d events, %d resyncs", eventCountA, resyncCountA)
		t.Logf("  Config-B: %d events, %d resyncs", eventCountB, resyncCountB)
		
		if resyncCountA == 0 && resyncCountB == 0 {
			t.Log("NO RESYNCS! This explains why 10-minute resync doesn't work")
		}
		
		if eventCountB == 0 {
			t.Error("Config-B got NO events without factory.Start()")
		}
	})
	
	t.Run("WithFactoryStart", func(t *testing.T) {
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 1*time.Second)
		baseInformer := informerFactory.Core().V1().Services().Informer()
		
		stopCh := make(chan struct{})
		defer close(stopCh)
		
		// CALL factory.Start()
		informerFactory.Start(stopCh)
		
		// Start config-a
		filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
		go filteredA.Run(stopCh)
		
		eventsA := int32(0)
		resyncsA := int32(0)
		filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				atomic.AddInt32(&eventsA, 1)
			},
			UpdateFunc: func(old, new interface{}) {
				atomic.AddInt32(&resyncsA, 1)
			},
		})
		
		// Wait for potential resync
		time.Sleep(3 * time.Second)
		
		// Start config-b
		filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		go filteredB.Run(stopCh)
		
		eventsB := int32(0)
		resyncsB := int32(0)
		filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				atomic.AddInt32(&eventsB, 1)
				t.Log("Config-B: Add event")
			},
			UpdateFunc: func(old, new interface{}) {
				atomic.AddInt32(&resyncsB, 1)
				t.Log("Config-B: Update event (resync)")
			},
		})
		
		// Wait for resync periods
		time.Sleep(3 * time.Second)
		
		eventCountA := atomic.LoadInt32(&eventsA)
		resyncCountA := atomic.LoadInt32(&resyncsA)
		eventCountB := atomic.LoadInt32(&eventsB)
		resyncCountB := atomic.LoadInt32(&resyncsB)
		
		t.Logf("With factory.Start():")
		t.Logf("  Config-A: %d events, %d resyncs", eventCountA, resyncCountA)
		t.Logf("  Config-B: %d events, %d resyncs", eventCountB, resyncCountB)
		
		if resyncCountA > 0 || resyncCountB > 0 {
			t.Log("Resyncs working with factory.Start()")
		}
		
		if eventCountB == 0 {
			t.Error("Config-B got NO events even with factory.Start()")
		}
	})
}

// TestFactoryStartTiming tests when factory.Start() should be called
func TestFactoryStartTiming(t *testing.T) {
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
	
	t.Run("FactoryStartBeforeRun", func(t *testing.T) {
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
		baseInformer := informerFactory.Core().V1().Services().Informer()
		
		stopCh := make(chan struct{})
		defer close(stopCh)
		
		// Start factory FIRST
		informerFactory.Start(stopCh)
		
		// Then create and run filtered informers
		filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		go filteredB.Run(stopCh)
		
		received := false
		filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				received = true
			},
		})
		
		time.Sleep(200 * time.Millisecond)
		
		if !received {
			t.Error("No event with factory.Start() before Run()")
		}
	})
	
	t.Run("FactoryStartAfterRun", func(t *testing.T) {
		informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
		baseInformer := informerFactory.Core().V1().Services().Informer()
		
		stopCh := make(chan struct{})
		defer close(stopCh)
		
		// Create and run filtered informers FIRST
		filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
		go filteredB.Run(stopCh)
		
		received := false
		filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				received = true
			},
		})
		
		// Start factory AFTER
		informerFactory.Start(stopCh)
		
		time.Sleep(200 * time.Millisecond)
		
		if !received {
			t.Error("No event with factory.Start() after Run()")
		}
	})
}