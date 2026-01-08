package deployer

import (
	"helm.sh/helm/v3/pkg/chart"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
)

var _ Deployer = (*envoyDeployer)(nil)

// envoyDeployer is a Deployer implementation for Envoy-based gateways.
type envoyDeployer struct {
	baseDeployer
}

// NewEnvoyDeployer creates a new deployer for Envoy-based gateways.
// Prefer using the factory functions in pkg/kgateway/deployer which bake in
// the appropriate controller names.
func NewEnvoyDeployer(
	controllerName string,
	scheme *runtime.Scheme,
	client apiclient.Client,
	chart *chart.Chart,
	hvg HelmValuesGenerator,
	helmReleaseNameAndNamespaceGenerator func(obj client.Object) (string, string),
	opts ...Option,
) Deployer {
	return &envoyDeployer{
		baseDeployer: newBaseDeployer(controllerName, scheme, client, chart, hvg, helmReleaseNameAndNamespaceGenerator, opts...),
	}
}
