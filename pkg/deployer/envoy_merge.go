package deployer

import (
	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/kgateway"
)

// This file contains Envoy-specific merge functions that work with kgateway.* types.
// Shared merge utilities (generic types and corev1 types) are in merge.go.

func DeepMergeGatewayParameters(dst, src *kgateway.GatewayParameters) {
	if src != nil && src.Spec.SelfManaged != nil {
		// The src override specifies a self-managed gateway, set this on the dst
		// and skip merging of kube fields that are irrelevant because of using
		// a self-managed gateway
		dst.Spec.SelfManaged = src.Spec.SelfManaged
		dst.Spec.Kube = nil
		return
	}

	// nil src override means just use dst
	if src == nil || src.Spec.Kube == nil {
		return
	}

	if dst == nil || dst.Spec.Kube == nil {
		return
	}

	dstKube := dst.Spec.Kube
	srcKube := src.Spec.Kube.DeepCopy()

	dstKube.Deployment = deepMergeDeployment(dstKube.GetDeployment(), srcKube.GetDeployment())
	dstKube.EnvoyContainer = deepMergeEnvoyContainer(dstKube.GetEnvoyContainer(), srcKube.GetEnvoyContainer())
	dstKube.SdsContainer = deepMergeSdsContainer(dstKube.GetSdsContainer(), srcKube.GetSdsContainer())
	dstKube.PodTemplate = deepMergePodTemplate(dstKube.GetPodTemplate(), srcKube.GetPodTemplate())
	dstKube.Service = deepMergeService(dstKube.GetService(), srcKube.GetService())
	dstKube.ServiceAccount = deepMergeServiceAccount(dstKube.GetServiceAccount(), srcKube.GetServiceAccount())
	dstKube.Istio = deepMergeIstioIntegration(dstKube.GetIstio(), srcKube.GetIstio())
	dstKube.Stats = deepMergeStatsConfig(dstKube.GetStats(), srcKube.GetStats())
	dstKube.OmitDefaultSecurityContext = MergePointers(dstKube.GetOmitDefaultSecurityContext(), srcKube.GetOmitDefaultSecurityContext())
}

func deepMergeStatsConfig(dst *kgateway.StatsConfig, src *kgateway.StatsConfig) *kgateway.StatsConfig {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Enabled = MergePointers(dst.GetEnabled(), src.GetEnabled())
	dst.RoutePrefixRewrite = MergePointers(dst.GetRoutePrefixRewrite(), src.GetRoutePrefixRewrite())
	dst.EnableStatsRoute = MergePointers(dst.GetEnableStatsRoute(), src.GetEnableStatsRoute())
	dst.StatsRoutePrefixRewrite = MergePointers(dst.GetStatsRoutePrefixRewrite(), src.GetStatsRoutePrefixRewrite())
	dst.Matcher = MergePointers(dst.GetMatcher(), src.GetMatcher())

	return dst
}

func deepMergePodTemplate(dst, src *kgateway.Pod) *kgateway.Pod {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.ExtraLabels = DeepMergeMaps(dst.GetExtraLabels(), src.GetExtraLabels())
	dst.ExtraAnnotations = DeepMergeMaps(dst.GetExtraAnnotations(), src.GetExtraAnnotations())
	dst.SecurityContext = deepMergePodSecurityContext(dst.GetSecurityContext(), src.GetSecurityContext())
	dst.ImagePullSecrets = DeepMergeSlices(dst.GetImagePullSecrets(), src.GetImagePullSecrets())
	dst.NodeSelector = DeepMergeMaps(dst.GetNodeSelector(), src.GetNodeSelector())
	dst.Affinity = DeepMergeAffinity(dst.GetAffinity(), src.GetAffinity())
	dst.Tolerations = DeepMergeSlices(dst.GetTolerations(), src.GetTolerations())
	dst.GracefulShutdown = deepMergeGracefulShutdown(dst.GetGracefulShutdown(), src.GetGracefulShutdown())
	dst.TerminationGracePeriodSeconds = MergePointers(dst.TerminationGracePeriodSeconds, src.TerminationGracePeriodSeconds)
	dst.StartupProbe = deepMergeProbe(dst.GetStartupProbe(), src.GetStartupProbe())
	dst.ReadinessProbe = deepMergeProbe(dst.GetReadinessProbe(), src.GetReadinessProbe())
	dst.LivenessProbe = deepMergeProbe(dst.GetLivenessProbe(), src.GetLivenessProbe())
	dst.TopologySpreadConstraints = DeepMergeSlices(dst.GetTopologySpreadConstraints(), src.GetTopologySpreadConstraints())
	dst.ExtraVolumes = DeepMergeSlices(dst.GetExtraVolumes(), src.GetExtraVolumes())
	dst.PriorityClassName = MergePointers(dst.PriorityClassName, src.PriorityClassName)

	return dst
}

func deepMergeGracefulShutdown(dst, src *kgateway.GracefulShutdownSpec) *kgateway.GracefulShutdownSpec {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Enabled = MergePointers(dst.Enabled, src.Enabled)
	dst.SleepTimeSeconds = MergePointers(dst.SleepTimeSeconds, src.SleepTimeSeconds)

	return dst
}

func deepMergeService(dst, src *kgateway.Service) *kgateway.Service {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	if src.GetType() != nil {
		dst.Type = src.GetType()
	}

	if src.GetClusterIP() != nil {
		dst.ClusterIP = src.GetClusterIP()
	}

	if src.GetLoadBalancerClass() != nil {
		dst.LoadBalancerClass = src.GetLoadBalancerClass()
	}

	dst.ExtraLabels = DeepMergeMaps(dst.GetExtraLabels(), src.GetExtraLabels())
	dst.ExtraAnnotations = DeepMergeMaps(dst.GetExtraAnnotations(), src.GetExtraAnnotations())
	dst.Ports = DeepMergeSlices(dst.GetPorts(), src.GetPorts())
	if src.GetExternalTrafficPolicy() != nil {
		dst.ExternalTrafficPolicy = src.GetExternalTrafficPolicy()
	}

	return dst
}

func deepMergeServiceAccount(dst, src *kgateway.ServiceAccount) *kgateway.ServiceAccount {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.ExtraLabels = DeepMergeMaps(dst.GetExtraLabels(), src.GetExtraLabels())
	dst.ExtraAnnotations = DeepMergeMaps(dst.GetExtraAnnotations(), src.GetExtraAnnotations())

	return dst
}

func deepMergeSdsContainer(dst, src *kgateway.SdsContainer) *kgateway.SdsContainer {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Image = DeepMergeImage(dst.GetImage(), src.GetImage())
	dst.SecurityContext = DeepMergeSecurityContext(dst.GetSecurityContext(), src.GetSecurityContext())
	dst.Resources = DeepMergeResourceRequirements(dst.GetResources(), src.GetResources())
	dst.Bootstrap = deepMergeSdsBootstrap(dst.GetBootstrap(), src.GetBootstrap())

	return dst
}

func deepMergeSdsBootstrap(dst, src *kgateway.SdsBootstrap) *kgateway.SdsBootstrap {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	if src.GetLogLevel() != nil {
		dst.LogLevel = src.GetLogLevel()
	}

	return dst
}

func deepMergeIstioIntegration(dst, src *kgateway.IstioIntegration) *kgateway.IstioIntegration {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.IstioProxyContainer = deepMergeIstioContainer(dst.GetIstioProxyContainer(), src.GetIstioProxyContainer())
	dst.CustomSidecars = mergeCustomSidecars(dst.GetCustomSidecars(), src.GetCustomSidecars()) //nolint:staticcheck // deprecated but still needs merge support

	return dst
}

func deepMergeIstioContainer(dst, src *kgateway.IstioContainer) *kgateway.IstioContainer {
	// nil src override means just use dst
	if src == nil {
		return dst
	}
	if dst == nil {
		return src
	}

	dst.Image = DeepMergeImage(dst.GetImage(), src.GetImage())
	dst.SecurityContext = DeepMergeSecurityContext(dst.GetSecurityContext(), src.GetSecurityContext())
	dst.Resources = DeepMergeResourceRequirements(dst.GetResources(), src.GetResources())

	if src.GetLogLevel() != nil {
		dst.LogLevel = src.GetLogLevel()
	}

	if src.GetIstioDiscoveryAddress() != nil {
		dst.IstioDiscoveryAddress = src.GetIstioDiscoveryAddress()
	}

	if src.GetIstioMetaMeshId() != nil {
		dst.IstioMetaMeshId = src.GetIstioMetaMeshId()
	}

	if src.GetIstioMetaClusterId() != nil {
		dst.IstioMetaClusterId = src.GetIstioMetaClusterId()
	}

	return dst
}

func deepMergeEnvoyContainer(dst, src *kgateway.EnvoyContainer) *kgateway.EnvoyContainer {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Bootstrap = deepMergeEnvoyBootstrap(dst.GetBootstrap(), src.GetBootstrap())
	dst.Image = DeepMergeImage(dst.GetImage(), src.GetImage())
	dst.SecurityContext = DeepMergeSecurityContext(dst.GetSecurityContext(), src.GetSecurityContext())
	dst.Resources = DeepMergeResourceRequirements(dst.GetResources(), src.GetResources())
	dst.Env = DeepMergeSlices(dst.GetEnv(), src.GetEnv())
	dst.ExtraVolumeMounts = DeepMergeSlices(dst.ExtraVolumeMounts, src.ExtraVolumeMounts)

	return dst
}

func DeepMergeImage(dst, src *kgateway.Image) *kgateway.Image {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	if src.GetRegistry() != nil {
		dst.Registry = src.GetRegistry()
	}

	if src.GetRepository() != nil {
		dst.Repository = src.GetRepository()
	}

	if src.GetTag() != nil {
		dst.Tag = src.GetTag()
	}

	if src.GetDigest() != nil {
		dst.Digest = src.GetDigest()
	}

	if src.GetPullPolicy() != nil {
		dst.PullPolicy = src.GetPullPolicy()
	}

	return dst
}

func deepMergeEnvoyBootstrap(dst, src *kgateway.EnvoyBootstrap) *kgateway.EnvoyBootstrap {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}
	if src.GetLogLevel() != nil {
		dst.LogLevel = src.GetLogLevel()
	}

	dst.ComponentLogLevels = DeepMergeMaps(dst.GetComponentLogLevels(), src.GetComponentLogLevels())
	dst.DnsResolver = deepMergeDnsResolver(dst.GetDnsResolver(), src.GetDnsResolver())

	return dst
}

func deepMergeDnsResolver(dst, src *kgateway.DnsResolver) *kgateway.DnsResolver {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}
	if src.GetUdpMaxQueries() != nil {
		dst.UdpMaxQueries = src.GetUdpMaxQueries()
	}

	return dst
}

func deepMergeDeployment(dst, src *kgateway.ProxyDeployment) *kgateway.ProxyDeployment {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Replicas = MergePointers(dst.GetReplicas(), src.GetReplicas())
	dst.Strategy = MergePointers(dst.Strategy, src.Strategy)

	return dst
}
