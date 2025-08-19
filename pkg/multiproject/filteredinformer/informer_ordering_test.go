package filteredinformer

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// helper to wait until a condition is met or timeout
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

// Verifies that when handlers are registered BEFORE starting the (filtered) informer,
// initial Add events are observed for matching objects.
func TestInformerOrdering_HandlerBeforeStart(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.pc"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "svc-a", Labels: map[string]string{"test.pc": "pc-a"}}}
	client := fake.NewSimpleClientset(svc)
	factory := informers.NewSharedInformerFactory(client, 0)

	base := factory.Core().V1().Services().Informer()
	filtered := NewProviderConfigFilteredInformer(base, "pc-a")

	var (
		mu   sync.Mutex
		adds []string
	)

	_, err := filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			s := obj.(*corev1.Service)
			mu.Lock()
			adds = append(adds, s.Name)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("failed to add handler: %v", err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go filtered.Run(stop)
	if !cache.WaitForCacheSync(stop, filtered.HasSynced) {
		t.Fatalf("failed to sync filtered informer")
	}

	waitUntil(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(adds) == 1 && adds[0] == "svc-a"
	})
}

// Verifies that when the base informer is ALREADY running and a handler is registered
// later through the provider-config filtered wrapper, a synthetic Add is still delivered
// for matching existing objects.
func TestInformerOrdering_HandlerAfterBaseStarted(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "test.pc"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	client := fake.NewSimpleClientset()
	factory := informers.NewSharedInformerFactory(client, 0)
	base := factory.Core().V1().Services().Informer()

	// Start base informer first
	stop := make(chan struct{})
	defer close(stop)
	go base.Run(stop)
	if !cache.WaitForCacheSync(stop, base.HasSynced) {
		t.Fatalf("failed to sync base informer")
	}

	// Create an object BEFORE adding the filtered handler
	_, err := client.CoreV1().Services("default").Create(
		context.Background(),
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "svc-b", Labels: map[string]string{"test.pc": "pc-b"}}},
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("failed to create service: %v", err)
	}

	filtered := NewProviderConfigFilteredInformer(base, "pc-b")

	var (
		mu   sync.Mutex
		adds []string
	)
	_, err = filtered.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			s := obj.(*corev1.Service)
			mu.Lock()
			adds = append(adds, s.Name)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("failed to add handler: %v", err)
	}

	// Running the filtered informer is safe even if base is already running
	go filtered.Run(stop)

	// We don't strictly need HasSynced here, but it doesn't hurt
	if !cache.WaitForCacheSync(stop, filtered.HasSynced) {
		t.Fatalf("failed to sync filtered informer")
	}

	waitUntil(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(adds) >= 1 && adds[0] == "svc-b"
	})
}
