package deployer

import (
	corev1 "k8s.io/api/core/v1"
)

// AgentgatewayHelmConfig stores the top-level helm values used by the deployer
// for agentgateway deployments.
type AgentgatewayHelmConfig struct {
	Agentgateway *AgentgatewayHelmGateway `json:"agentgateway,omitempty"`
}

type AgentgatewayHelmService struct {
	LoadBalancerIP *string `json:"loadBalancerIP,omitempty"`
}

type AgentgatewayHelmGateway struct {
	// naming
	Name               *string           `json:"name,omitempty"`
	GatewayName        *string           `json:"gatewayName,omitempty"`
	GatewayNamespace   *string           `json:"gatewayNamespace,omitempty"`
	GatewayClassName   *string           `json:"gatewayClassName,omitempty"`
	GatewayAnnotations map[string]string `json:"gatewayAnnotations,omitempty"`
	GatewayLabels      map[string]string `json:"gatewayLabels,omitempty"`

	// deployment/service values
	Ports   []HelmPort               `json:"ports,omitempty"`
	Service *AgentgatewayHelmService `json:"service,omitempty"`

	// container values
	Image     *HelmImage                   `json:"image,omitempty"`
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	Env       []corev1.EnvVar              `json:"env,omitempty"`
	AgwXds    *HelmXds                     `json:"agwXds,omitempty"`

	// LogFormat specifies the logging format ("json" or "text")
	LogFormat *string `json:"logFormat,omitempty"`
	// RawConfig provides opaque (unvalidated) config to be merged into
	// config.yaml. If users are often choosing to use this, it may be time to
	// update the agentgatewayParameters API to provide validated,
	// better-tested alternatives.
	RawConfig map[string]any `json:"rawConfig,omitempty"`

	// Shutdown configures graceful shutdown timing
	Shutdown *AgentgatewayShutdown `json:"shutdown,omitempty"`

	// Istio configures Istio integration
	Istio *AgentgatewayHelmIstio `json:"istio,omitempty"`
}

// AgentgatewayHelmIstio configures Istio integration for agentgateway.
type AgentgatewayHelmIstio struct {
	// CaAddress is the address of the Istio CA.
	CaAddress *string `json:"caAddress,omitempty"`
	// TrustDomain is the Istio trust domain.
	TrustDomain *string `json:"trustDomain,omitempty"`
}

// AgentgatewayShutdown configures graceful shutdown timing for agentgateway.
type AgentgatewayShutdown struct {
	// Min is the minimum time (in seconds) to wait before allowing termination.
	Min *int64 `json:"min,omitempty"`
	// Max is the maximum time (in seconds) to wait before allowing termination.
	Max *int64 `json:"max,omitempty"`
}
