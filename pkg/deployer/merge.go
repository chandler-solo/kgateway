package deployer

import (
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// This file contains shared merge utilities used by both Envoy and Agentgateway
// parameter merging. Functions that work with generic types or corev1 types
// belong here. Envoy-specific merge functions (those using kgateway.* types)
// are in envoy_merge.go.

// MergePointers will decide whether to use dst or src without dereferencing or recursing
func MergePointers[T any](dst, src *T) *T {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	// given non-nil src override, use that instead
	return src
}

// DeepMergeMaps will use dst if src is nil, src if dest is nil, or add all entries from src into dst
// if neither are nil
func DeepMergeMaps[keyT comparable, valT any](dst, src map[keyT]valT) map[keyT]valT {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil || len(src) == 0 {
		return src
	}

	maps.Copy(dst, src)
	return dst
}

func DeepMergeSlices[T any](dst, src []T) []T {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil || len(src) == 0 {
		return src
	}

	dst = append(dst, src...)

	return dst
}

func OverrideSlices[T any](dst, src []T) []T {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	return src
}

// MergeComparable checks against the zero value and returns dst if src is zero.
func MergeComparable[T comparable](dst, src T) T {
	var t T
	if src == t {
		return dst
	}

	return src
}

func DeepMergeResourceRequirements(dst, src *corev1.ResourceRequirements) *corev1.ResourceRequirements {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Limits = DeepMergeMaps(dst.Limits, src.Limits)
	dst.Requests = DeepMergeMaps(dst.Requests, src.Requests)

	return dst
}

func DeepMergeSecurityContext(dst, src *corev1.SecurityContext) *corev1.SecurityContext {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Capabilities = deepMergeCapabilities(dst.Capabilities, src.Capabilities)
	dst.SELinuxOptions = deepMergeSELinuxOptions(dst.SELinuxOptions, src.SELinuxOptions)
	dst.WindowsOptions = deepMergeWindowsSecurityContextOptions(dst.WindowsOptions, src.WindowsOptions)
	dst.RunAsUser = MergePointers(dst.RunAsUser, src.RunAsUser)
	dst.RunAsGroup = MergePointers(dst.RunAsGroup, src.RunAsGroup)
	dst.RunAsNonRoot = MergePointers(dst.RunAsNonRoot, src.RunAsNonRoot)
	dst.Privileged = MergePointers(dst.Privileged, src.Privileged)
	dst.ReadOnlyRootFilesystem = MergePointers(dst.ReadOnlyRootFilesystem, src.ReadOnlyRootFilesystem)
	dst.AllowPrivilegeEscalation = MergePointers(dst.AllowPrivilegeEscalation, src.AllowPrivilegeEscalation)
	dst.ProcMount = MergePointers(dst.ProcMount, src.ProcMount)
	dst.SeccompProfile = deepMergeSeccompProfile(dst.SeccompProfile, src.SeccompProfile)

	return dst
}

func deepMergeCapabilities(dst, src *corev1.Capabilities) *corev1.Capabilities {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Add = DeepMergeSlices(dst.Add, src.Add)
	dst.Drop = DeepMergeSlices(dst.Drop, src.Drop)

	return dst
}

func DeepMergeAffinity(dst, src *corev1.Affinity) *corev1.Affinity {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.NodeAffinity = deepMergeNodeAffinity(dst.NodeAffinity, src.NodeAffinity)
	dst.PodAffinity = deepMergePodAffinity(dst.PodAffinity, src.PodAffinity)
	dst.PodAntiAffinity = deepMergePodAntiAffinity(dst.PodAntiAffinity, src.PodAntiAffinity)

	return dst
}

func deepMergeNodeAffinity(dst, src *corev1.NodeAffinity) *corev1.NodeAffinity {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.RequiredDuringSchedulingIgnoredDuringExecution = deepMergeNodeSelector(dst.RequiredDuringSchedulingIgnoredDuringExecution, src.RequiredDuringSchedulingIgnoredDuringExecution)
	dst.PreferredDuringSchedulingIgnoredDuringExecution = DeepMergeSlices(dst.PreferredDuringSchedulingIgnoredDuringExecution, src.PreferredDuringSchedulingIgnoredDuringExecution)

	return dst
}

func deepMergeNodeSelector(dst, src *corev1.NodeSelector) *corev1.NodeSelector {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.NodeSelectorTerms = DeepMergeSlices(dst.NodeSelectorTerms, src.NodeSelectorTerms)

	return dst
}

func deepMergePodAffinity(dst, src *corev1.PodAffinity) *corev1.PodAffinity {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.RequiredDuringSchedulingIgnoredDuringExecution = DeepMergeSlices(dst.RequiredDuringSchedulingIgnoredDuringExecution, src.RequiredDuringSchedulingIgnoredDuringExecution)
	dst.PreferredDuringSchedulingIgnoredDuringExecution = DeepMergeSlices(dst.PreferredDuringSchedulingIgnoredDuringExecution, src.PreferredDuringSchedulingIgnoredDuringExecution)

	return dst
}

func deepMergePodAntiAffinity(dst, src *corev1.PodAntiAffinity) *corev1.PodAntiAffinity {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.RequiredDuringSchedulingIgnoredDuringExecution = DeepMergeSlices(dst.RequiredDuringSchedulingIgnoredDuringExecution, src.RequiredDuringSchedulingIgnoredDuringExecution)
	dst.PreferredDuringSchedulingIgnoredDuringExecution = DeepMergeSlices(dst.PreferredDuringSchedulingIgnoredDuringExecution, src.PreferredDuringSchedulingIgnoredDuringExecution)

	return dst
}

func deepMergePodSecurityContext(dst, src *corev1.PodSecurityContext) *corev1.PodSecurityContext {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.SELinuxOptions = deepMergeSELinuxOptions(dst.SELinuxOptions, src.SELinuxOptions)
	dst.WindowsOptions = deepMergeWindowsSecurityContextOptions(dst.WindowsOptions, src.WindowsOptions)
	dst.RunAsUser = MergePointers(dst.RunAsUser, src.RunAsUser)
	dst.RunAsGroup = MergePointers(dst.RunAsGroup, src.RunAsGroup)
	dst.RunAsNonRoot = MergePointers(dst.RunAsNonRoot, src.RunAsNonRoot)
	dst.SupplementalGroups = DeepMergeSlices(dst.SupplementalGroups, src.SupplementalGroups)
	dst.FSGroup = MergePointers(dst.FSGroup, src.FSGroup)
	dst.Sysctls = DeepMergeSlices(dst.Sysctls, src.Sysctls)
	dst.FSGroupChangePolicy = MergePointers(dst.FSGroupChangePolicy, src.FSGroupChangePolicy)
	dst.SeccompProfile = deepMergeSeccompProfile(dst.SeccompProfile, src.SeccompProfile)

	return dst
}

func deepMergeSELinuxOptions(dst, src *corev1.SELinuxOptions) *corev1.SELinuxOptions {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.User = MergeComparable(dst.User, src.User)
	dst.Role = MergeComparable(dst.Role, src.Role)
	dst.Type = MergeComparable(dst.Type, src.Type)
	dst.Level = MergeComparable(dst.Level, src.Level)

	return dst
}

func deepMergeWindowsSecurityContextOptions(dst, src *corev1.WindowsSecurityContextOptions) *corev1.WindowsSecurityContextOptions {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.GMSACredentialSpecName = MergePointers(dst.GMSACredentialSpec, src.GMSACredentialSpec)
	dst.GMSACredentialSpec = MergePointers(dst.GMSACredentialSpec, src.GMSACredentialSpec)
	dst.RunAsUserName = MergePointers(dst.RunAsUserName, src.RunAsUserName)
	dst.HostProcess = MergePointers(dst.HostProcess, src.HostProcess)

	return dst
}

func deepMergeSeccompProfile(dst, src *corev1.SeccompProfile) *corev1.SeccompProfile {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Type = MergeComparable(dst.Type, src.Type)
	dst.LocalhostProfile = MergePointers(dst.LocalhostProfile, src.LocalhostProfile)

	return dst
}

func deepMergeProbe(dst, src *corev1.Probe) *corev1.Probe {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.ProbeHandler = deepMergeProbeHandler(dst.ProbeHandler, src.ProbeHandler)
	dst.InitialDelaySeconds = MergeComparable(dst.InitialDelaySeconds, src.InitialDelaySeconds)
	dst.TimeoutSeconds = MergeComparable(dst.TimeoutSeconds, src.TimeoutSeconds)
	dst.PeriodSeconds = MergeComparable(dst.PeriodSeconds, src.PeriodSeconds)
	dst.SuccessThreshold = MergeComparable(dst.SuccessThreshold, src.SuccessThreshold)
	dst.FailureThreshold = MergeComparable(dst.FailureThreshold, src.FailureThreshold)
	dst.TerminationGracePeriodSeconds = MergePointers(dst.TerminationGracePeriodSeconds, src.TerminationGracePeriodSeconds)

	return dst
}

func deepMergeProbeHandler(dst, src corev1.ProbeHandler) corev1.ProbeHandler {
	srcHasExecAction := src.Exec != nil
	srcHasHTTPGetAction := src.HTTPGet != nil
	srcHasTCPSocketAction := src.TCPSocket != nil
	srcHasGRPCAction := src.GRPC != nil
	srcHasAction := srcHasExecAction || srcHasHTTPGetAction || srcHasTCPSocketAction || srcHasGRPCAction
	if srcHasAction {
		// Reset the dest so it does not conflict with the src Action as there should only be one Action defined per probe
		dst.Exec = nil
		dst.HTTPGet = nil
		dst.TCPSocket = nil
		dst.GRPC = nil
	}
	// kube-builder validation ensures that the src has only one action
	dst.Exec = deepMergeExecAction(dst.Exec, src.Exec)
	dst.HTTPGet = deepMergeHTTPGetAction(dst.HTTPGet, src.HTTPGet)
	dst.TCPSocket = deepMergeTCPSocketAction(dst.TCPSocket, src.TCPSocket)
	dst.GRPC = deepMergeGRPCAction(dst.GRPC, src.GRPC)

	return dst
}

func deepMergeExecAction(dst, src *corev1.ExecAction) *corev1.ExecAction {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	// Don't merge the command string as that can break the entire probe
	dst.Command = OverrideSlices(dst.Command, src.Command)

	return dst
}

func deepMergeHTTPGetAction(dst, src *corev1.HTTPGetAction) *corev1.HTTPGetAction {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Path = MergeComparable(dst.Path, src.Path)
	dst.Port = mergeIntOrString(dst.Port, src.Port)
	dst.Host = MergeComparable(dst.Host, src.Host)
	dst.Scheme = MergeComparable(dst.Scheme, src.Scheme)
	dst.HTTPHeaders = DeepMergeSlices(dst.HTTPHeaders, src.HTTPHeaders)

	return dst
}

func mergeIntOrString(dst, src intstr.IntOrString) intstr.IntOrString {
	// Do not deep merge as this can cause a conflict between the name and number of the port to access on the container
	return MergeComparable(dst, src)
}

func deepMergeTCPSocketAction(dst, src *corev1.TCPSocketAction) *corev1.TCPSocketAction {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Port = mergeIntOrString(dst.Port, src.Port)
	dst.Host = MergeComparable(dst.Host, src.Host)

	return dst
}

func deepMergeGRPCAction(dst, src *corev1.GRPCAction) *corev1.GRPCAction {
	// nil src override means just use dst
	if src == nil {
		return dst
	}

	if dst == nil {
		return src
	}

	dst.Port = MergeComparable(dst.Port, src.Port)
	dst.Service = MergePointers(dst.Service, src.Service)

	return dst
}

// mergeCustomSidecars will decide whether to use dst or src custom sidecar containers
func mergeCustomSidecars(dst, src []corev1.Container) []corev1.Container {
	// nil src override means just use dst
	if len(src) == 0 {
		return dst
	}

	// given non-nil src override, use that instead
	return src
}
