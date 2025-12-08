package deployer

import (
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
)

// NewEnvoyGatewayDeployer creates a deployer for Envoy-based gateways.
func NewEnvoyGatewayDeployer(
	controllerName, agwControllerName, agwGatewayClassName string,
	scheme *runtime.Scheme,
	client apiclient.Client,
	hvg deployer.HelmValuesGenerator,
	opts ...deployer.Option,
) (*deployer.Deployer, error) {
	envoyChart, err := LoadEnvoyChart()
	if err != nil {
		return nil, err
	}
	return deployer.NewDeployer(
		controllerName, agwControllerName, agwGatewayClassName,
		scheme, client, envoyChart, hvg, GatewayReleaseNameAndNamespace, opts...), nil
}

// NewAgentgatewayDeployer creates a deployer for agentgateway-based gateways.
func NewAgentgatewayDeployer(
	controllerName, agwControllerName, agwGatewayClassName string,
	scheme *runtime.Scheme,
	client apiclient.Client,
	hvg deployer.HelmValuesGenerator,
	opts ...deployer.Option,
) (*deployer.Deployer, error) {
	agentgatewayChart, err := LoadAgentgatewayChart()
	if err != nil {
		return nil, err
	}
	return deployer.NewDeployer(
		controllerName, agwControllerName, agwGatewayClassName,
		scheme, client, agentgatewayChart, hvg, GatewayReleaseNameAndNamespace, opts...), nil
}
