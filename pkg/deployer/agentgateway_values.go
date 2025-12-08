package deployer

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/kgateway"
)

// AgentgatewayHelmConfig stores the top-level helm values used by the deployer
// for agentgateway deployments.
type AgentgatewayHelmConfig struct {
	Gateway *AgentgatewayHelmGateway `json:"gateway,omitempty"`
}

// AgentgatewayHelmGateway contains helm values specific to agentgateway deployments.
type AgentgatewayHelmGateway struct {
	// naming
	Name               *string           `json:"name,omitempty"`
	GatewayName        *string           `json:"gatewayName,omitempty"`
	GatewayNamespace   *string           `json:"gatewayNamespace,omitempty"`
	GatewayClassName   *string           `json:"gatewayClassName,omitempty"`
	GatewayAnnotations map[string]string `json:"gatewayAnnotations,omitempty"`
	GatewayLabels      map[string]string `json:"gatewayLabels,omitempty"`
	NameOverride       *string           `json:"nameOverride,omitempty"`
	FullnameOverride   *string           `json:"fullnameOverride,omitempty"`

	// deployment/service values
	ReplicaCount *uint32                    `json:"replicaCount,omitempty"`
	Ports        []HelmPort                 `json:"ports,omitempty"`
	Service      *HelmService               `json:"service,omitempty"`
	Strategy     *appsv1.DeploymentStrategy `json:"strategy,omitempty"`

	// serviceaccount values
	ServiceAccount *HelmServiceAccount `json:"serviceAccount,omitempty"`

	// pod template values
	ExtraPodAnnotations           map[string]string                 `json:"extraPodAnnotations,omitempty"`
	ExtraPodLabels                map[string]string                 `json:"extraPodLabels,omitempty"`
	ImagePullSecrets              []corev1.LocalObjectReference     `json:"imagePullSecrets,omitempty"`
	PodSecurityContext            *corev1.PodSecurityContext        `json:"podSecurityContext,omitempty"`
	NodeSelector                  map[string]string                 `json:"nodeSelector,omitempty"`
	Affinity                      *corev1.Affinity                  `json:"affinity,omitempty"`
	Tolerations                   []corev1.Toleration               `json:"tolerations,omitempty"`
	StartupProbe                  *corev1.Probe                     `json:"startupProbe,omitempty"`
	ReadinessProbe                *corev1.Probe                     `json:"readinessProbe,omitempty"`
	LivenessProbe                 *corev1.Probe                     `json:"livenessProbe,omitempty"`
	ExtraVolumes                  []corev1.Volume                   `json:"extraVolumes,omitempty"`
	GracefulShutdown              *kgateway.GracefulShutdownSpec    `json:"gracefulShutdown,omitempty"`
	TerminationGracePeriodSeconds *int64                            `json:"terminationGracePeriodSeconds,omitempty"`
	TopologySpreadConstraints     []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	PriorityClassName             *string                           `json:"priorityClassName,omitempty"`

	// agentgateway container values
	LogLevel          *string                      `json:"logLevel,omitempty"`
	Image             *HelmImage                   `json:"image,omitempty"`
	Resources         *corev1.ResourceRequirements `json:"resources,omitempty"`
	SecurityContext   *corev1.SecurityContext      `json:"securityContext,omitempty"`
	Env               []corev1.EnvVar              `json:"env,omitempty"`
	ExtraVolumeMounts []corev1.VolumeMount         `json:"extraVolumeMounts,omitempty"`

	// agentgateway xds values
	// Note: agentgateway uses agwXds for its xds connection, but the helm template
	// also references xds.host for constructing the XDS_ADDRESS
	Xds    *HelmXds `json:"xds,omitempty"`
	AgwXds *HelmXds `json:"agwXds,omitempty"`

	// agentgateway-specific config
	CustomConfigMapName *string `json:"customConfigMapName,omitempty"`
	// LogFormat specifies the logging format for agentgateway (Json or Text)
	LogFormat *string `json:"logFormat,omitempty"`
	// RawConfig provides opaque config to be merged into config.yaml
	RawConfig map[string]any `json:"rawConfig,omitempty"`
}
