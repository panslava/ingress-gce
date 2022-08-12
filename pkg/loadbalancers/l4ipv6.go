package loadbalancers

import (
	"strings"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/ingress-gce/pkg/annotations"
	"k8s.io/ingress-gce/pkg/composite"
	"k8s.io/ingress-gce/pkg/firewalls"
	"k8s.io/ingress-gce/pkg/utils"
	"k8s.io/klog/v2"
	"k8s.io/legacy-cloud-providers/gce"
)

func (l *L4) ensureIPv6Resources(syncResult *L4ILBSyncResult, nodeNames []string, options gce.ILBOptions, bsLink string, bsName string) {
	ipv6fr, err := l.ensureIPv6ForwardingRule(bsLink, options)
	if err != nil {
		klog.Errorf("ensureIPv6Resources: Failed to create ipv6 forwarding rule - %v", err)
		syncResult.GCEResourceInError = annotations.IPv6ForwardingRuleResource
		syncResult.Error = err
	}

	if ipv6fr.IPProtocol == string(corev1.ProtocolTCP) {
		syncResult.Annotations[annotations.IPv6TCPForwardingRuleKey] = ipv6fr.Name
	} else {
		syncResult.Annotations[annotations.IPv6UDPForwardingRuleKey] = ipv6fr.Name
	}

	err = l.ensureIPv6Firewall(ipv6fr, bsName, nodeNames)
	if err != nil {
		syncResult.GCEResourceInError = annotations.IPv6FirewallRuleResource
		syncResult.Error = err
		return
	}

	trimmedIPv6Address := strings.Split(ipv6fr.IPAddress, "/")[0]
	syncResult.Status = utils.AddIPToLBStatus(syncResult.Status, trimmedIPv6Address)
}

func (l *L4) deleteIPv6Resources(syncResult *L4ILBSyncResult, bsName string) {
	err := l.deleteIPv6ForwardingRule()
	if err != nil {
		klog.Errorf("Failed to delete ipv6 forwarding rule for internal loadbalancer service %s, err %v", l.NamespacedName.String(), err)
		syncResult.Error = err
		syncResult.GCEResourceInError = annotations.IPv6ForwardingRuleResource
	}

	err = l.deleteIPv6Firewall(bsName)
	if err != nil {
		klog.Errorf("Failed to delete ipv6 firewall rule for internal loadbalancer service %s, err %v", l.NamespacedName.String(), err)
		syncResult.GCEResourceInError = annotations.IPv6FirewallRuleResource
		syncResult.Error = err
	}
}

func (l *L4) getIPv6FRName() string {
	protocol := utils.GetProtocol(l.Service.Spec.Ports)
	return l.getIPv6FRNameWithProtocol(string(protocol))
}

func (l *L4) getIPv6FRNameWithProtocol(protocol string) string {
	return l.namer.L4IPv6ForwardingRule(l.Service.Namespace, l.Service.Name, strings.ToLower(protocol))
}

func (l *L4) getIPv6FirewallName(bsName string) string {
	return bsName + "-ipv6"
}

func (l *L4) ensureIPv6Firewall(forwardingRule *composite.ForwardingRule, bsName string, nodeNames []string) error {
	svcPorts := l.Service.Spec.Ports
	portRanges := utils.GetServicePortRanges(svcPorts)
	protocol := utils.GetProtocol(svcPorts)

	ipv6nodesFWRParams := firewalls.FirewallParams{
		PortRanges: portRanges,
		// TODO(panslava): support .spec.loadBalancerSourceRanges
		SourceRanges:      []string{"0::0/0"},
		DestinationRanges: []string{forwardingRule.IPAddress},
		Protocol:          string(protocol),
		Name:              l.getIPv6FirewallName(bsName),
		NodeNames:         nodeNames,
		L4Type:            utils.ILB,
	}

	return firewalls.EnsureL4LBFirewallForNodes(l.Service, &ipv6nodesFWRParams, l.cloud, l.recorder)
}

func (l *L4) deleteIPv6ForwardingRule() error {
	ipv6FrName := l.getIPv6FRName()
	ipv6key, err := l.CreateKey(ipv6FrName)
	if err != nil {
		return err
	}
	return utils.IgnoreHTTPNotFound(composite.DeleteForwardingRule(l.cloud, ipv6key, meta.VersionGA))
}

func (l *L4) deleteIPv6Firewall(bsName string) error {
	return l.deleteFirewall(l.getIPv6FirewallName(bsName))
}
