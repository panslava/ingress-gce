package filteredinformer

import (
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/ingress-gce/pkg/flags"
)

// TestServiceWithAnnotations tests if services with NEG annotations are processed
func TestServiceWithAnnotations(t *testing.T) {
	// Set up the provider config label key
	originalKey := flags.F.ProviderConfigNameLabelKey
	flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
	defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

	// Create a service with NEG annotations (like production services)
	serviceB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "service-b",
			Namespace: "default",
			Labels: map[string]string{
				"cloud.gke.io/provider-config-name": "config-b",
			},
			Annotations: map[string]string{
				"cloud.google.com/neg": `{"exposed_ports": {"80": {}}}`,
			},
		},
		Spec: v1.ServiceSpec{
			Type: v1.ServiceTypeLoadBalancer,
			Ports: []v1.ServicePort{
				{
					Name:       "http",
					Port:       80,
					TargetPort: intstr.FromInt(8080),
					Protocol:   v1.ProtocolTCP,
				},
			},
			Selector: map[string]string{
				"app": "test",
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
	
	eventsA := 0
	filteredA.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eventsA++
			svc := obj.(*v1.Service)
			t.Logf("Config-A received: %s", svc.Name)
		},
	})

	time.Sleep(100 * time.Millisecond)

	// Start config-b (base already running)
	filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
	go filteredB.Run(stopCh)

	eventsB := 0
	filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			eventsB++
			svc := obj.(*v1.Service)
			t.Logf("Config-B received: %s (annotations: %v)", svc.Name, svc.Annotations)
		},
	})

	time.Sleep(200 * time.Millisecond)

	if eventsA != 0 {
		t.Errorf("Config-A should not receive any events, got %d", eventsA)
	}
	if eventsB == 0 {
		t.Error("Config-B didn't receive the service with NEG annotations")
	}

	// Check if the service can be retrieved from lister
	lister := filteredB.GetIndexer()
	obj, exists, err := lister.GetByKey("default/service-b")
	if err != nil {
		t.Errorf("GetByKey error: %v", err)
	}
	if !exists {
		t.Error("Service doesn't exist in lister")
	}
	if obj != nil {
		svc := obj.(*v1.Service)
		if svc.Annotations == nil || svc.Annotations["cloud.google.com/neg"] == "" {
			t.Error("Service in lister lost its annotations")
		}
	}
}

// TestServiceProcessingConditions tests various conditions that might prevent processing
func TestServiceProcessingConditions(t *testing.T) {
	testCases := []struct {
		name        string
		service     *v1.Service
		shouldProcess bool
	}{
		{
			name: "LoadBalancer with NEG annotation",
			service: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "lb-with-neg",
					Namespace: "default",
					Labels: map[string]string{
						"cloud.gke.io/provider-config-name": "config-b",
					},
					Annotations: map[string]string{
						"cloud.google.com/neg": `{"exposed_ports": {"80": {}}}`,
					},
				},
				Spec: v1.ServiceSpec{
					Type: v1.ServiceTypeLoadBalancer,
					Ports: []v1.ServicePort{{Port: 80}},
				},
			},
			shouldProcess: true,
		},
		{
			name: "ClusterIP with NEG annotation",
			service: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "clusterip-with-neg",
					Namespace: "default",
					Labels: map[string]string{
						"cloud.gke.io/provider-config-name": "config-b",
					},
					Annotations: map[string]string{
						"cloud.google.com/neg": `{"ingress": true}`,
					},
				},
				Spec: v1.ServiceSpec{
					Type: v1.ServiceTypeClusterIP,
					Ports: []v1.ServicePort{{Port: 80}},
				},
			},
			shouldProcess: true,
		},
		{
			name: "Service without NEG annotation",
			service: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "no-neg",
					Namespace: "default",
					Labels: map[string]string{
						"cloud.gke.io/provider-config-name": "config-b",
					},
				},
				Spec: v1.ServiceSpec{
					Type: v1.ServiceTypeClusterIP,
					Ports: []v1.ServicePort{{Port: 80}},
				},
			},
			shouldProcess: true, // Still should receive event, NEG controller decides
		},
		{
			name: "Service with wrong provider config",
			service: &v1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "wrong-config",
					Namespace: "default",
					Labels: map[string]string{
						"cloud.gke.io/provider-config-name": "config-a",
					},
					Annotations: map[string]string{
						"cloud.google.com/neg": `{"exposed_ports": {"80": {}}}`,
					},
				},
				Spec: v1.ServiceSpec{
					Type: v1.ServiceTypeLoadBalancer,
					Ports: []v1.ServicePort{{Port: 80}},
				},
			},
			shouldProcess: false, // Wrong provider config label
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Set up
			originalKey := flags.F.ProviderConfigNameLabelKey
			flags.F.ProviderConfigNameLabelKey = "cloud.gke.io/provider-config-name"
			defer func() { flags.F.ProviderConfigNameLabelKey = originalKey }()

			fakeClient := fake.NewSimpleClientset(tc.service)
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

			time.Sleep(50 * time.Millisecond)

			// Start config-b
			filteredB := NewProviderConfigFilteredInformer(baseInformer, "config-b")
			go filteredB.Run(stopCh)

			received := false
			filteredB.AddEventHandler(cache.ResourceEventHandlerFuncs{
				AddFunc: func(obj interface{}) {
					received = true
					svc := obj.(*v1.Service)
					t.Logf("Received: %s", svc.Name)
				},
			})

			time.Sleep(200 * time.Millisecond)

			if tc.shouldProcess && !received {
				t.Errorf("Expected to receive %s but didn't", tc.name)
			}
			if !tc.shouldProcess && received {
				t.Errorf("Should not have received %s but did", tc.name)
			}
		})
	}
}