package deployer

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/kgateway"
)

// AgentgatewayHelmConfig stores the top-level helm values used by the deployer
// for agentgateway deployments.
type AgentgatewayHelmConfig struct {
	Gateway *AgentgatewayHelmGateway `json:"gateway,omitempty"`
}

type AgentgatewayHelmGateway struct {
	// naming
	Name               *string           `json:"name,omitempty"`
	GatewayName        *string           `json:"gatewayName,omitempty"`
	GatewayNamespace   *string           `json:"gatewayNamespace,omitempty"`
	GatewayClassName   *string           `json:"gatewayClassName,omitempty"`
	GatewayAnnotations map[string]string `json:"gatewayAnnotations,omitempty"`
	GatewayLabels      map[string]string `json:"gatewayLabels,omitempty"`

	// deployment values
	Ports []HelmPort `json:"ports,omitempty"`

	// pod template values
	PodSecurityContext            *corev1.PodSecurityContext     `json:"podSecurityContext,omitempty"`
	StartupProbe                  *corev1.Probe                  `json:"startupProbe,omitempty"`
	ReadinessProbe                *corev1.Probe                  `json:"readinessProbe,omitempty"`
	GracefulShutdown              *kgateway.GracefulShutdownSpec `json:"gracefulShutdown,omitempty"`
	TerminationGracePeriodSeconds *int64                         `json:"terminationGracePeriodSeconds,omitempty"`

	// container values
	Image           *HelmImage                   `json:"image,omitempty"`
	Resources       *corev1.ResourceRequirements `json:"resources,omitempty"`
	SecurityContext *corev1.SecurityContext      `json:"securityContext,omitempty"`
	Env             []corev1.EnvVar              `json:"env,omitempty"`
	AgwXds          *HelmXds                     `json:"agwXds,omitempty"`

	// LogFormat specifies the logging format ("json" or "text")
	LogFormat *string `json:"logFormat,omitempty"`
	// RawConfig provides opaque (unvalidated) config to be merged into
	// config.yaml. If users are often choosing to use this, it may be time to
	// update the agentgatewayParameters API to provide validated,
	// better-tested alternatives.
	RawConfig map[string]any `json:"rawConfig,omitempty"`
}
