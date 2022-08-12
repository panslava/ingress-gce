package utils

import (
	"testing"

	"k8s.io/api/core/v1"
)

func TestNeedsIPv6(t *testing.T) {
	testCases := []struct {
		service       *v1.Service
		wantNeedsIPv6 bool
		desc          string
	}{
		{
			service:       nil,
			wantNeedsIPv6: false,
			desc:          "Should return false for nil pointer",
		},
		{
			service: &v1.Service{Spec: v1.ServiceSpec{
				IPFamilies: []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			}},
			wantNeedsIPv6: true,
			desc:          "Should detect ipv6 for dual-stack ip families",
		},
		{
			service: &v1.Service{Spec: v1.ServiceSpec{
				IPFamilies: []v1.IPFamily{v1.IPv4Protocol},
			}},
			wantNeedsIPv6: false,
			desc:          "Should not detect ipv6 for only ipv4 families",
		},
		{
			service: &v1.Service{Spec: v1.ServiceSpec{
				IPFamilies: []v1.IPFamily{v1.IPv6Protocol},
			}},
			wantNeedsIPv6: true,
			desc:          "Should detect ipv6 for only ipv6 families",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			needsIPv6 := NeedsIPv6(tc.service)

			if needsIPv6 != tc.wantNeedsIPv6 {
				t.Errorf("NeedsIPv6(%v) returned %t, not equal to expected wantNeedsIPv6 = %t", tc.service, needsIPv6, tc.wantNeedsIPv6)
			}
		})
	}
}

func TestNeedsIPv4(t *testing.T) {
	testCases := []struct {
		service       *v1.Service
		wantNeedsIPv4 bool
		desc          string
	}{
		{
			service:       nil,
			wantNeedsIPv4: false,
			desc:          "Should return false for nil pointer",
		},
		{
			service: &v1.Service{Spec: v1.ServiceSpec{
				IPFamilies: []v1.IPFamily{v1.IPv4Protocol, v1.IPv6Protocol},
			}},
			wantNeedsIPv4: true,
			desc:          "Should handle dual-stack ip families",
		},
		{
			service: &v1.Service{Spec: v1.ServiceSpec{
				IPFamilies: []v1.IPFamily{v1.IPv4Protocol},
			}},
			wantNeedsIPv4: true,
			desc:          "Should handle only ipv4 family",
		},
		{
			service: &v1.Service{Spec: v1.ServiceSpec{
				IPFamilies: []v1.IPFamily{v1.IPv6Protocol},
			}},
			wantNeedsIPv4: false,
			desc:          "Should not handle only ipv6 family",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			needsIPv4 := NeedsIPv4(tc.service)

			if needsIPv4 != tc.wantNeedsIPv4 {
				t.Errorf("NeedsIPv4(%v) returned %t, not equal to expected wantNeedsIPv6 = %t", tc.service, needsIPv4, tc.wantNeedsIPv4)
			}
		})
	}
}
