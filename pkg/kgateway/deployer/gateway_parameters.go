package deployer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"istio.io/istio/pkg/kube/kclient"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/agentgateway"
	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/kgateway"
	"github.com/kgateway-dev/kgateway/v2/pkg/apiclient"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer"
	"github.com/kgateway-dev/kgateway/v2/pkg/deployer/strategicpatch"
)

var (
	// ErrNoValidPorts is returned when no valid ports are found for the Gateway
	ErrNoValidPorts = errors.New("no valid ports")

	// ErrNotFound is returned when a requested resource is not found
	ErrNotFound = errors.New("resource not found")
)

func NewGatewayParameters(cli apiclient.Client, inputs *deployer.Inputs) *GatewayParameters {
	gp := &GatewayParameters{
		inputs: inputs,
	}

	// Only create the kgateway parameters client if Envoy is enabled
	if inputs.CommonCollections.Settings.EnableEnvoy {
		gp.kgwParameters = NewEnvoyGatewayParameters(cli, inputs)
	}

	// Only create the agentgateway parameters client if agentgateway is enabled
	if inputs.CommonCollections.Settings.EnableAgentgateway {
		gp.agwHelmValuesGenerator = NewAgentgatewayParametersHelmValuesGenerator(cli, inputs)
	}

	return gp
}

type GatewayParameters struct {
	inputs                      *deployer.Inputs
	helmValuesGeneratorOverride deployer.HelmValuesGenerator
	kgwParameters               *EnvoyGatewayParameters
	agwHelmValuesGenerator      *AgentgatewayParametersHelmValuesGenerator
}

func (gp *GatewayParameters) WithHelmValuesGeneratorOverride(generator deployer.HelmValuesGenerator) *GatewayParameters {
	gp.helmValuesGeneratorOverride = generator
	return gp
}

// GetGatewayParametersClient returns the GatewayParameters client if Envoy is enabled, nil otherwise.
// This allows the reconciler to reuse the same client for watching changes.
func (gp *GatewayParameters) GetGatewayParametersClient() kclient.Client[*kgateway.GatewayParameters] {
	if gp.kgwParameters != nil {
		return gp.kgwParameters.gwParamClient
	}
	return nil
}

// GetAgentgatewayParametersClient returns the AgentgatewayParameters client if Agentgateway is enabled, nil otherwise.
// This allows the reconciler to reuse the same client for watching changes.
func (gp *GatewayParameters) GetAgentgatewayParametersClient() kclient.Client[*agentgateway.AgentgatewayParameters] {
	if gp.agwHelmValuesGenerator != nil {
		return gp.agwHelmValuesGenerator.agwParamClient
	}
	return nil
}

func (gp *GatewayParameters) GetValues(ctx context.Context, obj client.Object) (map[string]any, error) {
	generator, err := gp.getHelmValuesGenerator(obj)
	if err != nil {
		return nil, err
	}

	return generator.GetValues(ctx, obj)
}

func (gp *GatewayParameters) GetCacheSyncHandlers() []cache.InformerSynced {
	if gp.helmValuesGeneratorOverride != nil {
		return gp.helmValuesGeneratorOverride.GetCacheSyncHandlers()
	}

	var handlers []cache.InformerSynced
	if gp.kgwParameters != nil {
		handlers = append(handlers, gp.kgwParameters.GetCacheSyncHandlers()...)
	}
	if gp.agwHelmValuesGenerator != nil {
		handlers = append(handlers, gp.agwHelmValuesGenerator.GetCacheSyncHandlers()...)
	}
	return handlers
}

// EnvoyHelmValuesGenerator returns the helm values generator for Envoy-based gateways.
// If a helm values generator override is set, it returns that instead.
func (gp *GatewayParameters) EnvoyHelmValuesGenerator() deployer.HelmValuesGenerator {
	if gp.helmValuesGeneratorOverride != nil {
		slog.Debug("using override HelmValuesGenerator for envoy")
		return gp.helmValuesGeneratorOverride
	}
	return gp.kgwParameters
}

// PostProcessObjects implements deployer.ObjectPostProcessor.
// It applies GatewayParameters or AgentgatewayParameters overlays to the rendered objects.
// When both GatewayClass and Gateway have parameters, the overlays
// are applied in order: GatewayClass first, then Gateway on top.
func (gp *GatewayParameters) PostProcessObjects(ctx context.Context, obj client.Object, rendered []client.Object) ([]client.Object, error) {
	// Check if override implements ObjectPostProcessor and delegate to it
	if gp.helmValuesGeneratorOverride != nil {
		if postProcessor, ok := gp.helmValuesGeneratorOverride.(deployer.ObjectPostProcessor); ok {
			return postProcessor.PostProcessObjects(ctx, obj, rendered)
		}
	}

	gw, ok := obj.(*gwv1.Gateway)
	if !ok {
		return rendered, nil
	}

	// Determine which controller this Gateway uses
	var gwClassClient kclient.Client[*gwv1.GatewayClass]
	if gp.kgwParameters != nil {
		gwClassClient = gp.kgwParameters.gwClassClient
	} else if gp.agwHelmValuesGenerator != nil {
		gwClassClient = gp.agwHelmValuesGenerator.gwClassClient
	} else {
		return nil, fmt.Errorf("no controller enabled for Gateway %s/%s", gw.GetNamespace(), gw.GetName())
	}

	gwc, err := getGatewayClassFromGateway(gwClassClient, gw)
	if err != nil {
		return nil, fmt.Errorf("failed to get GatewayClass for Gateway %s/%s: %w", gw.GetNamespace(), gw.GetName(), err)
	}

	// Check if this is an agentgateway or envoy gateway
	if string(gwc.Spec.ControllerName) == gp.inputs.AgentgatewayControllerName {
		// Agentgateway overlays
		if gp.agwHelmValuesGenerator == nil {
			// Agentgateway not enabled; skip overlays (not an error since overlays are optional).
			return rendered, nil
		}
		resolved, err := gp.agwHelmValuesGenerator.GetResolvedParametersForGateway(gw)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve AgentgatewayParameters for Gateway %s/%s: %w", gw.GetNamespace(), gw.GetName(), err)
		}

		// Apply overlays in order: GatewayClass first, then Gateway.
		if resolved.gatewayClassAGWP != nil {
			applier := NewAgentgatewayParametersApplier(resolved.gatewayClassAGWP)
			rendered, err = applier.ApplyOverlaysToObjects(rendered)
			if err != nil {
				return nil, err
			}
		}
		if resolved.gatewayAGWP != nil {
			applier := NewAgentgatewayParametersApplier(resolved.gatewayAGWP)
			rendered, err = applier.ApplyOverlaysToObjects(rendered)
			if err != nil {
				return nil, err
			}
		}
	} else {
		// Envoy (kgateway) overlays
		if gp.kgwParameters == nil {
			// Envoy not enabled; skip overlays (not an error since overlays are optional).
			return rendered, nil
		}
		resolved := gp.kgwParameters.resolveParametersForOverlays(gw)

		// Apply overlays in order: GatewayClass first, then Gateway.
		if resolved.gatewayClassGWP != nil {
			applier := strategicpatch.NewOverlayApplierFromGatewayParameters(resolved.gatewayClassGWP)
			var err error
			rendered, err = applier.ApplyOverlays(rendered)
			if err != nil {
				return nil, err
			}
		}
		if resolved.gatewayGWP != nil {
			applier := strategicpatch.NewOverlayApplierFromGatewayParameters(resolved.gatewayGWP)
			var err error
			rendered, err = applier.ApplyOverlays(rendered)
			if err != nil {
				return nil, err
			}
		}
	}

	return rendered, nil
}

// AgentgatewayParametersHelmValuesGenerator returns the helm values generator for agentgateway-based gateways.
// If a helm values generator override is set, it returns that instead.
func (gp *GatewayParameters) AgentgatewayParametersHelmValuesGenerator() deployer.HelmValuesGenerator {
	if gp.helmValuesGeneratorOverride != nil {
		slog.Debug("using override HelmValuesGenerator for agentgateway")
		return gp.helmValuesGeneratorOverride
	}
	return gp.agwHelmValuesGenerator
}

func GatewayReleaseNameAndNamespace(obj client.Object) (string, string) {
	// A helm release is never installed, only a template is generated, so the name doesn't matter
	// Use a hard-coded name to avoid going over the 53 character name limit
	return "release-name-placeholder", obj.GetNamespace()
}

func (gp *GatewayParameters) getHelmValuesGenerator(obj client.Object) (deployer.HelmValuesGenerator, error) {
	gw, ok := obj.(*gwv1.Gateway)
	if !ok {
		return nil, fmt.Errorf("expected a Gateway resource, got %s", obj.GetObjectKind().GroupVersionKind().String())
	}

	if gp.helmValuesGeneratorOverride != nil {
		slog.Debug("using override HelmValuesGenerator for Gateway",
			"gateway_name", gw.GetName(),
			"gateway_namespace", gw.GetNamespace(),
		)
		return gp.helmValuesGeneratorOverride, nil
	}

	// Need a GatewayClass client to determine which controller this Gateway uses.
	// Use whichever parameter client is available (both have gwClassClient).
	var gwClassClient kclient.Client[*gwv1.GatewayClass]
	if gp.kgwParameters != nil {
		gwClassClient = gp.kgwParameters.gwClassClient
	} else if gp.agwHelmValuesGenerator != nil {
		gwClassClient = gp.agwHelmValuesGenerator.gwClassClient
	} else {
		return nil, fmt.Errorf("no parameter clients available")
	}

	// Check if the GatewayClass uses the agentgateway controller
	gwc, err := getGatewayClassFromGateway(gwClassClient, gw)
	if err != nil {
		return nil, fmt.Errorf("failed to get GatewayClass of Gateway: %w", err)
	}

	if string(gwc.Spec.ControllerName) == gp.inputs.AgentgatewayControllerName {
		if gp.agwHelmValuesGenerator == nil {
			// this should never happen, as the controller should not let any of these GatewayClass's through if agentgateway is not enabled
			return nil, fmt.Errorf("agentgateway is not enabled but Gateway %s/%s uses agentgateway controller", gw.GetNamespace(), gw.GetName())
		}
		slog.Debug("using AgentgatewayParameters HelmValuesGenerator for Gateway",
			"gateway_name", gw.GetName(),
			"gateway_namespace", gw.GetNamespace(),
			"controller_name", gwc.Spec.ControllerName,
		)
		return gp.agwHelmValuesGenerator, nil
	}

	// Use kgwParameters for helm values generation (envoy-based gateways).
	if gp.kgwParameters == nil {
		// this should never happen, as the controller should not let any of these GatewayClass's through if envoy is not enabled
		return nil, fmt.Errorf("envoy is not enabled but Gateway %s/%s uses envoy controller", gw.GetNamespace(), gw.GetName())
	}
	slog.Debug("using default HelmValuesGenerator for Gateway",
		"gateway_name", gw.GetName(),
		"gateway_namespace", gw.GetNamespace(),
		"controller_name", gwc.Spec.ControllerName,
	)
	return gp.kgwParameters, nil
}

func getGatewayClassFromGateway(cli kclient.Client[*gwv1.GatewayClass], gw *gwv1.Gateway) (*gwv1.GatewayClass, error) {
	if gw == nil {
		return nil, errors.New("nil Gateway")
	}
	if gw.Spec.GatewayClassName == "" {
		return nil, errors.New("GatewayClassName must not be empty")
	}

	gwc := cli.Get(string(gw.Spec.GatewayClassName), metav1.NamespaceNone)
	if gwc == nil {
		return nil, fmt.Errorf("failed to get GatewayClass for Gateway %s/%s", gw.GetName(), gw.GetNamespace())
	}

	return gwc, nil
}

func translateInfraMeta[K ~string, V ~string](meta map[K]V) map[string]string {
	infra := make(map[string]string, len(meta))
	for k, v := range meta {
		if strings.HasPrefix(string(k), "gateway.networking.k8s.io/") {
			continue // ignore this prefix to avoid conflicts
		}
		infra[string(k)] = string(v)
	}
	return infra
}
