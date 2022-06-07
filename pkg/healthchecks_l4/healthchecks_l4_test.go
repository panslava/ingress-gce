package healthchecks_l4

import (
	"reflect"
	"sync"
	"testing"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/test"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/legacy-cloud-providers/gce"
)

func NewL4TestHealthChecks(cloud *gce.Cloud, service *corev1.Service, namer *namer.L4Namer) *l4HealthChecks {
	lock := &sync.Mutex{}
	recorder := record.NewFakeRecorder(100)

	return newL4HealthChecks(lock, cloud, service, recorder, namer, utils.ILB)
}

func TestNewL4HealthChecks(t *testing.T) {
	t.Parallel()

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))

	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	testCases := []struct {
		service                        *corev1.Service
		expectedHCName                 string
		expectedFWName                 string
		expectedScope                  meta.KeyType
		expectedIsClusterTrafficPolicy bool
		expectedL4Type                 utils.L4LBType
		expectedPort                   int32
		expectedPath                   string
		desc                           string
	}{
		{
			service:                        rbsService,
			expectedHCName:                 l4namer.ClusterPolicyHealthCheck(),
			expectedFWName:                 l4namer.ClusterPolicyHealthCheckFirewallRule(),
			expectedScope:                  meta.Regional,
			expectedIsClusterTrafficPolicy: true,
			expectedL4Type:                 utils.XLB,
			expectedPort:                   10256,
			expectedPath:                   "/healthz",
			desc:                           "Check RBS Cluster Policy service health check",
		},
		{
			service:                        rbsLocalService,
			expectedHCName:                 l4namer.LocalPolicyHealthCheck(rbsLocalService.Namespace, rbsLocalService.Name),
			expectedFWName:                 l4namer.LocalPolicyHealthCheckFirewallRule(rbsLocalService.Namespace, rbsLocalService.Name),
			expectedScope:                  meta.Regional,
			expectedIsClusterTrafficPolicy: false,
			expectedL4Type:                 utils.XLB,
			expectedPort:                   0,
			expectedPath:                   "",
			desc:                           "Check RBS Local Policy service health check",
		},
		{
			service:                        ilbService,
			expectedHCName:                 l4namer.ClusterPolicyHealthCheck(),
			expectedFWName:                 l4namer.ClusterPolicyHealthCheckFirewallRule(),
			expectedScope:                  meta.Global,
			expectedIsClusterTrafficPolicy: true,
			expectedL4Type:                 utils.ILB,
			expectedPort:                   10256,
			expectedPath:                   "/healthz",
			desc:                           "Check ILB Cluster Policy service health check",
		},
		{
			service:                        ilbLocalService,
			expectedHCName:                 l4namer.LocalPolicyHealthCheck(ilbLocalService.Namespace, ilbLocalService.Name),
			expectedFWName:                 l4namer.LocalPolicyHealthCheckFirewallRule(ilbLocalService.Namespace, ilbLocalService.Name),
			expectedScope:                  meta.Global,
			expectedIsClusterTrafficPolicy: false,
			expectedL4Type:                 utils.ILB,
			expectedPort:                   0,
			expectedPath:                   "",
			desc:                           "Check ILB Local Policy service health check",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			fakeCloud := gce.NewFakeGCECloud(vals)

			healthChecksController := NewL4TestHealthChecks(fakeCloud, tc.service, l4namer)

			if healthChecksController.hcParams.healthCheckName != tc.expectedHCName {
				t.Errorf("Wrong health check name, expected: %s, got %s", tc.expectedHCName, healthChecksController.hcParams.healthCheckName)
			}
			if healthChecksController.hcParams.scope != tc.expectedScope {
				t.Errorf("Wrong health check scope, expected: %s, got %s", tc.expectedScope, healthChecksController.hcParams.scope)
			}
			if healthChecksController.hcFWName != tc.expectedFWName {
				t.Errorf("Wrong health check firewall name, expected: %s, got %s", tc.expectedFWName, healthChecksController.hcFWName)
			}
			if healthChecksController.hcParams.isClusterTrafficPolicy != tc.expectedIsClusterTrafficPolicy {
				t.Errorf("Wrong isClusterTrafficPolicy, expected: %t, got %t", tc.expectedIsClusterTrafficPolicy, healthChecksController.hcParams.isClusterTrafficPolicy)
			}
			if healthChecksController.hcParams.l4Type != tc.expectedL4Type {
				t.Errorf("Wrong l4Type, expected: %v, got %v", tc.expectedL4Type, healthChecksController.hcParams.l4Type)
			}
			if healthChecksController.hcParams.path != tc.expectedPath {
				t.Errorf("Wrong path, expected: %s, got %s", tc.expectedPath, healthChecksController.hcParams.path)
			}
			if healthChecksController.hcParams.port != tc.expectedPort {
				t.Errorf("Wrong port, expected: %d, got %d", tc.expectedPort, healthChecksController.hcParams.port)
			}
		})
	}
}

func TestCreateNewL4HealthCheck(t *testing.T) {
	t.Parallel()

	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))

	testCases := []struct {
		service        *corev1.Service
		expectedHCName string
		expectedScope  meta.KeyType
		desc           string
	}{
		{
			service:        rbsService,
			expectedHCName: l4namer.ClusterPolicyHealthCheck(),
			expectedScope:  meta.Regional,
			desc:           "Test RBS Cluster Policy service health check creation",
		},
		{
			service:        rbsLocalService,
			expectedHCName: l4namer.LocalPolicyHealthCheck(rbsLocalService.Namespace, rbsLocalService.Name),
			expectedScope:  meta.Regional,
			desc:           "Test RBS Local Policy service health check creation",
		},
		{
			service:        ilbService,
			expectedHCName: l4namer.ClusterPolicyHealthCheck(),
			expectedScope:  meta.Global,
			desc:           "Test ILB Cluster Policy service health check creation",
		},
		{
			service:        ilbLocalService,
			expectedHCName: l4namer.LocalPolicyHealthCheck(ilbLocalService.Namespace, ilbLocalService.Name),
			expectedScope:  meta.Global,
			desc:           "Test ILB Local Policy service health check creation",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			fakeCloud := gce.NewFakeGCECloud(vals)

			healthChecksController := NewL4TestHealthChecks(fakeCloud, tc.service, l4namer)
			createdHealthCheck, err := healthChecksController.createNewHealthCheck()
			if err != nil {
				t.Fatalf("healthChecksController.createNewHealthCheck() returned error %v, want nil", err)
			}
			if createdHealthCheck == nil {
				t.Errorf("expected non nil createdHealthCheck")
			}

			key, err := composite.CreateKey(fakeCloud, tc.expectedHCName, tc.expectedScope)
			if err != nil {
				t.Fatalf("composite.CreateKey(_, %s, %s) returned error: %v, want nil", tc.expectedHCName, tc.expectedScope, err)
			}

			cloudHC, err := composite.GetHealthCheck(fakeCloud, key, meta.VersionGA)
			if err != nil {
				t.Errorf("composite.GetHealthCheck(_, %s, %s) returned error %v, want nil", key, meta.VersionGA, err)
			}
			controllerHC, err := healthChecksController.getExistingHealthCheck()
			if err != nil {
				t.Errorf("healthChecksController.getExistingHealthCheck() returned error %v, want nil", err)
			}

			if !reflect.DeepEqual(cloudHC, controllerHC) {
				t.Errorf("Cloud health check = %v, not equal to controller HC = %v", cloudHC, controllerHC)
			}
		})
	}
}

func TestEnsureHealthCheck(t *testing.T) {
	t.Parallel()

	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))

	testCases := []struct {
		service *corev1.Service
		desc    string
	}{
		{
			service: rbsService,
			desc:    "Test RBS Cluster Policy service ensure health check",
		},

		{
			service: rbsLocalService,
			desc:    "Test RBS Local Policy service ensure health check",
		},

		{
			service: ilbService,
			desc:    "Test ILB Cluster Policy service ensure health check",
		},

		{
			service: ilbLocalService,
			desc:    "Test ILB Local Policy service ensure health check",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			fakeCloud := gce.NewFakeGCECloud(vals)

			healthChecksController := NewL4TestHealthChecks(fakeCloud, tc.service, l4namer)

			_, err := healthChecksController.ensureHealthCheck()
			if err != nil {
				t.Errorf("healthChecksController.ensureHealthCheck() returned error %v, want nil", err)
			}

			healthCheck, err := healthChecksController.getExistingHealthCheck()
			if err != nil {
				t.Errorf("healthChecksController.getExistingHealthCheck() returned error %v, want nil", err)
			}

			nonUpdatedHealthCheck, err := healthChecksController.ensureHealthCheck()
			if err != nil {
				t.Errorf("ealthChecksController.ensureHealthCheck() returned error %v, want nil", err)
			}

			if !reflect.DeepEqual(healthCheck, nonUpdatedHealthCheck) {
				t.Errorf("Health Check should not change, if executing ensureHealthCheck twice, oldHealthCheck %v, newHealthCheck %v", healthCheck, nonUpdatedHealthCheck)
			}
		})
	}
}

func TestEnsureHealthCheckFirewall(t *testing.T) {
	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))

	testCases := []struct {
		service        *corev1.Service
		expectedFWName string
		desc           string
	}{
		{
			service:        rbsService,
			expectedFWName: l4namer.ClusterPolicyHealthCheckFirewallRule(),
			desc:           "Test ensure health check firewall for RBS Cluster Policy service",
		},
		{
			service:        rbsLocalService,
			expectedFWName: l4namer.LocalPolicyHealthCheckFirewallRule(rbsLocalService.Namespace, rbsLocalService.Name),
			desc:           "Test ensure health check firewall for RBS Local Policy service",
		},
		{
			service:        ilbService,
			expectedFWName: l4namer.ClusterPolicyHealthCheckFirewallRule(),
			desc:           "Test ensure health check firewall for ILB Cluster Policy service",
		},
		{
			service:        ilbLocalService,
			expectedFWName: l4namer.LocalPolicyHealthCheckFirewallRule(ilbLocalService.Namespace, ilbLocalService.Name),
			desc:           "Test ensure health check firewall for ILB Local Policy service",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			fakeCloud := gce.NewFakeGCECloud(vals)
			healthChecksController := NewL4TestHealthChecks(fakeCloud, tc.service, l4namer)

			nodeNames := []string{"instance-1", "instance-2"}
			_, err := test.CreateAndInsertNodes(fakeCloud, nodeNames, vals.ZoneName)
			if err != nil {
				t.Errorf("test.CreateAndInsertNodes(_, %v, %s) returned error %v, want nil", nodeNames, vals.ZoneName, err)
			}

			err = healthChecksController.ensureHealthCheckFirewall(nodeNames)
			if err != nil {
				t.Errorf("healthChecksController.ensureHealthCheckFirewall(%v) returned error %v, want nil", nodeNames, err)
			}

			_, err = fakeCloud.GetFirewall(tc.expectedFWName)
			if err != nil {
				t.Errorf("fakeCloud.GetFirewall(%s) returned error %v, want nil", tc.expectedFWName, err)
			}
		})
	}
}

func TestEnsureAndDeleteHealthChecksWithFirewalls(t *testing.T) {
	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))

	testCases := []struct {
		service *corev1.Service
		desc    string
	}{
		{
			service: rbsService,
			desc:    "Test RBS Cluster Policy service ensure and delete health check with firewall",
		},

		{
			service: rbsLocalService,
			desc:    "Test RBS Local Policy service ensure and delete health check with firewall",
		},

		{
			service: ilbService,
			desc:    "Test ILB Cluster Policy service ensure and delete health check with firewall",
		},

		{
			service: ilbLocalService,
			desc:    "Test ILB Local Policy service ensure and delete health check with firewall",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			fakeCloud := gce.NewFakeGCECloud(vals)
			healthChecksController := NewL4TestHealthChecks(fakeCloud, tc.service, l4namer)

			nodeNames := []string{"instance-1", "instance-2"}
			_, err := test.CreateAndInsertNodes(fakeCloud, nodeNames, vals.ZoneName)
			if err != nil {
				t.Errorf("test.CreateAndInsertNodes(_, %v, %s) returned error %v, want nil", nodeNames, vals.ZoneName, err)
			}

			_, err = healthChecksController.EnsureHealthCheckWithFirewall(nodeNames)
			if err != nil {
				t.Errorf("healthChecksController.EnsureHealthCheckWithFirewall(%v) returned error %v, want nil", nodeNames, err)
			}

			_, err = fakeCloud.GetFirewall(healthChecksController.hcFWName)
			if err != nil {
				t.Errorf("fakeCloud.GetFirewall(%s) returned error %v, want nil", healthChecksController.hcFWName, err)
			}

			_, err = healthChecksController.getExistingHealthCheck()
			if err != nil {
				t.Errorf("healthChecksController.getExistingHealthCheck() returned error %v, want nil", err)
			}

			err = healthChecksController.DeleteHealthChecksWithFirewalls()
			if err != nil {
				t.Errorf("healthChecksController.DeleteHealthChecksWithFirewalls() returned error %v, want nil", err)
			}

			key, err := composite.CreateKey(fakeCloud, healthChecksController.hcParams.healthCheckName, healthChecksController.hcParams.scope)
			if err != nil {
				t.Errorf("composite.CreateKey(_, %s, %s) returned error: %v, want nil", healthChecksController.hcParams.healthCheckName, healthChecksController.hcParams.scope, err)
			}

			hc, err := composite.GetHealthCheck(fakeCloud, key, meta.VersionGA)
			if err == nil || !utils.IsNotFoundError(err) {
				t.Errorf("Expected to get not found health check error, got error %v, healthcheck %v", err, hc)
			}

			fw, err := fakeCloud.GetFirewall(healthChecksController.hcFWName)
			if err == nil || !utils.IsNotFoundError(err) {
				t.Errorf("Expected to get not found firewall error, got error %v, firewall %v", err, fw)
			}
		})
	}
}

func TestExistsOtherLBTypeClusterHealthCheck(t *testing.T) {
	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))
	fakeCloud := gce.NewFakeGCECloud(vals)

	nodeNames := []string{"instance-1", "instance-2"}
	_, err := test.CreateAndInsertNodes(fakeCloud, nodeNames, vals.ZoneName)
	if err != nil {
		t.Errorf("test.CreateAndInsertNodes(_, %v, %s) returned error %v, want nil", nodeNames, vals.ZoneName, err)
	}

	rbsHealthChecksController := NewL4TestHealthChecks(fakeCloud, rbsService, l4namer)
	rbsLocalHealthChecksController := NewL4TestHealthChecks(fakeCloud, rbsLocalService, l4namer)
	ilbHealthChecksController := NewL4TestHealthChecks(fakeCloud, ilbService, l4namer)
	ilbLocalHealthChecksController := NewL4TestHealthChecks(fakeCloud, ilbLocalService, l4namer)

	controllers := []*l4HealthChecks{rbsHealthChecksController, rbsLocalHealthChecksController, ilbHealthChecksController, ilbLocalHealthChecksController}

	for _, controller := range controllers {
		exists, err := controller.existsOtherLBTypeClusterHealthCheck()
		if err != nil {
			t.Errorf("controller.existsOtherLBTypeClusterHealthCheck() returned error %v, want nil", err)
		}
		if exists {
			t.Errorf("existsOtherLBTypeClusterHealthCheck() should return false before creating any health checks")
		}
	}

	_, err = ilbLocalHealthChecksController.EnsureHealthCheckWithFirewall(nodeNames)
	if err != nil {
		t.Errorf("ilbLocalHealthChecksController.EnsureHealthCheckWithFirewall(%v) returned erorr %v, want nil", nodeNames, err)
	}
	_, err = rbsLocalHealthChecksController.EnsureHealthCheckWithFirewall(nodeNames)
	if err != nil {
		t.Errorf("rbsLocalHealthChecksController.EnsureHealthCheckWithFirewall(%v) returned erorr %v, want nil", nodeNames, err)
	}

	for _, controller := range controllers {
		exists, err := controller.existsOtherLBTypeClusterHealthCheck()
		if err != nil {
			t.Errorf("controller.existsOtherLBTypeClusterHealthCheck() returned error %v, want nil", err)
		}
		if exists {
			t.Errorf("existsOtherLBTypeClusterHealthCheck() should return false after creating local health checks")
		}
	}

	_, err = rbsHealthChecksController.EnsureHealthCheckWithFirewall(nodeNames)
	if err != nil {
		t.Errorf("rbsHealthChecksController.EnsureHealthCheckWithFirewall(%v) returned erorr %v, want nil", nodeNames, err)
	}
	exists, err := rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck()
	if err != nil {
		t.Errorf("rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck() returned error %v, want nil", err)
	}
	if exists {
		t.Errorf("rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck() should return false after creating only rbs health check")
	}

	exists, err = ilbHealthChecksController.existsOtherLBTypeClusterHealthCheck()
	if err != nil {
		t.Errorf("ilbHealthChecksController.existsOtherLBTypeClusterHealthCheck() returned error %v, want nil", err)
	}
	if !exists {
		t.Errorf("ilbHealthChecksController.existsOtherLBTypeClusterHealthCheck() should return true, after creating rbs health check")
	}

	_, err = ilbHealthChecksController.EnsureHealthCheckWithFirewall(nodeNames)
	if err != nil {
		t.Errorf("ilbHealthChecksController.EnsureHealthCheckWithFirewall(%v) returned erorr %v, want nil", nodeNames, err)
	}
	exists, err = rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck()
	if err != nil {
		t.Errorf("rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck() returned error %v, want nil", err)
	}
	if !exists {
		t.Errorf("rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck() should return true, after creating ilb health check")
	}

	err = ilbHealthChecksController.DeleteHealthChecksWithFirewalls()
	if err != nil {
		t.Errorf("ilbHealthChecksController.DeleteHealthChecksWithFirewalls() returned error %v, want nil", err)
	}

	exists, err = rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck()
	if err != nil {
		t.Errorf("rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck() returned error %v, want nil", err)
	}
	if exists {
		t.Errorf("rbsHealthChecksController.existsOtherLBTypeClusterHealthCheck() should return false, after deleting ilb health check")
	}
}

func TestGetHealthCheckName(t *testing.T) {
	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))

	testCases := []struct {
		service        *corev1.Service
		expectedHCName string
	}{
		{
			service:        ilbService,
			expectedHCName: l4namer.ClusterPolicyHealthCheck(),
		},
		{
			service:        ilbLocalService,
			expectedHCName: l4namer.LocalPolicyHealthCheck(ilbLocalService.Namespace, ilbLocalService.Name),
		},
		{
			service:        rbsService,
			expectedHCName: l4namer.ClusterPolicyHealthCheck(),
		},
		{
			service:        rbsLocalService,
			expectedHCName: l4namer.LocalPolicyHealthCheck(rbsLocalService.Namespace, rbsLocalService.Name),
		},
	}

	for _, tc := range testCases {
		fakeCloud := gce.NewFakeGCECloud(vals)
		healthChecksController := NewL4TestHealthChecks(fakeCloud, tc.service, l4namer)

		hcName := healthChecksController.GetHealthCheckName()
		if hcName != tc.expectedHCName {
			t.Errorf("Wrong health check name, expected %s, got %s", tc.expectedHCName, hcName)
		}
	}
}

func TestGetHealthCheckFirewallName(t *testing.T) {
	rbsService := test.NewL4NetLBRBSService(8080)
	rbsLocalService := test.NewL4NetLBLocalPolicyRBSService(8080)
	ilbService := test.NewL4ILBService(false, 8080)
	ilbLocalService := test.NewL4ILBService(true, 8080)

	vals := gce.DefaultTestClusterValues()
	l4namer := namer.NewL4Namer("aaaaa", namer.NewNamer(vals.ClusterName, "cluster-fw"))

	testCases := []struct {
		service          *corev1.Service
		expectedHCFWName string
	}{
		{
			service:          ilbService,
			expectedHCFWName: l4namer.ClusterPolicyHealthCheckFirewallRule(),
		},
		{
			service:          ilbLocalService,
			expectedHCFWName: l4namer.LocalPolicyHealthCheckFirewallRule(ilbLocalService.Namespace, ilbLocalService.Name),
		},
		{
			service:          rbsService,
			expectedHCFWName: l4namer.ClusterPolicyHealthCheckFirewallRule(),
		},
		{
			service:          rbsLocalService,
			expectedHCFWName: l4namer.LocalPolicyHealthCheckFirewallRule(rbsLocalService.Namespace, rbsLocalService.Name),
		},
	}

	for _, tc := range testCases {
		fakeCloud := gce.NewFakeGCECloud(vals)
		healthChecksController := NewL4TestHealthChecks(fakeCloud, tc.service, l4namer)

		hcFWName := healthChecksController.GetHealthCheckFirewallName()
		if hcFWName != tc.expectedHCFWName {
			t.Errorf("Wrong health check firewall rule name, expected %s, got %s", tc.expectedHCFWName, hcFWName)
		}
	}
}
