package deployer

import (
	"context"
	"encoding/json"
	"fmt"

	"istio.io/istio/pkg/kube/kclient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/agentgateway"
	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/kgateway"
	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer/strategicpatch"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

// AgentgatewayParametersApplier applies AgentgatewayParameters configurations and overlays.
type AgentgatewayParametersApplier struct {
	params *agentgateway.AgentgatewayParameters
}

// NewAgentgatewayParametersApplier creates a new applier from the resolved parameters.
func NewAgentgatewayParametersApplier(params *agentgateway.AgentgatewayParameters) *AgentgatewayParametersApplier {
	return &AgentgatewayParametersApplier{params: params}
}

func setIfNonNil[T any](dst **T, src *T) {
	if src != nil {
		*dst = src
	}
}

func setIfNonZero[T comparable](dst *T, src T) {
	var zero T
	if src != zero {
		*dst = src
	}
}

// ApplyToHelmValues applies the AgentgatewayParameters configs to the helm
// values.  This is called before rendering the helm chart. (We render a helm
// chart, but we do not use helm beyond that point.)
func (a *AgentgatewayParametersApplier) ApplyToHelmValues(vals *deployer.AgentgatewayHelmConfig) {
	if a.params == nil || vals == nil || vals.Gateway == nil {
		return
	}

	configs := a.params.Spec.AgentgatewayParametersConfigs
	res := vals.Gateway

	// Do a manual merge of the fields.
	// Convert from agentgateway.Image to HelmImage
	if configs.Image != nil {
		if res.Image == nil {
			res.Image = &deployer.HelmImage{}
		}
		setIfNonNil(&res.Image.Tag, configs.Image.Tag)
		setIfNonNil(&res.Image.Registry, configs.Image.Registry)
		setIfNonNil(&res.Image.Repository, configs.Image.Repository)
		setIfNonNil(&res.Image.PullPolicy, configs.Image.PullPolicy)
		setIfNonNil(&res.Image.Digest, configs.Image.Digest)
	}
	setIfNonNil(&res.Resources, configs.Resources)

	// Convert RawConfig from *apiextensionsv1.JSON to map[string]any
	if configs.RawConfig != nil && len(configs.RawConfig.Raw) > 0 {
		var rawConfigMap map[string]any
		if err := json.Unmarshal(configs.RawConfig.Raw, &rawConfigMap); err == nil && rawConfigMap != nil {
			res.RawConfig = rawConfigMap
		}
	}

	// Apply logging format
	if configs.Logging != nil {
		if configs.Logging.Format != "" {
			format := string(configs.Logging.Format)
			res.LogFormat = &format
		}
	}

	// Apply explicit environment variables last so they can override logging.level.
	res.Env = mergeEnvVars(res.Env, configs.Env)
}

// mergeEnvVars merges two slices of environment variables.
// Variables in 'override' take precedence over variables in 'base' with the same name.
// The order is preserved: base vars first (minus overridden ones), then override vars.
func mergeEnvVars(base, override []corev1.EnvVar) []corev1.EnvVar {
	if len(override) == 0 {
		return base
	}
	if len(base) == 0 {
		return override
	}

	// Build a set of names in override
	overrideNames := make(map[string]struct{}, len(override))
	for _, env := range override {
		overrideNames[env.Name] = struct{}{}
	}

	// Keep base vars that are not overridden
	result := make([]corev1.EnvVar, 0, len(base)+len(override))
	for _, env := range base {
		if _, exists := overrideNames[env.Name]; !exists {
			result = append(result, env)
		}
	}

	// Append all override vars
	result = append(result, override...)
	return result
}

// ApplyOverlaysToObjects applies the strategic-merge-patch overlays to rendered k8s objects.
// This is called after rendering the helm chart.
func (a *AgentgatewayParametersApplier) ApplyOverlaysToObjects(objs []client.Object) error {
	if a.params == nil {
		return nil
	}
	applier := strategicpatch.NewOverlayApplier(a.params)
	return applier.ApplyOverlays(objs)
}

type AgentgatewayParametersHelmValuesGenerator struct {
	agwParamClient kclient.Client[*agentgateway.AgentgatewayParameters]
	gwClassClient  kclient.Client[*gwv1.GatewayClass]
	inputs         *deployer.Inputs
}

func NewAgentgatewayParametersHelmValuesGenerator(cli apiclient.Client, inputs *deployer.Inputs) *AgentgatewayParametersHelmValuesGenerator {
	return &AgentgatewayParametersHelmValuesGenerator{
		agwParamClient: kclient.NewFilteredDelayed[*agentgateway.AgentgatewayParameters](cli, wellknown.AgentgatewayParametersGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		gwClassClient:  kclient.NewFilteredDelayed[*gwv1.GatewayClass](cli, wellknown.GatewayClassGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		inputs:         inputs,
	}
}

// GetValues returns helm values derived from AgentgatewayParameters.
func (g *AgentgatewayParametersHelmValuesGenerator) GetValues(ctx context.Context, obj client.Object) (map[string]any, error) {
	gw, ok := obj.(*gwv1.Gateway)
	if !ok {
		return nil, fmt.Errorf("expected a Gateway resource, got %s", obj.GetObjectKind().GroupVersionKind().String())
	}

	resolved, err := g.resolveParameters(gw)
	if err != nil {
		return nil, err
	}

	vals, err := g.getDefaultAgentgatewayHelmValues(gw)
	if err != nil {
		return nil, err
	}

	// Apply AGWP Configs in order: GatewayClass first, then Gateway on top.
	// This allows Gateway-level configs to override GatewayClass-level configs.
	if resolved.gatewayClassAGWP != nil {
		applier := NewAgentgatewayParametersApplier(resolved.gatewayClassAGWP)
		applier.ApplyToHelmValues(vals)
	}
	if resolved.gatewayAGWP != nil {
		applier := NewAgentgatewayParametersApplier(resolved.gatewayAGWP)
		applier.ApplyToHelmValues(vals)
	}

	if g.inputs.ControlPlane.XdsTLS {
		if err := injectXdsCACertificate(g.inputs.ControlPlane.XdsTlsCaPath, vals.Gateway.AgwXds); err != nil {
			return nil, fmt.Errorf("failed to inject xDS CA certificate: %w", err)
		}
	}

	var jsonVals map[string]any
	err = deployer.AgentgatewayJsonConvert(vals, &jsonVals)
	return jsonVals, err
}

// resolvedParameters holds the resolved parameters for a Gateway, supporting
// both GatewayClass-level and Gateway-level AgentgatewayParameters.
type resolvedParameters struct {
	// gatewayClassAGWP is the AgentgatewayParameters from the GatewayClass (if any).
	gatewayClassAGWP *agentgateway.AgentgatewayParameters
	// gatewayAGWP is the AgentgatewayParameters from the Gateway (if any).
	gatewayAGWP *agentgateway.AgentgatewayParameters
}

// resolveParameters resolves the AgentgatewayParameters for the Gateway.
// It returns both GatewayClass-level and Gateway-level
// separately to support ordered overlay merging (GatewayClass first, then Gateway).
func (g *AgentgatewayParametersHelmValuesGenerator) resolveParameters(gw *gwv1.Gateway) (*resolvedParameters, error) {
	result := &resolvedParameters{}

	// Get GatewayClass parameters first
	gwc := g.gwClassClient.Get(string(gw.Spec.GatewayClassName), metav1.NamespaceNone)
	if gwc != nil && gwc.Spec.ParametersRef != nil {
		ref := gwc.Spec.ParametersRef

		// Check for AgentgatewayParameters on GatewayClass
		if ref.Group == agentgateway.GroupName && string(ref.Kind) == wellknown.AgentgatewayParametersGVK.Kind {
			agwpNamespace := ""
			if ref.Namespace != nil {
				agwpNamespace = string(*ref.Namespace)
			}
			agwp := g.agwParamClient.Get(ref.Name, agwpNamespace)
			if agwp == nil {
				return nil, fmt.Errorf("for GatewayClass %s, AgentgatewayParameters %s/%s not found",
					gwc.GetName(), agwpNamespace, ref.Name)
			}
			result.gatewayClassAGWP = agwp
		} else {
			return nil, fmt.Errorf("the GatewayClass %s references parameters of a type other than AgentgatewayParameters: %s",
				gwc.GetName(), ref.Name)
		}
	}

	// Check if Gateway has its own parametersRef
	if gw.Spec.Infrastructure != nil && gw.Spec.Infrastructure.ParametersRef != nil {
		ref := gw.Spec.Infrastructure.ParametersRef

		if ref.Group == agentgateway.GroupName && ref.Kind == gwv1.Kind(wellknown.AgentgatewayParametersGVK.Kind) {
			agwp := g.agwParamClient.Get(ref.Name, gw.GetNamespace())
			if agwp == nil {
				return nil, fmt.Errorf("AgentgatewayParameters %s/%s not found for Gateway %s/%s",
					gw.GetNamespace(), ref.Name, gw.GetNamespace(), gw.GetName())
			}
			result.gatewayAGWP = agwp
			return result, nil
		}

		return nil, fmt.Errorf("infrastructure.parametersRef on Gateway %s/%s references unsupported type: group=%s kind=%s; use AgentgatewayParameters instead",
			gw.GetNamespace(), gw.GetName(), ref.Group, ref.Kind)
	}

	return result, nil
}

func (g *AgentgatewayParametersHelmValuesGenerator) GetCacheSyncHandlers() []cache.InformerSynced {
	return []cache.InformerSynced{g.agwParamClient.HasSynced, g.gwClassClient.HasSynced}
}

// PostProcessObjects implements deployer.ObjectPostProcessor.
// It applies AgentgatewayParameters overlays to the rendered objects.
// When both GatewayClass and Gateway have AgentgatewayParameters, the overlays
// are applied in order: GatewayClass first, then Gateway on top.
func (g *AgentgatewayParametersHelmValuesGenerator) PostProcessObjects(ctx context.Context, obj client.Object, rendered []client.Object) error {
	gw, ok := obj.(*gwv1.Gateway)
	if !ok {
		return nil
	}

	resolved, err := g.GetResolvedParametersForGateway(gw)
	if err != nil {
		return nil
	}

	// Apply overlays in order: GatewayClass first, then Gateway.
	// This allows Gateway-level overlays to override GatewayClass-level overlays.
	if resolved.gatewayClassAGWP != nil {
		applier := NewAgentgatewayParametersApplier(resolved.gatewayClassAGWP)
		if err := applier.ApplyOverlaysToObjects(rendered); err != nil {
			return err
		}
	}
	if resolved.gatewayAGWP != nil {
		applier := NewAgentgatewayParametersApplier(resolved.gatewayAGWP)
		if err := applier.ApplyOverlaysToObjects(rendered); err != nil {
			return err
		}
	}

	return nil
}

// GetResolvedParametersForGateway returns both the GatewayClass-level and Gateway-level
// AgentgatewayParameters for the given Gateway. This allows callers to apply overlays
// in order (GatewayClass first, then Gateway).
func (g *AgentgatewayParametersHelmValuesGenerator) GetResolvedParametersForGateway(gw *gwv1.Gateway) (*resolvedParameters, error) {
	return g.resolveParameters(gw)
}

func (g *AgentgatewayParametersHelmValuesGenerator) getDefaultAgentgatewayHelmValues(gw *gwv1.Gateway) (*deployer.AgentgatewayHelmConfig, error) {
	irGW := deployer.GetGatewayIR(gw, g.inputs.CommonCollections)
	ports := deployer.GetPortsValues(irGW, nil, true) // true = agentgateway
	if len(ports) == 0 {
		return nil, ErrNoValidPorts
	}

	gtw := &deployer.AgentgatewayHelmGateway{
		Name:             &gw.Name,
		GatewayName:      &gw.Name,
		GatewayNamespace: &gw.Namespace,
		GatewayClassName: func() *string {
			s := string(gw.Spec.GatewayClassName)
			return &s
		}(),
		Ports: ports,
		AgwXds: &deployer.HelmXds{
			Host: &g.inputs.ControlPlane.XdsHost,
			Port: &g.inputs.ControlPlane.AgwXdsPort,
			Tls: &deployer.HelmXdsTls{
				Enabled: func() *bool { b := g.inputs.ControlPlane.XdsTLS; return &b }(),
				CaCert:  &g.inputs.ControlPlane.XdsTlsCaPath,
			},
		},
	}

	if i := gw.Spec.Infrastructure; i != nil {
		gtw.GatewayAnnotations = translateInfraMeta(i.Annotations)
		gtw.GatewayLabels = translateInfraMeta(i.Labels)
	}

	gtw.Image = &deployer.HelmImage{
		Registry:   ptr.To(deployer.AgentgatewayRegistry),
		Repository: ptr.To(deployer.AgentgatewayImage),
		Tag:        ptr.To(deployer.AgentgatewayDefaultTag),
		PullPolicy: ptr.To(""),
	}

	gtw.TerminationGracePeriodSeconds = ptr.To(int64(60))
	gtw.GracefulShutdown = &kgateway.GracefulShutdownSpec{
		Enabled:          ptr.To(true),
		SleepTimeSeconds: ptr.To(int64(10)),
	}

	gtw.ReadinessProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthz/ready",
				Port: intstr.FromInt(15021),
			},
		},
		PeriodSeconds: 10,
	}
	gtw.StartupProbe = &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/healthz/ready",
				Port: intstr.FromInt(15021),
			},
		},
		PeriodSeconds:    1,
		TimeoutSeconds:   2,
		FailureThreshold: 60,
		SuccessThreshold: 1,
	}

	gtw.PodSecurityContext = &corev1.PodSecurityContext{
		Sysctls: []corev1.Sysctl{
			{
				Name:  "net.ipv4.ip_unprivileged_port_start",
				Value: "0",
			},
		},
	}
	gtw.SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		ReadOnlyRootFilesystem: ptr.To(true),
		RunAsNonRoot:           ptr.To(true),
		RunAsUser:              ptr.To(int64(10101)),
	}

	return &deployer.AgentgatewayHelmConfig{Gateway: gtw}, nil
}
