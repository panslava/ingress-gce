package healthchecks_l4

import (
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/klog/v2"
	"k8s.io/legacy-cloud-providers/gce"
)

const (
	// L4 Load Balancer parameters
	gceHcCheckIntervalSeconds = int64(8)
	gceHcTimeoutSeconds       = int64(1)
	// Start sending requests as soon as one healthcheck succeeds.
	gceHcHealthyThreshold = int64(1)
	// Defaults to 3 * 8 = 24 seconds before the LB will steer traffic away.
	gceHcUnhealthyThreshold = int64(3)
)

type l4healthCheckParams struct {
	l4Type                 utils.L4LBType
	scope                  meta.KeyType
	isClusterTrafficPolicy bool
	path                   string
	port                   int32
	serviceName            string
	serviceNamespace       string
	healthCheckName        string
}

func newCompositeL4HealthCheck(cloud *gce.Cloud, params *l4healthCheckParams) *composite.HealthCheck {
	var region string
	if params.scope == meta.Regional {
		region = cloud.Region()
	}

	serviceName := types.NamespacedName{Name: params.serviceName, Namespace: params.serviceNamespace}.String()
	desc, err := utils.MakeL4LBServiceDescription(serviceName, "", meta.VersionGA, params.isClusterTrafficPolicy, params.l4Type)
	if err != nil {
		klog.Warningf("Failed to generate description for HealthCheck %s, err %v", params.healthCheckName, err)
	}

	httpSettings := composite.HTTPHealthCheck{
		Port:        int64(params.port),
		RequestPath: params.path,
	}

	return &composite.HealthCheck{
		Name:               params.healthCheckName,
		CheckIntervalSec:   gceHcCheckIntervalSeconds,
		TimeoutSec:         gceHcTimeoutSeconds,
		HealthyThreshold:   gceHcHealthyThreshold,
		UnhealthyThreshold: gceHcUnhealthyThreshold,
		HttpHealthCheck:    &httpSettings,
		Type:               "HTTP",
		Description:        desc,
		Scope:              params.scope,
		// Region will be omitted by GCP API if Scope is set to Global
		Region: region,
	}
}

// needToUpdateHealthChecks checks whether the healthcheck needs to be updated.
func needToUpdateHealthChecks(hc, newHC *composite.HealthCheck) bool {
	return hc.HttpHealthCheck == nil ||
		newHC.HttpHealthCheck == nil ||
		hc.HttpHealthCheck.Port != newHC.HttpHealthCheck.Port ||
		hc.HttpHealthCheck.RequestPath != newHC.HttpHealthCheck.RequestPath ||
		hc.Description != newHC.Description ||
		hc.CheckIntervalSec < newHC.CheckIntervalSec ||
		hc.TimeoutSec < newHC.TimeoutSec ||
		hc.UnhealthyThreshold < newHC.UnhealthyThreshold ||
		hc.HealthyThreshold < newHC.HealthyThreshold
}

// mergeHealthChecks reconciles HealthCheck config to be no smaller than
// the default values. newHC is assumed to have defaults,
// since it is created by the newL4HealthCheck call.
// E.g. old health check interval is 2s, new has the default of 8.
// The HC interval will be reconciled to 8 seconds.
// If the existing health check values are larger than the default interval,
// the existing configuration will be kept.
func mergeHealthChecks(hc, newHC *composite.HealthCheck) {
	if hc.CheckIntervalSec > newHC.CheckIntervalSec {
		newHC.CheckIntervalSec = hc.CheckIntervalSec
	}
	if hc.TimeoutSec > newHC.TimeoutSec {
		newHC.TimeoutSec = hc.TimeoutSec
	}
	if hc.UnhealthyThreshold > newHC.UnhealthyThreshold {
		newHC.UnhealthyThreshold = hc.UnhealthyThreshold
	}
	if hc.HealthyThreshold > newHC.HealthyThreshold {
		newHC.HealthyThreshold = hc.HealthyThreshold
	}
}
