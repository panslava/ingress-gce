package forwardingrules

import (
	"testing"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/legacy-cloud-providers/gce"
)

func TestCreateForwardingRule(t *testing.T) {
	testCases := []struct {
		frRule *composite.ForwardingRule
		desc   string
	}{
		{
			frRule: &composite.ForwardingRule{
				Name:                "elb",
				Description:         "elb description",
				LoadBalancingScheme: string(cloud.SchemeExternal),
			},
			desc: "Test creating external forwarding rule",
		},
		{
			frRule: &composite.ForwardingRule{
				Name:                "ilb",
				Description:         "ilb description",
				LoadBalancingScheme: string(cloud.SchemeInternal),
			},
			desc: "Test creating internal forwarding rule",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fakeGCE := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
			frc := NewForwardingRules(fakeGCE, meta.VersionGA, meta.Regional)

			err := frc.Create(tc.frRule)
			if err != nil {
				t.Fatalf("frc.Create(%v), returned error %v, want nil", tc.frRule, err)
			}

			verifyForwardingRuleExists(t, fakeGCE, tc.frRule.Name)
		})
	}
}

func TestGetForwardingRule(t *testing.T) {
	elbForwardingRule := &composite.ForwardingRule{
		Name:                "elb",
		Version:             meta.VersionGA,
		Scope:               meta.Regional,
		LoadBalancingScheme: string(cloud.SchemeExternal),
	}
	ilbForwardingRule := &composite.ForwardingRule{
		Name:                "ilb",
		Version:             meta.VersionGA,
		Scope:               meta.Regional,
		LoadBalancingScheme: string(cloud.SchemeInternal),
	}

	testCases := []struct {
		existingFwdRules []*composite.ForwardingRule
		getFwdRuleName   string
		expectedFwdRule  *composite.ForwardingRule
		desc             string
	}{
		{
			existingFwdRules: []*composite.ForwardingRule{elbForwardingRule, ilbForwardingRule},
			getFwdRuleName:   elbForwardingRule.Name,
			expectedFwdRule:  elbForwardingRule,
			desc:             "Test getting external forwarding rule",
		},
		{
			existingFwdRules: []*composite.ForwardingRule{elbForwardingRule, ilbForwardingRule},
			getFwdRuleName:   ilbForwardingRule.Name,
			expectedFwdRule:  ilbForwardingRule,
			desc:             "Test getting internal forwarding rule",
		},
		{
			existingFwdRules: []*composite.ForwardingRule{elbForwardingRule, ilbForwardingRule},
			getFwdRuleName:   "non-existent-rule",
			expectedFwdRule:  nil,
			desc:             "Test getting non existent forwarding rule",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fakeGCE := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
			for _, fr := range tc.existingFwdRules {
				mustCreateForwardingRule(t, fakeGCE, fr)
			}
			frc := NewForwardingRules(fakeGCE, meta.VersionGA, meta.Regional)

			fr, err := frc.Get(tc.getFwdRuleName)
			if err != nil {
				t.Fatalf("frc.Get(%v), returned error %v, want nil", tc.getFwdRuleName, err)
			}

			ignoreFields := cmpopts.IgnoreFields(composite.ForwardingRule{}, "SelfLink", "Region")
			if !cmp.Equal(fr, tc.expectedFwdRule, ignoreFields) {
				diff := cmp.Diff(fr, tc.expectedFwdRule, ignoreFields)
				t.Errorf("frc.Get(s) returned %v, not equal to expectedFwdRule %v, diff: %v", fr, tc.expectedFwdRule, diff)
			}
		})
	}
}

func TestDeleteForwardingRule(t *testing.T) {
	elbForwardingRule := &composite.ForwardingRule{
		Name:                "elb",
		LoadBalancingScheme: string(cloud.SchemeExternal),
	}
	ilbForwardingRule := &composite.ForwardingRule{
		Name:                "ilb",
		LoadBalancingScheme: string(cloud.SchemeInternal),
	}

	testCases := []struct {
		existingFwdRules    []*composite.ForwardingRule
		deleteFwdRuleName   string
		shouldExistFwdRules []*composite.ForwardingRule
		desc                string
	}{
		{
			existingFwdRules:    []*composite.ForwardingRule{elbForwardingRule, ilbForwardingRule},
			deleteFwdRuleName:   elbForwardingRule.Name,
			shouldExistFwdRules: []*composite.ForwardingRule{ilbForwardingRule},
			desc:                "Delete elb forwarding rule",
		},
		{
			existingFwdRules:    []*composite.ForwardingRule{elbForwardingRule, ilbForwardingRule},
			deleteFwdRuleName:   ilbForwardingRule.Name,
			shouldExistFwdRules: []*composite.ForwardingRule{elbForwardingRule},
			desc:                "Delete ilb forwarding rule",
		},
		{
			existingFwdRules:    []*composite.ForwardingRule{elbForwardingRule},
			deleteFwdRuleName:   elbForwardingRule.Name,
			shouldExistFwdRules: []*composite.ForwardingRule{},
			desc:                "Delete single elb forwarding rule",
		},
		{
			existingFwdRules:    []*composite.ForwardingRule{elbForwardingRule, ilbForwardingRule},
			deleteFwdRuleName:   "non-existent",
			shouldExistFwdRules: []*composite.ForwardingRule{elbForwardingRule, ilbForwardingRule},
			desc:                "Delete non existent forwarding rule",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fakeGCE := gce.NewFakeGCECloud(gce.DefaultTestClusterValues())
			for _, fr := range tc.existingFwdRules {
				mustCreateForwardingRule(t, fakeGCE, fr)
			}
			frc := NewForwardingRules(fakeGCE, meta.VersionGA, meta.Regional)

			err := frc.Delete(tc.deleteFwdRuleName)
			if err != nil {
				t.Fatalf("frc.Delete(%v), returned error %v, want nil", tc.deleteFwdRuleName, err)
			}

			verifyForwardingRuleNotExists(t, fakeGCE, tc.deleteFwdRuleName)
			for _, fw := range tc.shouldExistFwdRules {
				verifyForwardingRuleExists(t, fakeGCE, fw.Name)
			}
		})
	}
}

func verifyForwardingRuleExists(t *testing.T, cloud *gce.Cloud, name string) {
	t.Helper()
	verifyForwardingRuleShouldExist(t, cloud, name, true)
}

func verifyForwardingRuleNotExists(t *testing.T, cloud *gce.Cloud, name string) {
	t.Helper()
	verifyForwardingRuleShouldExist(t, cloud, name, false)
}

func verifyForwardingRuleShouldExist(t *testing.T, cloud *gce.Cloud, name string, shouldExist bool) {
	t.Helper()

	key, err := composite.CreateKey(cloud, name, meta.Regional)
	if err != nil {
		t.Fatalf("Failed to create key for fetching forwarding rule %s, err: %v", name, err)
	}
	_, err = composite.GetForwardingRule(cloud, key, meta.VersionGA)
	if err != nil {
		if utils.IsNotFoundError(err) {
			if shouldExist {
				t.Errorf("Forwarding rule %s was not found, expected to exist", name)
			}
			return
		}
		t.Fatalf("composite.GetForwardingRule(_, %v, %v) returned error %v, want nil", key, meta.VersionGA, err)
	}
	if !shouldExist {
		t.Errorf("Forwarding rule %s exists, expected to be not found", name)
	}
}

func mustCreateForwardingRule(t *testing.T, cloud *gce.Cloud, fr *composite.ForwardingRule) {
	t.Helper()

	key := meta.RegionalKey(fr.Name, cloud.Region())
	err := composite.CreateForwardingRule(cloud, key, fr)
	if err != nil {
		t.Fatalf("composite.CreateForwardingRule(_, %s, %v) returned error %v, want nil", key, fr, err)
	}
}
