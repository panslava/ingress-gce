package healthchecks_l4

import (
	"testing"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/legacy-cloud-providers/gce"
)

func TestCreateHealthCheck(t *testing.T) {
	testCases := []struct {
		healthCheck *composite.HealthCheck
		desc        string
	}{
		{
			healthCheck: &composite.HealthCheck{
				Name:  "regional-hc",
				Scope: meta.Regional,
			},
			desc: "Test creating regional health check",
		},
		{
			healthCheck: &composite.HealthCheck{
				Name:  "global-hc",
				Scope: meta.Global,
			},
			desc: "Test creating glbal health check",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fakeGCE := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
			hc := NewHealthChecks(fakeGCE, meta.VersionGA)

			err := hc.Create(tc.healthCheck)
			if err != nil {
				t.Fatalf("hc.Create(%v), returned error %v, want nil", tc.healthCheck, err)
			}

			verifyHealthCheckExists(t, fakeGCE, tc.healthCheck.Name, tc.healthCheck.Scope)
		})
	}
}

func TestGetHealthCheck(t *testing.T) {
	regionalHealthCheck := &composite.HealthCheck{
		Name:    "regional-hc",
		Version: meta.VersionGA,
		Scope:   meta.Regional,
	}
	globalHealthCheck := &composite.HealthCheck{
		Name:    "global-hc",
		Version: meta.VersionGA,
		Scope:   meta.Global,
	}

	testCases := []struct {
		existingHealthChecks []*composite.HealthCheck
		getHCName            string
		getHCScope           meta.KeyType
		expectedHealthCheck  *composite.HealthCheck
		desc                 string
	}{
		{
			existingHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			getHCName:            regionalHealthCheck.Name,
			getHCScope:           regionalHealthCheck.Scope,
			expectedHealthCheck:  regionalHealthCheck,
			desc:                 "Test getting regional health check",
		},
		{
			existingHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			getHCName:            globalHealthCheck.Name,
			getHCScope:           globalHealthCheck.Scope,
			expectedHealthCheck:  globalHealthCheck,
			desc:                 "Test getting global health check",
		},
		{
			existingHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			getHCName:            "non-existent-hc",
			getHCScope:           meta.Global,
			expectedHealthCheck:  nil,
			desc:                 "Test getting non existent global health check",
		},
		{
			existingHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			getHCName:            "non-existent-hc",
			getHCScope:           meta.Regional,
			expectedHealthCheck:  nil,
			desc:                 "Test getting non existent regional health check",
		},
		{
			existingHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			getHCName:            regionalHealthCheck.Name,
			getHCScope:           meta.Global,
			expectedHealthCheck:  nil,
			desc:                 "Test getting existent regional health check, but providing global scope",
		},
		{
			existingHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			getHCName:            globalHealthCheck.Name,
			getHCScope:           meta.Regional,
			expectedHealthCheck:  nil,
			desc:                 "Test getting existent global health check, but providing regional scope",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fakeGCE := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
			for _, hc := range tc.existingHealthChecks {
				mustCreateHealthCheck(t, fakeGCE, hc)
			}
			hcp := NewHealthChecks(fakeGCE, meta.VersionGA)

			hc, err := hcp.Get(tc.getHCName, tc.getHCScope)
			if err != nil {
				t.Fatalf("hcp.Get(%v), returned error %v, want nil", tc.getHCName, err)
			}

			// Scope field gets removed (but region added), after creating health check
			ignoreFields := cmpopts.IgnoreFields(composite.HealthCheck{}, "SelfLink", "Region", "Scope")
			if !cmp.Equal(hc, tc.expectedHealthCheck, ignoreFields) {
				diff := cmp.Diff(hc, tc.expectedHealthCheck, ignoreFields)
				t.Errorf("hcp.Get(s) returned %v, not equal to expectedHealthCheck %v, diff: %v", hc, tc.expectedHealthCheck, diff)
			}
		})
	}
}

func TestDeleteHealthCheck(t *testing.T) {
	regionalHealthCheck := &composite.HealthCheck{
		Name:    "regional-hc",
		Version: meta.VersionGA,
		Scope:   meta.Regional,
	}
	globalHealthCheck := &composite.HealthCheck{
		Name:    "global-hc",
		Version: meta.VersionGA,
		Scope:   meta.Global,
	}

	testCases := []struct {
		existingHealthChecks    []*composite.HealthCheck
		deleteHCName            string
		deleteHCScope           meta.KeyType
		shouldExistHealthChecks []*composite.HealthCheck
		desc                    string
	}{
		{
			existingHealthChecks:    []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			deleteHCName:            regionalHealthCheck.Name,
			deleteHCScope:           regionalHealthCheck.Scope,
			shouldExistHealthChecks: []*composite.HealthCheck{globalHealthCheck},
			desc:                    "Delete regional health check",
		},
		{
			existingHealthChecks:    []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			deleteHCName:            globalHealthCheck.Name,
			deleteHCScope:           globalHealthCheck.Scope,
			shouldExistHealthChecks: []*composite.HealthCheck{regionalHealthCheck},
			desc:                    "Delete global health check",
		},
		{
			existingHealthChecks:    []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			deleteHCName:            "non-existent",
			deleteHCScope:           meta.Regional,
			shouldExistHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			desc:                    "Delete non existent healthCheck",
		},
		{
			existingHealthChecks:    []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			deleteHCName:            globalHealthCheck.Name,
			deleteHCScope:           meta.Regional,
			shouldExistHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			desc:                    "Delete global health check name, but using regional scope",
		},
		{
			existingHealthChecks:    []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			deleteHCName:            regionalHealthCheck.Name,
			deleteHCScope:           meta.Global,
			shouldExistHealthChecks: []*composite.HealthCheck{regionalHealthCheck, globalHealthCheck},
			desc:                    "Delete regional health check name, but using global scope",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fakeGCE := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
			for _, hc := range tc.existingHealthChecks {
				mustCreateHealthCheck(t, fakeGCE, hc)
			}
			hc := NewHealthChecks(fakeGCE, meta.VersionGA)

			err := hc.Delete(tc.deleteHCName, tc.deleteHCScope)
			if err != nil {
				t.Fatalf("hc.Delete(%v), returned error %v, want nil", tc.deleteHCName, err)
			}

			verifyHealthCheckNotExists(t, fakeGCE, tc.deleteHCName, tc.deleteHCScope)
			for _, hc := range tc.shouldExistHealthChecks {
				verifyHealthCheckExists(t, fakeGCE, hc.Name, hc.Scope)
			}
		})
	}
}

func verifyHealthCheckExists(t *testing.T, cloud *gce.Cloud, name string, scope meta.KeyType) {
	t.Helper()
	verifyHealthCheckShouldExist(t, cloud, name, scope, true)
}

func verifyHealthCheckNotExists(t *testing.T, cloud *gce.Cloud, name string, scope meta.KeyType) {
	t.Helper()
	verifyHealthCheckShouldExist(t, cloud, name, scope, false)
}

func verifyHealthCheckShouldExist(t *testing.T, cloud *gce.Cloud, name string, scope meta.KeyType, shouldExist bool) {
	t.Helper()

	key, err := composite.CreateKey(cloud, name, scope)
	if err != nil {
		t.Fatalf("Failed to create key for fetching health check %s, err: %v", name, err)
	}
	_, err = composite.GetHealthCheck(cloud, key, meta.VersionGA)
	if err != nil {
		if utils.IsNotFoundError(err) {
			if shouldExist {
				t.Errorf("Health check %s in scope %s was not found", name, scope)
			}
			return
		}
		t.Fatalf("composite.GetHealthCheck(_, %v, %v) returned error %v, want nil", key, meta.VersionGA, err)
	}
	if !shouldExist {
		t.Errorf("Health Check %s in scope %s exists, expected to be not found", name, scope)
	}
}

func mustCreateHealthCheck(t *testing.T, cloud *gce.Cloud, hc *composite.HealthCheck) {
	t.Helper()

	key, err := composite.CreateKey(cloud, hc.Name, hc.Scope)
	if err != nil {
		t.Fatalf("composite.CreateKey(_, %s, %s) returned error %v, want nil", hc.Name, hc.Scope, err)
	}
	err = composite.CreateHealthCheck(cloud, key, hc)
	if err != nil {
		t.Fatalf("composite.CreateHealthCheck(_, %s, %v) returned error %v, want nil", key, hc, err)
	}
}
