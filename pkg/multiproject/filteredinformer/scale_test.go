package filteredinformer

import (
	"context"
	"fmt"
	goruntime "runtime"
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

// TestScaleWithManyServices tests with many services to simulate production scale
func TestScaleWithManyServices(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create many services
	var services []runtime.Object

	// 1000 services for config-a
	for i := 0; i < 1000; i++ {
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

	// 1000 services for config-b
	for i := 0; i < 1000; i++ {
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

	// 1000 services with no label
	for i := 0; i < 1000; i++ {
		service := &v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("service-none-%d", i),
				Namespace: "default",
			},
		}
		services = append(services, service)
	}

	fakeClient := fake.NewSimpleClientset(services...)
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 10*time.Minute)
	baseInformer := informerFactory.Core().V1().Services().Informer()

	stopCh := make(chan struct{})
	defer close(stopCh)

	// Start config-a first
	t.Log("Starting config-a with 3000 services in cluster")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)

	eventsA := int32(0)
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			atomic.AddInt32(&eventsA, 1)
		},
	})

	// Let it process
	time.Sleep(500 * time.Millisecond)

	countA := atomic.LoadInt32(&eventsA)
	if countA != 1000 {
		t.Errorf("Config-A should have received 1000 events, got %d", countA)
	}

	// Now start config-b
	t.Log("Starting config-b (base already running with 3000 services)")
	startTime := time.Now()
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	eventsB := int32(0)
	firstEventTime := time.Time{}
	var firstEventOnce sync.Once

	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			count := atomic.AddInt32(&eventsB, 1)
			firstEventOnce.Do(func() {
				firstEventTime = time.Now()
				t.Logf("First event received after %v", firstEventTime.Sub(startTime))
			})
			if count%100 == 0 {
				t.Logf("Received %d events", count)
			}
		},
	})

	// Wait for events to be processed
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		count := atomic.LoadInt32(&eventsB)
		if count >= 1000 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	finalCount := atomic.LoadInt32(&eventsB)
	if finalCount == 0 {
		t.Error("BUG: Config-B didn't receive ANY events with scale")
		t.Log("This could be the issue - scale prevents synthetic events")
	} else if finalCount < 1000 {
		t.Errorf("Config-B only received %d events out of 1000", finalCount)
		t.Log("Partial delivery could indicate buffering issues")
	} else {
		t.Logf("Config-B received all %d events", finalCount)
		if !firstEventTime.IsZero() {
			t.Logf("Time to first event: %v", firstEventTime.Sub(startTime))
		}
	}
}

// TestMemoryPressure tests behavior under memory pressure
func TestMemoryPressure(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Force garbage collection to simulate memory pressure
	goruntime.GC()
	goruntime.GC()

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

	// Allocate memory to create pressure
	var memoryHog [][]byte
	for i := 0; i < 100; i++ {
		memoryHog = append(memoryHog, make([]byte, 1024*1024)) // 1MB chunks
		goruntime.GC()
	}

	// Now start config-b under memory pressure
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	received := false
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			received = true
		},
	})

	// More GC pressure
	goruntime.GC()
	goruntime.GC()

	time.Sleep(500 * time.Millisecond)

	// Free memory
	memoryHog = nil
	goruntime.GC()

	if !received {
		t.Error("Config-B didn't receive event under memory pressure")
		t.Log("Memory pressure might affect synthetic event delivery")
	}
}

// TestConcurrentModification tests if concurrent modifications affect synthetic events
func TestConcurrentModification(t *testing.T) {
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
			ResourceVersion: "1",
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
		UpdateFunc: func(old, new interface{}) {
			t.Log("Config-A saw update")
		},
	})

	// Start goroutine that continuously modifies the service
	go func() {
		for i := 0; i < 10; i++ {
			time.Sleep(50 * time.Millisecond)
			serviceB.Annotations = map[string]string{"iteration": fmt.Sprintf("%d", i)}
			serviceB.ResourceVersion = fmt.Sprintf("%d", i+2)
			_, _ = fakeClient.CoreV1().Services("default").Update(context.Background(), serviceB, metav1.UpdateOptions{})
		}
	}()

	// While modifications are happening, start config-b
	time.Sleep(100 * time.Millisecond)

	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	events := []string{}
	var mu sync.Mutex

	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			events = append(events, "add")
			mu.Unlock()
			t.Log("Config-B received Add")
		},
		UpdateFunc: func(old, new interface{}) {
			mu.Lock()
			events = append(events, "update")
			mu.Unlock()
			t.Log("Config-B received Update")
		},
	})

	// Wait for events
	time.Sleep(1 * time.Second)

	mu.Lock()
	defer mu.Unlock()

	if len(events) == 0 {
		t.Error("Config-B didn't receive any events during concurrent modifications")
		t.Log("Concurrent modifications might prevent synthetic events")
	} else {
		t.Logf("Config-B received %d events: %v", len(events), events)
		hasAdd := false
		for _, e := range events {
			if e == "add" {
				hasAdd = true
				break
			}
		}
		if !hasAdd {
			t.Error("Config-B didn't receive initial Add event, only updates")
		}
	}
}
