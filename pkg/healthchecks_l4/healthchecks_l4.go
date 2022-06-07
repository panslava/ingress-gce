/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package healthchecks_l4

import (
	"fmt"
	"strconv"
	"sync"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/ingress-gce/pkg/annotations"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/firewalls"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/ingress-gce/pkg/utils/namer"
	"k8s.io/klog/v2"
	"k8s.io/legacy-cloud-providers/gce"
)

var (
	sharedLock = &sync.Mutex{}
)

type l4HealthChecks struct {
	hcProvider HealthChecksProvider
	mu         *sync.Mutex
	cloud      *gce.Cloud
	namer      namer.L4ResourcesNamer
	recorder   record.EventRecorder
	service    *corev1.Service
}

func newL4HealthChecks(lock *sync.Mutex, cloud *gce.Cloud, service *corev1.Service, recorder record.EventRecorder, namer namer.L4ResourcesNamer, l4Type utils.L4LBType) *l4HealthChecks {
	//var scope meta.KeyType = meta.Global
	//
	//if l4Type == utils.XLB {
	//	scope = meta.Regional
	//}
	//
	//isClusterTrafficPolicy := !helpers.RequestsOnlyLocalTraffic(service)
	//
	//var path, hcName, hcFWName string
	//var port int32
	//
	//if isClusterTrafficPolicy {
	//	path = gce.GetNodesHealthCheckPath()
	//	port = gce.GetNodesHealthCheckPort()
	//
	//	hcName = namer.ClusterPolicyHealthCheck()
	//	hcFWName = namer.ClusterPolicyHealthCheckFirewallRule()
	//} else {
	//	path, port = helpers.GetServiceHealthCheckPathPort(service)
	//
	//	hcName = namer.LocalPolicyHealthCheck(service.Namespace, service.Name)
	//	hcFWName = namer.LocalPolicyHealthCheckFirewallRule(service.Namespace, service.Name)
	//}
	//
	//hcParams := &l4healthCheckParams{
	//	l4Type:                 l4Type,
	//	scope:                  scope,
	//	isClusterTrafficPolicy: isClusterTrafficPolicy,
	//	path:                   path,
	//	port:                   port,
	//	serviceName:            service.Name,
	//	serviceNamespace:       service.Namespace,
	//	healthCheckName:        hcName,
	//}

	return &l4HealthChecks{
		hcProvider: NewHealthChecks(cloud, meta.VersionGA),
		mu:         lock,
		cloud:      cloud,
		namer:      namer,
		recorder:   recorder,
		service:    service,
	}
}

func NewL4ILBHealthChecks(cloud *gce.Cloud, service *corev1.Service, recorder record.EventRecorder, namer namer.L4ResourcesNamer) *l4HealthChecks {
	return newL4HealthChecks(sharedLock, cloud, service, recorder, namer, utils.ILB)
}

func NewL4ELBHealthChecks(cloud *gce.Cloud, service *corev1.Service, recorder record.EventRecorder, namer namer.L4ResourcesNamer) *l4HealthChecks {
	return newL4HealthChecks(sharedLock, cloud, service, recorder, namer, utils.XLB)
}

func (l4hc *l4HealthChecks) GetHealthCheckName() string {
	return l4hc.hcParams.healthCheckName
}

func (l4hc *l4HealthChecks) GetHealthCheckFirewallName() string {
	return l4hc.hcFWName
}

func (l4hc *l4HealthChecks) isSharedHealthCheck() bool {
	return l4hc.hcParams.isClusterTrafficPolicy
}

func (l4hc *l4HealthChecks) EnsureHealthCheckWithFirewall(nodeNames []string) (string, error) {
	if l4hc.isSharedHealthCheck() {
		l4hc.mu.Lock()
		defer l4hc.mu.Unlock()
	}

	klog.V(3).Infof("Creating health check for service %s/%s.", l4hc.service.Namespace, l4hc.service.Name)

	hc, err := l4hc.ensureHealthCheck()
	if err != nil {
		return "", &L4HealthCheckError{
			Err:                fmt.Errorf("l4hc.ensureHealthCheck() returned error %w, want nil", err),
			GCEResourceInError: annotations.HealthcheckResource,
		}
	}

	klog.V(3).Infof("Creating health check firewall for service %s/%s.", l4hc.service.Namespace, l4hc.service.Name)
	err = l4hc.ensureHealthCheckFirewall(nodeNames)
	if err != nil {
		return "", &L4HealthCheckError{
			Err:                fmt.Errorf("l4hc.ensureHealthCheckFirewall(%v) returned error %w, want nil", nodeNames, err),
			GCEResourceInError: annotations.FirewallForHealthcheckResource,
		}
	}

	return hc.SelfLink, nil
}

func (l4hc *l4HealthChecks) ensureHealthCheck() (*composite.HealthCheck, error) {
	existingHC, err := l4hc.getExistingHealthCheck()
	if err != nil {
		return nil, fmt.Errorf("getExistingHealthCheck() returned error %w, want nil", err)
	}

	if existingHC == nil {
		hc, err := l4hc.createNewHealthCheck()
		if err != nil {
			return nil, fmt.Errorf("createNewHealthCheck() returned error %w, want nil", err)
		}

		return hc, nil
	}

	return l4hc.updateHealthCheck(existingHC)
}

func (l4hc *l4HealthChecks) getExistingHealthCheck() (*composite.HealthCheck, error) {
	return l4hc.hcProvider.Get(l4hc.hcParams.healthCheckName, l4hc.hcParams.scope)
}

func (l4hc *l4HealthChecks) createNewHealthCheck() (*composite.HealthCheck, error) {
	compositeHC := newCompositeL4HealthCheck(l4hc.cloud, l4hc.hcParams)

	err := l4hc.hcProvider.Create(compositeHC)
	if err != nil {
		return nil, fmt.Errorf("l4hc.Create(%v) returned error %w, want nil", compositeHC, err)
	}
	return compositeHC, nil
}

func (l4hc *l4HealthChecks) updateHealthCheck(existingHC *composite.HealthCheck) (*composite.HealthCheck, error) {
	expectedHC := newCompositeL4HealthCheck(l4hc.cloud, l4hc.hcParams)

	if !needToUpdateHealthChecks(existingHC, expectedHC) {
		// nothing to do
		klog.V(3).Infof("Healthcheck %v does not require update, skipping", existingHC.Name)
		return existingHC, nil
	}

	mergeHealthChecks(existingHC, expectedHC)
	klog.V(2).Infof("Updating healthcheck %s, updated healthcheck: %v", existingHC.Name, expectedHC)
	err := l4hc.hcProvider.Update(existingHC.Name, existingHC.Scope, expectedHC)
	if err != nil {
		return nil, fmt.Errorf("l4hc.Update(%s, %s, %v) returned error %w, want nil", existingHC.Name, existingHC.Scope, expectedHC, err)
	}
	return expectedHC, nil
}

func (l4hc *l4HealthChecks) ensureHealthCheckFirewall(nodeNames []string) error {
	hcFWRParams := firewalls.FirewallParams{
		PortRanges:   []string{strconv.Itoa(int(l4hc.hcParams.port))},
		SourceRanges: gce.L4LoadBalancerSrcRanges(),
		Protocol:     string(corev1.ProtocolTCP),
		Name:         l4hc.hcFWName,
		NodeNames:    nodeNames,
	}
	err := firewalls.EnsureL4LBFirewallForHc(l4hc.service, l4hc.isSharedHealthCheck(), &hcFWRParams, l4hc.cloud, l4hc.recorder)
	if err != nil {
		return err
	}

	return nil
}

// DeleteHealthChecksWithFirewalls deletes both cluster and local health checks with firewall
//
// We don't delete health check during service update,
// so it is possible that there might be some health check leak
// when externalTrafficPolicy is changed from Local to Cluster and new a health check was created.
// When service is deleted we need to check both health checks shared and non-shared
// and delete them if needed.
func (l4hc *l4HealthChecks) DeleteHealthChecksWithFirewalls() error {
	err := l4hc.safeDeleteClusterHealthCheckWithFirewall()

	err = l4hc.deleteLocalHealthCheckWithFirewall()

	return err
}

func (l4hc *l4HealthChecks) safeDeleteClusterHealthCheckWithFirewall() error {
	l4hc.mu.Lock()
	defer l4hc.mu.Unlock()

	err := l4hc.deleteClusterHealthCheck()
	if err != nil {
		if utils.IsInUsedByError(err) {
			klog.V(3).Infof("Cluster health check for service %s/%s is shared and in use, skipping deletion", l4hc.service.Namespace, l4hc.service.Name)
			return nil
		}
		return &L4HealthCheckError{
			Err:                fmt.Errorf("l4hc.deleteClusterHealthCheck() returned error %w, want nil", err),
			GCEResourceInError: annotations.HealthcheckResource,
		}
	}

	err = l4hc.safeDeleteClusterHealthCheckFirewall()
	if err != nil {
		return &L4HealthCheckError{
			Err:                fmt.Errorf("l4hc.safeDeleteClusterHealthCheckFirewall() returned error %v, want nil", err),
			GCEResourceInError: annotations.FirewallForHealthcheckResource,
		}
	}

	return nil
}

func (l4hc *l4HealthChecks) deleteLocalHealthCheckWithFirewall() error {
	err := l4hc.deleteLocalHealthCheck()
	if err != nil {
		return &L4HealthCheckError{
			Err:                fmt.Errorf("l4hc.deleteLocalHealthCheck() returned error %w, want nil", err),
			GCEResourceInError: annotations.HealthcheckResource,
		}
	}

	err = l4hc.deleteLocalHealthCheckFirewall()
	if err != nil {
		return &L4HealthCheckError{
			Err:                fmt.Errorf("l4hc.deleteLocalHealthCheckFirewall() returned error %w, want nil", err),
			GCEResourceInError: annotations.FirewallForHealthcheckResource,
		}
	}

	return nil
}

func (l4hc *l4HealthChecks) deleteLocalHealthCheck() error {
	return l4hc.hcProvider.Delete(l4hc.namer.LocalPolicyHealthCheck(l4hc.service.Namespace, l4hc.service.Name), l4hc.hcParams.scope)
}

func (l4hc *l4HealthChecks) deleteClusterHealthCheck() error {
	return l4hc.hcProvider.Delete(l4hc.namer.ClusterPolicyHealthCheck(), l4hc.hcParams.scope)
}

func (l4hc *l4HealthChecks) safeDeleteClusterHealthCheckFirewall() error {
	foundOtherHC, err := l4hc.existsOtherLBTypeClusterHealthCheck()
	if err != nil {
		return fmt.Errorf("l4hc.existsELBClusterHealthCheck() returned error %v, want nil", err)
	}

	if foundOtherHC {
		return nil
	}

	err = l4hc.deleteClusterHealthCheckFirewall()
	if err != nil {
		return fmt.Errorf("l4hc.deleteClusterHealthCheckFirewall() returned error %v, want nil", err)
	}
	return nil
}

func (l4hc *l4HealthChecks) existsOtherLBTypeClusterHealthCheck() (bool, error) {
	if l4hc.hcParams.l4Type == utils.XLB {
		return l4hc.existsILBClusterHealthCheck()
	} else {
		return l4hc.existsELBClusterHealthCheck()
	}
}

func (l4hc *l4HealthChecks) existsILBClusterHealthCheck() (bool, error) {
	return l4hc.existsHealthCheck(l4hc.namer.ClusterPolicyHealthCheck(), meta.Global)
}

func (l4hc *l4HealthChecks) existsELBClusterHealthCheck() (bool, error) {
	return l4hc.existsHealthCheck(l4hc.namer.ClusterPolicyHealthCheck(), meta.Regional)
}

func (l4hc *l4HealthChecks) existsHealthCheck(name string, scope meta.KeyType) (bool, error) {
	hc, err := l4hc.hcProvider.Get(name, scope)
	if err != nil {
		return false, fmt.Errorf("l4hc.Get(%s, %s) returned error %w, want nil", name, scope, err)
	}
	return hc == nil, nil
}

func (l4hc *l4HealthChecks) deleteLocalHealthCheckFirewall() error {
	return l4hc.deleteHealthCheckFirewall(l4hc.namer.LocalPolicyHealthCheckFirewallRule(l4hc.service.Namespace, l4hc.service.Name))
}

func (l4hc *l4HealthChecks) deleteClusterHealthCheckFirewall() error {
	return l4hc.deleteHealthCheckFirewall(l4hc.namer.ClusterPolicyHealthCheckFirewallRule())
}

func (l4hc *l4HealthChecks) deleteHealthCheckFirewall(name string) error {
	err := firewalls.EnsureL4FirewallRuleDeleted(l4hc.cloud, name)
	if err != nil {
		// Suppress Firewall XPN error, as this is no retryable and requires action by security admin
		if fwErr, ok := err.(*firewalls.FirewallXPNError); ok {
			l4hc.recorder.Eventf(l4hc.service, corev1.EventTypeNormal, "XPN", fwErr.Message)
			return nil
		}
		return err
	}
	return nil
}
