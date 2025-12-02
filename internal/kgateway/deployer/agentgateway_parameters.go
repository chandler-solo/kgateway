package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"istio.io/istio/pkg/kube/kclient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1"
	"github.com/kgateway-dev/kgateway/v2/internal/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer/strategicpatch"
)

// agentgatewayParameters resolves AgentgatewayParameters for agentgateway-backed gateways.
type agentgatewayParameters struct {
	agwParamClient kclient.Client[*v1alpha1.AgentgatewayParameters]
	gwClassClient  kclient.Client[*gwv1.GatewayClass]
	inputs         *deployer.Inputs
}

// newAgentgatewayParameters creates a new agentgatewayParameters resolver.
func newAgentgatewayParameters(cli apiclient.Client, inputs *deployer.Inputs) *agentgatewayParameters {
	return &agentgatewayParameters{
		agwParamClient: kclient.NewFilteredDelayed[*v1alpha1.AgentgatewayParameters](cli, wellknown.AgentgatewayParametersGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		gwClassClient:  kclient.NewFilteredDelayed[*gwv1.GatewayClass](cli, wellknown.GatewayClassGVR, kclient.Filter{ObjectFilter: cli.ObjectFilter()}),
		inputs:         inputs,
	}
}

// GetCacheSyncHandlers returns the cache sync handlers for the AgentgatewayParameters controller.
func (a *agentgatewayParameters) GetCacheSyncHandlers() []cache.InformerSynced {
	return []cache.InformerSynced{a.agwParamClient.HasSynced, a.gwClassClient.HasSynced}
}

// GetAgentgatewayParametersForGateway returns the AgentgatewayParameters for the given Gateway.
// It first checks if the Gateway references an AgentgatewayParameters via infrastructure.parametersRef,
// then falls back to the GatewayClass's parametersRef.
func (a *agentgatewayParameters) GetAgentgatewayParametersForGateway(gw *gwv1.Gateway) (*v1alpha1.AgentgatewayParameters, error) {
	// First, check if the Gateway references an AgentgatewayParameters
	if gw.Spec.Infrastructure != nil && gw.Spec.Infrastructure.ParametersRef != nil {
		ref := gw.Spec.Infrastructure.ParametersRef

		// Check if it's an AgentgatewayParameters reference
		if ref.Group == v1alpha1.GroupName && ref.Kind == gwv1.Kind(wellknown.AgentgatewayParametersGVK.Kind) {
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

	// Fall back to GatewayClass's parametersRef
	return a.getAgentgatewayParametersFromGatewayClass(gw)
}

// getAgentgatewayParametersFromGatewayClass looks up AgentgatewayParameters from the GatewayClass.
func (a *agentgatewayParameters) getAgentgatewayParametersFromGatewayClass(gw *gwv1.Gateway) (*v1alpha1.AgentgatewayParameters, error) {
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

	// Check if it's an AgentgatewayParameters reference
	if ref.Group != v1alpha1.GroupName || string(ref.Kind) != wellknown.AgentgatewayParametersGVK.Kind {
		slog.Debug("GatewayClass parametersRef is not an AgentgatewayParameters",
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

// getGatewayClassFromGateway retrieves the GatewayClass for the given Gateway.
func (a *agentgatewayParameters) getGatewayClassFromGateway(gw *gwv1.Gateway) (*gwv1.GatewayClass, error) {
	if gw == nil {
		return nil, errors.New("nil Gateway")
	}
	if gw.Spec.GatewayClassName == "" {
		return nil, errors.New("GatewayClassName must not be empty")
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
	params *v1alpha1.AgentgatewayParameters
}

// NewAgentgatewayParametersApplier creates a new applier from the resolved parameters.
func NewAgentgatewayParametersApplier(params *v1alpha1.AgentgatewayParameters) *AgentgatewayParametersApplier {
	return &AgentgatewayParametersApplier{params: params}
}

// ApplyToHelmValues applies the AgentgatewayParameters configs to the helm values.
// This is called before rendering the helm chart.
func (a *AgentgatewayParametersApplier) ApplyToHelmValues(vals *deployer.HelmConfig) {
	if a.params == nil || vals == nil || vals.Gateway == nil {
		return
	}

	configs := a.params.Spec.AgentgatewayParametersConfigs

	// Apply image configuration
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

	// Apply resources
	if configs.Resources != nil {
		vals.Gateway.Resources = configs.Resources
	}

	// Apply environment variables
	if len(configs.Env) > 0 {
		vals.Gateway.Env = append(vals.Gateway.Env, configs.Env...)
	}

	// Apply logging configuration
	if configs.Logging != nil {
		if configs.Logging.Level != "" {
			vals.Gateway.LogLevel = &configs.Logging.Level
		}
	}

	// Apply common labels and annotations to pod template
	if len(configs.Labels) > 0 {
		if vals.Gateway.ExtraPodLabels == nil {
			vals.Gateway.ExtraPodLabels = make(map[string]string)
		}
		for k, v := range configs.Labels {
			vals.Gateway.ExtraPodLabels[k] = v
		}
	}
	if len(configs.Annotations) > 0 {
		if vals.Gateway.ExtraPodAnnotations == nil {
			vals.Gateway.ExtraPodAnnotations = make(map[string]string)
		}
		for k, v := range configs.Annotations {
			vals.Gateway.ExtraPodAnnotations[k] = v
		}
	}
}

// ApplyOverlaysToObjects applies the strategic merge patch overlays to rendered k8s objects.
// This is called after rendering the helm chart.
func (a *AgentgatewayParametersApplier) ApplyOverlaysToObjects(objs []client.Object) error {
	if a.params == nil {
		return nil
	}
	applier := strategicpatch.NewOverlayApplier(a.params)
	return applier.ApplyOverlays(objs)
}

// IsAgentgateway returns true if the Gateway is backed by agentgateway.
func IsAgentgateway(gw *gwv1.Gateway, agwClassName string) bool {
	return string(gw.Spec.GatewayClassName) == agwClassName
}

// IsAgentgatewayController returns true if the GatewayClass is controlled by the agentgateway controller.
func IsAgentgatewayController(gwc *gwv1.GatewayClass, agwControllerName string) bool {
	return string(gwc.Spec.ControllerName) == agwControllerName
}

// agentgatewayParametersHelmValuesGenerator is a HelmValuesGenerator that uses AgentgatewayParameters.
type agentgatewayParametersHelmValuesGenerator struct {
	agwParams *agentgatewayParameters
	inputs    *deployer.Inputs
}

// newAgentgatewayParametersHelmValuesGenerator creates a new HelmValuesGenerator for AgentgatewayParameters.
func newAgentgatewayParametersHelmValuesGenerator(cli apiclient.Client, inputs *deployer.Inputs) *agentgatewayParametersHelmValuesGenerator {
	return &agentgatewayParametersHelmValuesGenerator{
		agwParams: newAgentgatewayParameters(cli, inputs),
		inputs:    inputs,
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

	vals, err := g.getDefaultAgentgatewayHelmValues(gw)
	if err != nil {
		return nil, err
	}

	// Apply AgentgatewayParameters configs to helm values
	if agwp != nil {
		applier := NewAgentgatewayParametersApplier(agwp)
		applier.ApplyToHelmValues(vals)
	}

	var jsonVals map[string]any
	err = deployer.JsonConvert(vals, &jsonVals)
	return jsonVals, err
}

// GetCacheSyncHandlers returns cache sync handlers.
func (g *agentgatewayParametersHelmValuesGenerator) GetCacheSyncHandlers() []cache.InformerSynced {
	return g.agwParams.GetCacheSyncHandlers()
}

// GetAgentgatewayParametersForGateway returns the AgentgatewayParameters for the given Gateway.
func (g *agentgatewayParametersHelmValuesGenerator) GetAgentgatewayParametersForGateway(gw *gwv1.Gateway) (*v1alpha1.AgentgatewayParameters, error) {
	return g.agwParams.GetAgentgatewayParametersForGateway(gw)
}

// getDefaultAgentgatewayHelmValues returns default helm values for an agentgateway Gateway.
func (g *agentgatewayParametersHelmValuesGenerator) getDefaultAgentgatewayHelmValues(gw *gwv1.Gateway) (*deployer.HelmConfig, error) {
	// Get gateway IR for ports
	irGW := deployer.GetGatewayIR(gw, g.inputs.CommonCollections)
	ports := deployer.GetPortsValues(irGW, nil, true) // true = agentgateway
	if len(ports) == 0 {
		return nil, ErrNoValidPorts
	}

	// Build default helm values for agentgateway
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
	gtw.GracefulShutdown = &v1alpha1.GracefulShutdownSpec{
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

// AgentgatewayParametersResolver is the interface for resolving AgentgatewayParameters.
type AgentgatewayParametersResolver interface {
	GetAgentgatewayParametersForGateway(ctx context.Context, gw *gwv1.Gateway) (*v1alpha1.AgentgatewayParameters, error)
	GetCacheSyncHandlers() []cache.InformerSynced
}

// agentgatewayParametersResolverImpl is the implementation of AgentgatewayParametersResolver.
type agentgatewayParametersResolverImpl struct {
	agwParams *agentgatewayParameters
}

// NewAgentgatewayParametersResolver creates a new AgentgatewayParametersResolver.
func NewAgentgatewayParametersResolver(cli apiclient.Client, inputs *deployer.Inputs) AgentgatewayParametersResolver {
	return &agentgatewayParametersResolverImpl{
		agwParams: newAgentgatewayParameters(cli, inputs),
	}
}

// GetAgentgatewayParametersForGateway implements AgentgatewayParametersResolver.
func (r *agentgatewayParametersResolverImpl) GetAgentgatewayParametersForGateway(ctx context.Context, gw *gwv1.Gateway) (*v1alpha1.AgentgatewayParameters, error) {
	return r.agwParams.GetAgentgatewayParametersForGateway(gw)
}

// GetCacheSyncHandlers implements AgentgatewayParametersResolver.
func (r *agentgatewayParametersResolverImpl) GetCacheSyncHandlers() []cache.InformerSynced {
	return r.agwParams.GetCacheSyncHandlers()
}
