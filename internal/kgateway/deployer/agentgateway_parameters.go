package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"

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
	"github.com/kgateway-dev/kgateway/v2/internal/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer/strategicpatch"
)

type agentgatewayParameters struct {
	agwParamClient kclient.Client[*agentgateway.AgentgatewayParameters]
	gwClassClient  kclient.Client[*gwv1.GatewayClass]
	// DLC gwp client?
	inputs *deployer.Inputs
}

func newAgentgatewayParameters(cli apiclient.Client, inputs *deployer.Inputs) *agentgatewayParameters {
	return &agentgatewayParameters{
		agwParamClient: kclient.NewFilteredDelayed[*agentgateway.AgentgatewayParameters](cli, wellknown.AgentgatewayParametersGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		gwClassClient:  kclient.NewFilteredDelayed[*gwv1.GatewayClass](cli, wellknown.GatewayClassGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		inputs:         inputs,
	}
}

func (a *agentgatewayParameters) GetCacheSyncHandlers() []cache.InformerSynced {
	return []cache.InformerSynced{a.agwParamClient.HasSynced, a.gwClassClient.HasSynced}
}

// GetAgentgatewayParametersForGateway returns the AgentgatewayParameters for
// the given Gateway. Be aware of GatewayParameters as well.  It first checks
// if the Gateway references an AgentgatewayParameters via
// infrastructure.parametersRef, then falls back to the GatewayClass's
// parametersRef.
func (a *agentgatewayParameters) GetAgentgatewayParametersForGateway(gw *gwv1.Gateway) (*agentgateway.AgentgatewayParameters, error) {
	if gw.Spec.Infrastructure != nil && gw.Spec.Infrastructure.ParametersRef != nil {
		ref := gw.Spec.Infrastructure.ParametersRef

		if ref.Group == agentgateway.GroupName && ref.Kind == gwv1.Kind(wellknown.AgentgatewayParametersGVK.Kind) {
			agwpName := ref.Name
			agwpNamespace := gw.GetNamespace() // AgentgatewayParameters must be in the same namespace

			agwp := a.agwParamClient.Get(agwpName, agwpNamespace)
			if agwp == nil {
				return nil, fmt.Errorf("AgentgatewayParameters %s/%s not found for Gateway %s/%s",
					agwpNamespace, agwpName, gw.GetNamespace(), gw.GetName())
			}

			slog.Debug("found AgentgatewayParameters for Gateway",
				"agwp_name", agwpName,
				"agwp_namespace", agwpNamespace,
				"gateway_name", gw.GetName(),
				"gateway_namespace", gw.GetNamespace(),
			)
			return agwp, nil
		}
	}

	return a.getAgentgatewayParametersFromGatewayClass(gw)
}

func (a *agentgatewayParameters) getAgentgatewayParametersFromGatewayClass(gw *gwv1.Gateway) (*agentgateway.AgentgatewayParameters, error) {
	gwc, err := a.getGatewayClassFromGateway(gw)
	if err != nil {
		return nil, err
	}

	if gwc.Spec.ParametersRef == nil {
		slog.Debug("no parametersRef on GatewayClass",
			"gatewayclass_name", gwc.GetName(),
		)
		return nil, nil
	}

	ref := gwc.Spec.ParametersRef

	if ref.Group != agentgateway.GroupName || string(ref.Kind) != wellknown.AgentgatewayParametersGVK.Kind {
		slog.Debug("the GatewayClass parametersRef is not an AgentgatewayParameters",
			"gatewayclass_name", gwc.GetName(),
			"group", ref.Group,
			"kind", ref.Kind,
		)
		return nil, nil
	}

	agwpName := ref.Name
	agwpNamespace := ""
	if ref.Namespace != nil {
		agwpNamespace = string(*ref.Namespace)
	}

	agwp := a.agwParamClient.Get(agwpName, agwpNamespace)
	if agwp == nil {
		return nil, fmt.Errorf("AgentgatewayParameters %s/%s not found for GatewayClass %s",
			agwpNamespace, agwpName, gwc.GetName())
	}

	slog.Debug("found AgentgatewayParameters from GatewayClass",
		"agwp_name", agwpName,
		"agwp_namespace", agwpNamespace,
		"gatewayclass_name", gwc.GetName(),
	)
	return agwp, nil
}

func (a *agentgatewayParameters) getGatewayClassFromGateway(gw *gwv1.Gateway) (*gwv1.GatewayClass, error) {
	if gw == nil {
		return nil, errors.New("nil Gateway")
	}
	if gw.Spec.GatewayClassName == "" {
		return nil, errors.New("gatewayClassName must not be empty")
	}

	gwc := a.gwClassClient.Get(string(gw.Spec.GatewayClassName), metav1.NamespaceNone)
	if gwc == nil {
		return nil, fmt.Errorf("failed to get GatewayClass %s for Gateway %s/%s",
			gw.Spec.GatewayClassName, gw.GetNamespace(), gw.GetName())
	}

	return gwc, nil
}

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
	agwParams     *agentgatewayParameters
	gwParamClient kclient.Client[*kgateway.GatewayParameters]
	inputs        *deployer.Inputs
}

func newAgentgatewayParametersHelmValuesGenerator(cli apiclient.Client, inputs *deployer.Inputs) *agentgatewayParametersHelmValuesGenerator {
	return &agentgatewayParametersHelmValuesGenerator{
		agwParams:     newAgentgatewayParameters(cli, inputs),
		gwParamClient: kclient.NewFilteredDelayed[*kgateway.GatewayParameters](cli, wellknown.GatewayParametersGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		inputs:        inputs,
	}
}

// GetValues returns helm values derived from AgentgatewayParameters.
func (g *agentgatewayParametersHelmValuesGenerator) GetValues(ctx context.Context, obj client.Object) (map[string]any, error) {
	gw, ok := obj.(*gwv1.Gateway)
	if !ok {
		return nil, fmt.Errorf("expected a Gateway resource, got %s", obj.GetObjectKind().GroupVersionKind().String())
	}

	agwp, err := g.agwParams.GetAgentgatewayParametersForGateway(gw)
	if err != nil {
		return nil, err
	}

	// Check if a GatewayParameters is referenced (for backwards compatibility
	// since GatewayParameters predates AgentgatewayParameters)
	gwp := g.getGatewayParametersForGateway(gw)

	omitDefaultSecurityContext := g.shouldOmitDefaultSecurityContext(gwp)

	vals, err := g.getDefaultAgentgatewayHelmValues(gw, omitDefaultSecurityContext)
	if err != nil {
		return nil, err
	}

	if gwp != nil {
		g.applyGatewayParametersToHelmValues(gwp, vals)
	}

	// Apply AgentgatewayParameters configs to helm values (these take precedence over GWP TODO(chandler): DLC: but you can't have both)
	if agwp != nil {
		applier := NewAgentgatewayParametersApplier(agwp)
		applier.ApplyToHelmValues(vals)
	}

	if g.inputs.ControlPlane.XdsTLS {
		if err := g.injectXdsCACertificate(vals); err != nil {
			return nil, fmt.Errorf("failed to inject xDS CA certificate: %w", err)
		}
	}

	var jsonVals map[string]any
	err = deployer.JsonConvert(vals, &jsonVals)
	return jsonVals, err
}

// getGatewayParametersForGateway returns the GatewayParameters for the given
// Gateway, if any. (AgentgatewayParameters and GatewayParameters are both
// supported, but mixing GVKs is inelegant and so AgentgatewayParameters is
// preferred.)
func (g *agentgatewayParametersHelmValuesGenerator) getGatewayParametersForGateway(gw *gwv1.Gateway) *kgateway.GatewayParameters {
	// Check if the Gateway's infrastructure references a GatewayParameters
	if gw.Spec.Infrastructure != nil && gw.Spec.Infrastructure.ParametersRef != nil {
		ref := gw.Spec.Infrastructure.ParametersRef
		if ref.Group == kgateway.GroupName && ref.Kind == gwv1.Kind(wellknown.GatewayParametersGVK.Kind) {
			return g.gwParamClient.Get(ref.Name, gw.GetNamespace())
		}
	}

	// Check if the GatewayClass references a GatewayParameters
	gwc := g.agwParams.gwClassClient.Get(string(gw.Spec.GatewayClassName), metav1.NamespaceNone)
	if gwc != nil && gwc.Spec.ParametersRef != nil {
		ref := gwc.Spec.ParametersRef
		if ref.Group == kgateway.GroupName && string(ref.Kind) == wellknown.GatewayParametersGVK.Kind {
			gwpNamespace := ""
			if ref.Namespace != nil {
				gwpNamespace = string(*ref.Namespace)
			}
			return g.gwParamClient.Get(ref.Name, gwpNamespace)
		}
	}

	return nil
}

// shouldOmitDefaultSecurityContext checks if the Gateway references a GatewayParameters
// with omitDefaultSecurityContext set to true.
func (g *agentgatewayParametersHelmValuesGenerator) shouldOmitDefaultSecurityContext(gwp *kgateway.GatewayParameters) bool {
	if gwp != nil && gwp.Spec.Kube != nil {
		return ptr.Deref(gwp.Spec.Kube.OmitDefaultSecurityContext, false)
	}
	return false
}

// applyGatewayParametersToHelmValues applies relevant fields from
// GatewayParameters to helm values.  This provides backward compatibility for
// users who configure agentgateway using GatewayParameters, not
// AgentgatewayParameters.
func (g *agentgatewayParametersHelmValuesGenerator) applyGatewayParametersToHelmValues(gwp *kgateway.GatewayParameters, vals *deployer.HelmConfig) {
	if gwp == nil || gwp.Spec.Kube == nil || vals.Gateway == nil {
		return
	}

	podConfig := gwp.Spec.Kube.GetPodTemplate()

	if extraAnnotations := podConfig.GetExtraAnnotations(); len(extraAnnotations) > 0 {
		if vals.Gateway.ExtraPodAnnotations == nil {
			vals.Gateway.ExtraPodAnnotations = make(map[string]string)
		}
		maps.Copy(vals.Gateway.ExtraPodAnnotations, extraAnnotations)
	}
	if extraLabels := podConfig.GetExtraLabels(); len(extraLabels) > 0 {
		if vals.Gateway.ExtraPodLabels == nil {
			vals.Gateway.ExtraPodLabels = make(map[string]string)
		}
		maps.Copy(vals.Gateway.ExtraPodLabels, extraLabels)
	}

	svcConfig := gwp.Spec.Kube.GetService()
	vals.Gateway.Service = deployer.GetServiceValues(svcConfig)

	svcAccountConfig := gwp.Spec.Kube.GetServiceAccount()
	vals.Gateway.ServiceAccount = deployer.GetServiceAccountValues(svcAccountConfig)
}

func (g *agentgatewayParametersHelmValuesGenerator) GetCacheSyncHandlers() []cache.InformerSynced {
	handlers := g.agwParams.GetCacheSyncHandlers()
	return append(handlers, g.gwParamClient.HasSynced)
}

func (g *agentgatewayParametersHelmValuesGenerator) GetAgentgatewayParametersForGateway(gw *gwv1.Gateway) (*agentgateway.AgentgatewayParameters, error) {
	return g.agwParams.GetAgentgatewayParametersForGateway(gw)
}

func (g *agentgatewayParametersHelmValuesGenerator) getDefaultAgentgatewayHelmValues(gw *gwv1.Gateway, omitDefaultSecurityContext bool) (*deployer.HelmConfig, error) {
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

	if !omitDefaultSecurityContext {
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
	}

	return &deployer.HelmConfig{Gateway: gtw}, nil
}

// DLC injectXdsCACertificate reads the CA certificate from the control plane's mounted TLS Secret
// and injects it into the Helm values so it can be used by the proxy templates.
func (g *agentgatewayParametersHelmValuesGenerator) injectXdsCACertificate(vals *deployer.HelmConfig) error {
	caCertPath := g.inputs.ControlPlane.XdsTlsCaPath
	if _, err := os.Stat(caCertPath); os.IsNotExist(err) {
		return fmt.Errorf("xDS TLS is enabled but CA certificate file not found at %s. "+
			"Ensure the xDS TLS secret is properly mounted and contains ca.crt", caCertPath,
		)
	}

	caCert, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate from %s: %w", caCertPath, err)
	}
	if len(caCert) == 0 {
		return fmt.Errorf("CA certificate at %s is empty", caCertPath)
	}

	caCertStr := string(caCert)
	if vals.Gateway.Xds != nil && vals.Gateway.Xds.Tls != nil {
		vals.Gateway.Xds.Tls.CaCert = &caCertStr
	}
	if vals.Gateway.AgwXds != nil && vals.Gateway.AgwXds.Tls != nil {
		vals.Gateway.AgwXds.Tls.CaCert = &caCertStr
	}

	return nil
}
