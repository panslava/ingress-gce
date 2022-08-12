package utils

import "k8s.io/api/core/v1"

func NeedsIPv6(service *v1.Service) bool {
	return supportsIPFamily(service, v1.IPv6Protocol)
}

func NeedsIPv4(service *v1.Service) bool {
	return supportsIPFamily(service, v1.IPv4Protocol)
}

func supportsIPFamily(service *v1.Service, ipFamily v1.IPFamily) bool {
	if service == nil {
		return false
	}

	ipFamilies := service.Spec.IPFamilies
	for _, ipf := range ipFamilies {
		if ipf == ipFamily {
			return true
		}
	}
	return false
}
