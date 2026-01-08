package deployer

import (
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

// NewEnvoyGatewayDeployer creates a deployer for Envoy-based gateways.
func NewEnvoyGatewayDeployer(
	scheme *runtime.Scheme,
	client apiclient.Client,
	gwParams *GatewayParameters,
	opts ...deployer.Option,
) (deployer.Deployer, error) {
	envoyChart, err := LoadEnvoyChart()
	if err != nil {
		return nil, err
	}
	return deployer.NewEnvoyDeployer(
		wellknown.DefaultGatewayControllerName,
		scheme, client, envoyChart, gwParams.EnvoyHelmValuesGenerator(), GatewayReleaseNameAndNamespace, opts...), nil
}

// NewAgentgatewayDeployer creates a deployer for agentgateway-based gateways.
func NewAgentgatewayDeployer(
	scheme *runtime.Scheme,
	client apiclient.Client,
	gwParams *GatewayParameters,
	opts ...deployer.Option,
) (deployer.Deployer, error) {
	agentgatewayChart, err := LoadAgentgatewayChart()
	if err != nil {
		return nil, err
	}
	return deployer.NewAgentgatewayDeployer(
		wellknown.DefaultAgwControllerName,
		scheme, client, agentgatewayChart, gwParams.AgentgatewayParametersHelmValuesGenerator(), GatewayReleaseNameAndNamespace, opts...), nil
}
