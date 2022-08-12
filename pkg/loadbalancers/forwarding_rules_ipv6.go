package loadbalancers

import (
	"fmt"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud"
	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/events"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/klog/v2"
	"k8s.io/legacy-cloud-providers/gce"
)

func (l *L4) ensureIPv6ForwardingRule(bsLink string, options gce.ILBOptions) (*composite.ForwardingRule, error) {
	expectedIPv6FwdRule, err := l.buildExpectedIPv6ForwardingRule(bsLink, options)
	if err != nil {
		return nil, fmt.Errorf("l.buildExpectedIPv6ForwardingRule(%s, %v) returned error %w, want nil", bsLink, options, err)
	}

	existingIPv6FwdRule, err := l.forwardingRules.Get(expectedIPv6FwdRule.Name)
	if err != nil {
		return nil, fmt.Errorf("l.forwardingRules.GetForwardingRule(%s) returned error %w, want nil", expectedIPv6FwdRule.Name, err)
	}

	if existingIPv6FwdRule != nil {
		equal, err := EqualIPv6ForwardingRules(existingIPv6FwdRule, expectedIPv6FwdRule)
		if err != nil {
			return existingIPv6FwdRule, err
		}
		if equal {
			klog.V(2).Infof("ensureIPv6ForwardingRule: Skipping update of unchanged ipv6 forwarding rule - %s", expectedIPv6FwdRule.Name)
			return existingIPv6FwdRule, nil
		}

		frDiff := cmp.Diff(existingIPv6FwdRule, expectedIPv6FwdRule, cmpopts.IgnoreFields(composite.ForwardingRule{}, "IPAddress"))
		klog.V(2).Infof("ensureIPv6ForwardingRule: forwarding rule changed - Existing - %+v\n, New - %+v\n, Diff(-existing, +new) - %s\n. Deleting existing ipv6 forwarding rule.", existingIPv6FwdRule, expectedIPv6FwdRule, frDiff)

		err = l.forwardingRules.Delete(existingIPv6FwdRule.Name)
		if err != nil {
			return nil, err
		}
		l.recorder.Eventf(l.Service, corev1.EventTypeNormal, events.SyncIngress, "ForwardingRule %q deleted", existingIPv6FwdRule.Name)
	}
	klog.V(2).Infof("ensureIPv6ForwardingRule: Creating/Recreating forwarding rule - %s", expectedIPv6FwdRule.Name)
	err = l.forwardingRules.Create(expectedIPv6FwdRule)
	if err != nil {
		return nil, err
	}

	createdFr, err := l.forwardingRules.Get(expectedIPv6FwdRule.Name)
	return createdFr, err
}

func (l *L4) buildExpectedIPv6ForwardingRule(bsLink string, options gce.ILBOptions) (*composite.ForwardingRule, error) {
	frName := l.getIPv6FRName()

	frDesc, err := utils.MakeL4IPv6ForwardingRuleDescription(l.Service)
	if err != nil {
		return nil, fmt.Errorf("failed to compute description for forwarding rule %s, err: %w", frName, err)
	}

	subnetworkURL := l.cloud.SubnetworkURL()

	if options.SubnetName != "" {
		key, err := l.CreateKey(frName)
		if err != nil {
			return nil, err
		}
		subnetKey := *key
		subnetKey.Name = options.SubnetName
		subnetworkURL = cloud.SelfLink(meta.VersionGA, l.cloud.NetworkProjectID(), "subnetworks", &subnetKey)
	}

	svcPorts := l.Service.Spec.Ports
	ports := utils.GetPorts(svcPorts)
	protocol := utils.GetProtocol(svcPorts)

	fr := &composite.ForwardingRule{
		Name:                frName,
		Description:         frDesc,
		IPProtocol:          string(protocol),
		Ports:               ports,
		LoadBalancingScheme: string(cloud.SchemeInternal),
		BackendService:      bsLink,
		IpVersion:           "IPV6",
		Network:             l.cloud.NetworkURL(),
		Subnetwork:          subnetworkURL,
		AllowGlobalAccess:   options.AllowGlobalAccess,
		NetworkTier:         cloud.NetworkTierPremium.ToGCEValue(),
	}
	if len(ports) > maxL4ILBPorts {
		fr.Ports = nil
		fr.AllPorts = true
	}

	return fr, nil
}

func EqualIPv6ForwardingRules(fr1, fr2 *composite.ForwardingRule) (bool, error) {
	id1, err := cloud.ParseResourceURL(fr1.BackendService)
	if err != nil {
		return false, fmt.Errorf("EqualIPv6ForwardingRules(): failed to parse backend resource URL from FR, err - %w", err)
	}
	id2, err := cloud.ParseResourceURL(fr2.BackendService)
	if err != nil {
		return false, fmt.Errorf("EqualIPv6ForwardingRules(): failed to parse resource URL from FR, err - %w", err)
	}
	return fr1.IPProtocol == fr2.IPProtocol &&
		fr1.LoadBalancingScheme == fr2.LoadBalancingScheme &&
		utils.EqualStringSets(fr1.Ports, fr2.Ports) &&
		id1.Equal(id2) &&
		fr1.AllowGlobalAccess == fr2.AllowGlobalAccess &&
		fr1.AllPorts == fr2.AllPorts &&
		fr1.Subnetwork == fr2.Subnetwork &&
		fr1.NetworkTier == fr2.NetworkTier, nil
}
