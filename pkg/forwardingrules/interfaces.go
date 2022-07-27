package forwardingrules

import (
	"k8s.io/ingress-gce/pkg/composite"
)

// ForwardingRulesProvider is an interface to manage Google Cloud Forwarding Rules
type ForwardingRulesProvider interface {
	Get(name string) (*composite.ForwardingRule, error)
	Create(forwardingRule *composite.ForwardingRule) error
	Delete(name string) error
}
