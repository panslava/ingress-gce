package healthchecks_l4

import (
	"fmt"

	"github.com/GoogleCloudPlatform/k8s-cloud-provider/pkg/cloud/meta"
	"k8s.io/ingress-gce/pkg/composite"
)

// L4HealthChecks defines methods for creating and deleting health checks (and their firewall rules) for l4 services
type L4HealthChecks interface {
	// EnsureHealthCheckWithFirewall creates health check and firewall rule for health check for l4 service
	EnsureHealthCheckWithFirewall(nodeNames []string) (string, error)
	// DeleteHealthChecksWithFirewalls
	DeleteHealthChecksWithFirewalls() error

	GetHealthCheckName() string
	GetHealthCheckFirewallName() string
}

type L4HealthCheckError struct {
	Err                error
	GCEResourceInError string
}

func (l4hcerr *L4HealthCheckError) Error() string {
	return fmt.Sprintf("GCE Resource %v is in error: %v", l4hcerr.GCEResourceInError, l4hcerr.Err)
}

type HealthChecksProvider interface {
	Get(name string, scope meta.KeyType) (*composite.HealthCheck, error)
	Create(healthCheck *composite.HealthCheck) error
	Update(name string, scope meta.KeyType, updatedHealthCheck *composite.HealthCheck) error
	Delete(name string, scope meta.KeyType) error
}
