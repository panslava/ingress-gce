package utils

import "k8s.io/api/core/v1"

func NeedsIPv6(service *v1.Service) bool {
	if service.Spec.IPFamilyPolicy == nil {
		return false
	}

	ipFamilyPolicy := *service.Spec.IPFamilyPolicy
	if ipFamilyPolicy == v1.IPFamilyPolicySingleStack {
		ipFamilies := service.Spec.IPFamilies

		if len(ipFamilies) == 0 {
			return false
		}

		return ipFamilies[0] == v1.IPv6Protocol
	}

	return true
}
