package deployer

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/kgateway"
)

// EnvoyHelmConfig stores the top-level helm values used by the deployer for Envoy deployments.
type EnvoyHelmConfig struct {
	Gateway *EnvoyHelmGateway `json:"gateway,omitempty"`
}

// EnvoyHelmGateway contains helm values specific to Envoy gateway deployments.
type EnvoyHelmGateway struct {
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
	Service      *EnvoyHelmService          `json:"service,omitempty"`
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

	// sds container values
	SdsContainer *EnvoyHelmSdsContainer `json:"sdsContainer,omitempty"`
	// istio container values
	IstioContainer *EnvoyHelmIstioContainer `json:"istioContainer,omitempty"`
	// istio integration values
	Istio *EnvoyHelmIstio `json:"istio,omitempty"`

	// envoy container values
	LogLevel          *string                      `json:"logLevel,omitempty"`
	ComponentLogLevel *string                      `json:"componentLogLevel,omitempty"`
	Image             *HelmImage                   `json:"image,omitempty"`
	Resources         *corev1.ResourceRequirements `json:"resources,omitempty"`
	SecurityContext   *corev1.SecurityContext      `json:"securityContext,omitempty"`
	Env               []corev1.EnvVar              `json:"env,omitempty"`
	ExtraVolumeMounts []corev1.VolumeMount         `json:"extraVolumeMounts,omitempty"`

	// envoy bootstrap values
	DnsResolver *EnvoyHelmDnsResolver `json:"dnsResolver,omitempty"`

	// xds values
	Xds *HelmXds `json:"xds,omitempty"`

	// stats values
	Stats *EnvoyHelmStatsConfig `json:"stats,omitempty"`
}

// EnvoyHelmService contains service configuration for Envoy deployments.
type EnvoyHelmService struct {
	Type                  *string           `json:"type,omitempty"`
	ClusterIP             *string           `json:"clusterIP,omitempty"`
	LoadBalancerClass     *string           `json:"loadBalancerClass,omitempty"`
	LoadBalancerIP        *string           `json:"loadBalancerIP,omitempty"`
	ExtraAnnotations      map[string]string `json:"extraAnnotations,omitempty"`
	ExtraLabels           map[string]string `json:"extraLabels,omitempty"`
	ExternalTrafficPolicy *string           `json:"externalTrafficPolicy,omitempty"`
}

// EnvoyHelmDnsResolver contains DNS resolver configuration for Envoy.
type EnvoyHelmDnsResolver struct {
	UdpMaxQueries *int32 `json:"udpMaxQueries,omitempty"`
}

// EnvoyHelmIstio contains Istio integration configuration for Envoy.
type EnvoyHelmIstio struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// EnvoyHelmSdsContainer contains SDS container configuration for Envoy.
type EnvoyHelmSdsContainer struct {
	Image           *HelmImage                   `json:"image,omitempty"`
	Resources       *corev1.ResourceRequirements `json:"resources,omitempty"`
	SecurityContext *corev1.SecurityContext      `json:"securityContext,omitempty"`
	SdsBootstrap    *EnvoyHelmSdsBootstrap       `json:"sdsBootstrap,omitempty"`
}

// EnvoyHelmSdsBootstrap contains SDS bootstrap configuration.
type EnvoyHelmSdsBootstrap struct {
	LogLevel *string `json:"logLevel,omitempty"`
}

// EnvoyHelmIstioContainer contains Istio proxy container configuration.
type EnvoyHelmIstioContainer struct {
	Image    *HelmImage `json:"image,omitempty"`
	LogLevel *string    `json:"logLevel,omitempty"`

	Resources       *corev1.ResourceRequirements `json:"resources,omitempty"`
	SecurityContext *corev1.SecurityContext      `json:"securityContext,omitempty"`

	IstioDiscoveryAddress *string `json:"istioDiscoveryAddress,omitempty"`
	IstioMetaMeshId       *string `json:"istioMetaMeshId,omitempty"`
	IstioMetaClusterId    *string `json:"istioMetaClusterId,omitempty"`
}

// EnvoyHelmStatsConfig contains stats configuration for Envoy.
type EnvoyHelmStatsConfig struct {
	Enabled            *bool                  `json:"enabled,omitempty"`
	RoutePrefixRewrite *string                `json:"routePrefixRewrite,omitempty"`
	EnableStatsRoute   *bool                  `json:"enableStatsRoute,omitempty"`
	StatsPrefixRewrite *string                `json:"statsPrefixRewrite,omitempty"`
	Matcher            *EnvoyHelmStatsMatcher `json:"matcher,omitempty"`
}

// EnvoyHelmStatsMatcher represents mutually exclusive inclusion or exclusion lists for Envoy stats.
type EnvoyHelmStatsMatcher struct {
	InclusionList []EnvoyHelmStringMatcher `json:"inclusionList,omitempty"`
	ExclusionList []EnvoyHelmStringMatcher `json:"exclusionList,omitempty"`
}

// EnvoyHelmStringMatcher mirrors a subset of Envoy's StringMatcher.
// Only one of these fields should be set per matcher.
type EnvoyHelmStringMatcher struct {
	Exact      *string `json:"exact,omitempty"`
	Prefix     *string `json:"prefix,omitempty"`
	Suffix     *string `json:"suffix,omitempty"`
	Contains   *string `json:"contains,omitempty"`
	SafeRegex  *string `json:"safeRegex,omitempty"`
	IgnoreCase *bool   `json:"ignoreCase,omitempty"`
}
