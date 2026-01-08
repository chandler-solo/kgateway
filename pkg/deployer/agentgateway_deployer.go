package deployer

import (
	"helm.sh/helm/v3/pkg/chart"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
)

var _ Deployer = (*agentgatewayDeployer)(nil)

// agentgatewayDeployer is a Deployer implementation for agentgateway-based gateways.
type agentgatewayDeployer struct {
	baseDeployer
}

// NewAgentgatewayDeployer creates a new deployer for agentgateway-based gateways.
// Prefer using the factory functions in pkg/kgateway/deployer which bake in
// the appropriate controller names.
func NewAgentgatewayDeployer(
	controllerName string,
	scheme *runtime.Scheme,
	client apiclient.Client,
	chart *chart.Chart,
	hvg HelmValuesGenerator,
	helmReleaseNameAndNamespaceGenerator func(obj client.Object) (string, string),
	opts ...Option,
) Deployer {
	return &agentgatewayDeployer{
		baseDeployer: newBaseDeployer(controllerName, scheme, client, chart, hvg, helmReleaseNameAndNamespaceGenerator, opts...),
	}
}
