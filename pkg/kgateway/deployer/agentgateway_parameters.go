package deployer

import (
	"context"
	"fmt"
	"maps"

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

// ApplyToHelmValues applies the AgentgatewayParameters configs to the helm
// values.  This is called before rendering the helm chart. (We render a helm
// chart, but we do not use helm beyond that point.)
func (a *AgentgatewayParametersApplier) ApplyToHelmValues(vals *deployer.HelmConfig) {
	if a.params == nil || vals == nil || vals.Gateway == nil {
		return
	}

	configs := a.params.Spec.AgentgatewayParametersConfigs

	if configs.Image != nil {
		if vals.Gateway.Image == nil {
			vals.Gateway.Image = &deployer.HelmImage{}
		}
		if configs.Image.Registry != nil {
			vals.Gateway.Image.Registry = configs.Image.Registry
		}
		if configs.Image.Repository != nil {
			vals.Gateway.Image.Repository = configs.Image.Repository
		}
		if configs.Image.Tag != nil {
			vals.Gateway.Image.Tag = configs.Image.Tag
		}
		if configs.Image.Digest != nil {
			vals.Gateway.Image.Digest = configs.Image.Digest
		}
		if configs.Image.PullPolicy != nil {
			vals.Gateway.Image.PullPolicy = (*string)(configs.Image.PullPolicy)
		}
	}

	if configs.Resources != nil {
		vals.Gateway.Resources = configs.Resources
	}

	// Apply environment variables
	// The Helm template handles deduplication - if user specifies an env var,
	// the template skips the default with the same name.
	if len(configs.Env) > 0 {
		vals.Gateway.Env = append(vals.Gateway.Env, configs.Env...)
	}

	if configs.Logging != nil {
		if configs.Logging.Level != "" {
			vals.Gateway.LogLevel = &configs.Logging.Level
		}
	}

	if configs.Shutdown != nil {
		if configs.Shutdown.MaxSeconds != nil {
			vals.Gateway.TerminationGracePeriodSeconds = configs.Shutdown.MaxSeconds
		}
		if configs.Shutdown.MinSeconds != nil {
			if vals.Gateway.GracefulShutdown == nil {
				vals.Gateway.GracefulShutdown = &kgateway.GracefulShutdownSpec{}
			}
			vals.Gateway.GracefulShutdown.SleepTimeSeconds = configs.Shutdown.MinSeconds
		}
	}

	if len(configs.Labels) > 0 {
		if vals.Gateway.ExtraPodLabels == nil {
			vals.Gateway.ExtraPodLabels = make(map[string]string)
		}
		maps.Copy(vals.Gateway.ExtraPodLabels, configs.Labels)
	}
	if len(configs.Annotations) > 0 {
		if vals.Gateway.ExtraPodAnnotations == nil {
			vals.Gateway.ExtraPodAnnotations = make(map[string]string)
		}
		maps.Copy(vals.Gateway.ExtraPodAnnotations, configs.Annotations)
	}
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

type agentgatewayParametersHelmValuesGenerator struct {
	agwParamClient kclient.Client[*agentgateway.AgentgatewayParameters]
	gwClassClient  kclient.Client[*gwv1.GatewayClass]
	inputs         *deployer.Inputs
}

func newAgentgatewayParametersHelmValuesGenerator(cli apiclient.Client, inputs *deployer.Inputs) *agentgatewayParametersHelmValuesGenerator {
	return &agentgatewayParametersHelmValuesGenerator{
		agwParamClient: kclient.NewFilteredDelayed[*agentgateway.AgentgatewayParameters](cli, wellknown.AgentgatewayParametersGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		gwClassClient:  kclient.NewFilteredDelayed[*gwv1.GatewayClass](cli, wellknown.GatewayClassGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		inputs:         inputs,
	}
}

// GetValues returns helm values derived from AgentgatewayParameters.
func (g *agentgatewayParametersHelmValuesGenerator) GetValues(ctx context.Context, obj client.Object) (map[string]any, error) {
	gw, ok := obj.(*gwv1.Gateway)
	if !ok {
		return nil, fmt.Errorf("expected a Gateway resource, got %s", obj.GetObjectKind().GroupVersionKind().String())
	}

	agwp, err := g.GetAgentgatewayParametersForGateway(gw)
	if err != nil {
		return nil, err
	}

	vals, err := g.getDefaultAgentgatewayHelmValues(gw)
	if err != nil {
		return nil, err
	}

	if agwp != nil {
		applier := NewAgentgatewayParametersApplier(agwp)
		applier.ApplyToHelmValues(vals)
	}

	if g.inputs.ControlPlane.XdsTLS {
		if err := injectXdsCACertificate(g.inputs.ControlPlane.XdsTlsCaPath, vals); err != nil {
			return nil, fmt.Errorf("failed to inject xDS CA certificate: %w", err)
		}
	}

	var jsonVals map[string]any
	err = deployer.JsonConvert(vals, &jsonVals)
	return jsonVals, err
}

// GetAgentgatewayParametersForGateway resolves the effective AgentgatewayParameters for the Gateway.
// Gateway-level parametersRef completely replaces GatewayClass-level parametersRef.
func (g *agentgatewayParametersHelmValuesGenerator) GetAgentgatewayParametersForGateway(gw *gwv1.Gateway) (*agentgateway.AgentgatewayParameters, error) {
	// First, check if Gateway has its own parametersRef - this replaces any parametersRef on the GatewayClass
	if gw.Spec.Infrastructure != nil && gw.Spec.Infrastructure.ParametersRef != nil {
		ref := gw.Spec.Infrastructure.ParametersRef

		// Check for AgentgatewayParameters
		if ref.Group == agentgateway.GroupName && ref.Kind == gwv1.Kind(wellknown.AgentgatewayParametersGVK.Kind) {
			agwp := g.agwParamClient.Get(ref.Name, gw.GetNamespace())
			if agwp == nil {
				return nil, fmt.Errorf("AgentgatewayParameters %s/%s not found for Gateway %s/%s",
					gw.GetNamespace(), ref.Name, gw.GetNamespace(), gw.GetName())
			}
			return agwp, nil
		}

		// GatewayParameters is not supported for agentgateway Gateways
		return nil, fmt.Errorf("infrastructure.parametersRef on Gateway %s/%s references unsupported type: group=%s kind=%s; use AgentgatewayParameters instead",
			gw.GetNamespace(), gw.GetName(), ref.Group, ref.Kind)
	}

	// Fall back to GatewayClass parametersRef
	gwc := g.gwClassClient.Get(string(gw.Spec.GatewayClassName), metav1.NamespaceNone)
	if gwc == nil || gwc.Spec.ParametersRef == nil {
		return nil, nil
	}

	ref := gwc.Spec.ParametersRef

	// Check for AgentgatewayParameters on GatewayClass
	if ref.Group == agentgateway.GroupName && string(ref.Kind) == wellknown.AgentgatewayParametersGVK.Kind {
		agwpNamespace := ""
		if ref.Namespace != nil {
			agwpNamespace = string(*ref.Namespace)
		}
		agwp := g.agwParamClient.Get(ref.Name, agwpNamespace)
		if agwp == nil {
			return nil, fmt.Errorf("AgentgatewayParameters %s/%s not found for GatewayClass %s",
				agwpNamespace, ref.Name, gwc.GetName())
		}
		return agwp, nil
	}

	// GatewayParameters is not supported for agentgateway GatewayClasses
	return nil, fmt.Errorf("GatewayClass %s references unsupported parametersRef type: group=%s kind=%s; use AgentgatewayParameters instead",
		gwc.GetName(), ref.Group, ref.Kind)
}

func (g *agentgatewayParametersHelmValuesGenerator) GetCacheSyncHandlers() []cache.InformerSynced {
	return []cache.InformerSynced{g.agwParamClient.HasSynced, g.gwClassClient.HasSynced}
}

func (g *agentgatewayParametersHelmValuesGenerator) getDefaultAgentgatewayHelmValues(gw *gwv1.Gateway) (*deployer.HelmConfig, error) {
	irGW := deployer.GetGatewayIR(gw, g.inputs.CommonCollections)
	ports := deployer.GetPortsValues(irGW, nil, true) // true = agentgateway
	if len(ports) == 0 {
		return nil, ErrNoValidPorts
	}

	gtw := &deployer.HelmGateway{
		DataPlaneType:    deployer.DataPlaneAgentgateway,
		Name:             &gw.Name,
		GatewayName:      &gw.Name,
		GatewayNamespace: &gw.Namespace,
		GatewayClassName: func() *string {
			s := string(gw.Spec.GatewayClassName)
			return &s
		}(),
		Ports: ports,
		Xds: &deployer.HelmXds{
			Host: &g.inputs.ControlPlane.XdsHost,
			Port: &g.inputs.ControlPlane.XdsPort,
			Tls: &deployer.HelmXdsTls{
				Enabled: func() *bool { b := g.inputs.ControlPlane.XdsTLS; return &b }(),
				CaCert:  &g.inputs.ControlPlane.XdsTlsCaPath,
			},
		},
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
	gtw.DataPlaneType = deployer.DataPlaneAgentgateway

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

	return &deployer.HelmConfig{Gateway: gtw}, nil
}
