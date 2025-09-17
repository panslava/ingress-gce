package filteredinformer

import (
	"context"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestBaseInformerAlreadyStartedByOtherFiltered tests a specific scenario:
// 1. Filtered informer A starts the base informer
// 2. Base informer processes events
// 3. Much later, filtered informer B is created
// 4. Does B get synthetic events?
func TestBaseInformerAlreadyStartedByOtherFiltered(t *testing.T) {
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Service exists from the beginning
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

	// Step 1: Filtered A starts the base informer (NOT directly!)
	t.Log("Step 1: Starting filtered informer A")
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	
	// This is key: filtered A calls Run, which starts the base
	go filteredA.Run(stopCh)
	
	// Wait for sync
	if !cache.WaitForCacheSync(stopCh, filteredA.HasSynced) {
		t.Fatal("Failed to sync filtered A")
	}
	
	// Add handler for A
	eventsA := 0
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eventsA++
			svc := obj.(*v1.Service)
			t.Logf("Config-A received: %s", svc.Name)
		},
	})
	
	// Let it run for a bit
	time.Sleep(200 * time.Millisecond)
	
	// Verify A didn't get service-b (wrong label)
	if eventsA != 0 {
		t.Errorf("Config-A shouldn't have received any events, got %d", eventsA)
	}
	
	// Step 2: Much later, filtered B is created
	t.Log("Step 2: Starting filtered informer B (base already running)")
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Check state before Run
	hasSyncedBeforeRun := filteredB.HasSynced()
	t.Logf("Config-B HasSynced before Run: %v", hasSyncedBeforeRun)
	
	// Try to run (should be no-op since base is already running)
	go filteredB.Run(stopCh)
	
	// Check state after Run
	hasSyncedAfterRun := filteredB.HasSynced()
	t.Logf("Config-B HasSynced after Run: %v", hasSyncedAfterRun)
	
	// Add handler for B
	eventsB := 0
	var eventDetails []string
	var mu sync.Mutex
	
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			eventsB++
			svc := obj.(*v1.Service)
			eventDetails = append(eventDetails, "add:"+svc.Name)
			mu.Unlock()
			t.Logf("Config-B received Add: %s", svc.Name)
		},
	})
	
	// Wait for events
	time.Sleep(500 * time.Millisecond)
	
	// Check results
	mu.Lock()
	if eventsB == 0 {
		t.Error("BUG: Config-B didn't receive synthetic event")
		t.Log("This might be the issue - when filtered A starts base, filtered B doesn't get events")
	} else {
		t.Logf("Config-B received %d events: %v", eventsB, eventDetails)
	}
	mu.Unlock()
}

// TestFilteringLogicEdgeCases tests edge cases in the filtering logic
func TestFilteringLogicEdgeCases(t *testing.T) {
	testCases := []struct {
		name               string
		labelKey           string
		providerConfigName string
		serviceLabels      map[string]string
		shouldMatch        bool
	}{
		{
			name:               "Exact match",
			labelKey:           "cloud.gke.io/provider-config-name",
			providerConfigName: "config-a",
			serviceLabels:      map[string]string{"cloud.gke.io/provider-config-name": "config-a"},
			shouldMatch:        true,
		},
		{
			name:               "No match",
			labelKey:           "cloud.gke.io/provider-config-name",
			providerConfigName: "config-a",
			serviceLabels:      map[string]string{"cloud.gke.io/provider-config-name": "config-b"},
			shouldMatch:        false,
		},
		{
			name:               "Missing label",
			labelKey:           "cloud.gke.io/provider-config-name",
			providerConfigName: "config-a",
			serviceLabels:      map[string]string{},
			shouldMatch:        false,
		},
		{
			name:               "Empty provider config name",
			labelKey:           "cloud.gke.io/provider-config-name",
			providerConfigName: "",
			serviceLabels:      map[string]string{"cloud.gke.io/provider-config-name": ""},
			shouldMatch:        true,
		},
		{
			name:               "Case sensitive",
			labelKey:           "cloud.gke.io/provider-config-name",
			providerConfigName: "Config-A",
			serviceLabels:      map[string]string{"cloud.gke.io/provider-config-name": "config-a"},
			shouldMatch:        false,
		},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set up
			originalKey := flags.F.ProviderConfigNameLabelKey
			flags.F.ProviderConfigNameLabelKey = tc.labelKey
			defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()
			
			service := &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-service",
					Namespace: "default",
					Labels:    tc.serviceLabels,
				},
			}
			
			// Test the filtering
			result := isObjectInProviderConfig(service, tc.providerConfigName)
			if result != tc.shouldMatch {
				t.Errorf("Expected %v but got %v for labels %v with provider config %s",
					tc.shouldMatch, result, tc.serviceLabels, tc.providerConfigName)
			}
		})
	}
}

// TestEventHandlerRegistrationOrder tests if the order of handler registration matters
func TestEventHandlerRegistrationOrder(t *testing.T) {
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

	// Start base informer directly (not through filtered)
	go baseInformer.Run(stopCh)
	cache.WaitForCacheSync(stopCh, baseInformer.HasSynced)
	
	// Add multiple handlers in different orders
	var results []string
	var mu sync.Mutex
	
	// Handler 1: Direct on base
	baseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			results = append(results, "base-direct")
			mu.Unlock()
		},
	})
	
	// Handler 2: Through filtered A
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			results = append(results, "filtered-a")
			mu.Unlock()
		},
	})
	
	// Handler 3: Through filtered B
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			mu.Lock()
			results = append(results, "filtered-b")
			mu.Unlock()
		},
	})
	
	// Wait for synthetic events
	time.Sleep(200 * time.Millisecond)
	
	// Check results
	mu.Lock()
	defer mu.Unlock()
	
	t.Logf("Handlers that received events: %v", results)
	
	// base-direct should get it (no filter)
	foundBase := false
	foundB := false
	for _, r := range results {
		if r == "base-direct" {
			foundBase = true
		}
		if r == "filtered-b" {
			foundB = true
		}
	}
	
	if !foundBase {
		t.Error("Base handler didn't receive synthetic event")
	}
	if !foundB {
		t.Error("Filtered-B handler didn't receive synthetic event")
	}
}

// TestInformerInternalStateDeeper examines the internal state more deeply
func TestInformerInternalStateDeeper(t *testing.T) {
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

	// Use filtered A to start the base
	filteredA := NewProviderConfigFilteredInformer(baseInformer, "config-a")
	go filteredA.Run(stopCh)
	cache.WaitForCacheSync(stopCh, filteredA.HasSynced)
	
	// Add handler to A
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			t.Log("Config-A received event")
		},
	})
	
	// Create new service dynamically
	ctx := context.Background()
	newService := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "dynamic-service",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
		},
	}
	_, err := fakeClient.CoreV1().Services("default").Create(ctx, newService, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create service: %v", err)
	}
	
	// Give time for the create to propagate
	time.Sleep(200 * time.Millisecond)
	
	// Now add filtered B
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	
	// Check what's in the base cache
	baseStore := baseInformer.GetStore()
	baseItems := baseStore.List()
	t.Logf("Base cache has %d items", len(baseItems))
	for _, item := range baseItems {
		if svc, ok := item.(*v1.Service); ok {
			t.Logf("  - %s (labels: %v)", svc.Name, svc.Labels)
		}
	}
	
	// Check what filtered B sees
	filteredStore := filteredB.GetStore()
	filteredItems := filteredStore.List()
	t.Logf("Filtered-B cache has %d items", len(filteredItems))
	for _, item := range filteredItems {
		if svc, ok := item.(*v1.Service); ok {
			t.Logf("  - %s", svc.Name)
		}
	}
	
	// Add handler to B
	eventsB := 0
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eventsB++
			svc := obj.(*v1.Service)
			t.Logf("Config-B received: %s", svc.Name)
		},
	})
	
	// Try to run B (should be no-op)
	go filteredB.Run(stopCh)
	
	// Wait for events
	time.Sleep(500 * time.Millisecond)
	
	if eventsB != 2 {
		t.Errorf("Config-B should have received 2 events (service-b and dynamic-service), got %d", eventsB)
		if eventsB == 0 {
			t.Error("This could be the bug - no synthetic events when base started by another filtered informer")
		}
	}
}