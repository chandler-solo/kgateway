package proxy_syncer

import (
	"fmt"
	"sort"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

type validateAller interface {
	ValidateAll() error
}

type resourceValidator interface {
	Validate() error
}

func ValidateXDSSnapshot(snap *envoycache.Snapshot) error {
	if err := ValidateXDSResources(snap); err != nil {
		return err
	}

	if err := snap.Consistent(); err != nil {
		return fmt.Errorf("inconsistent xds snapshot: %w", err)
	}

	return nil
}

func ValidateXDSResources(snap *envoycache.Snapshot) error {
	if snap == nil {
		return fmt.Errorf("nil xds snapshot")
	}

	for responseType, resources := range snap.Resources {
		for name, item := range resources.Items {
			if err := validateXDSResource(envoycachetypes.ResponseType(responseType), name, item.Resource); err != nil {
				return err
			}
		}
	}

	if err := validateXDSSnapshotReferences(snap); err != nil {
		return err
	}

	return nil
}

func validateXDSSnapshotReferences(snap *envoycache.Snapshot) error {
	// Cluster references are deliberately NOT validated here: a route briefly
	// referencing a cluster the per-client pipeline has not produced yet is an
	// expected transient of the eventually-consistent dataflow, and its publish
	// policy (defer during client warm-up, publish-with-warning afterwards) is
	// decided in syncXds via missingSnapshotClusterReferences. Missing secrets,
	// by contrast, indicate a plugin emitting an SDS reference without its
	// secret — a bug, never a transient — so they reject hard.
	if missingSecrets := findMissingReferencedSecrets(snap); len(missingSecrets) > 0 {
		return fmt.Errorf("xds snapshot references missing secrets: %v", missingSecrets)
	}

	return nil
}

// missingSnapshotClusterReferences returns the dataplane cluster references
// (RouteAction / TcpProxy targets) absent from the snapshot's CDS, exempting
// the blackhole sentinel and clusters whose translation errored.
func missingSnapshotClusterReferences(snap *envoycache.Snapshot, erroredClusters []string) []string {
	referencedClusters := collectReferencedClusters(
		snap.Resources[envoycachetypes.Route],
		snap.Resources[envoycachetypes.Listener],
	)
	return findMissingReferencedClusters(
		referencedClusters,
		snap.Resources[envoycachetypes.Cluster].Items,
		erroredClusters,
	)
}

func findMissingReferencedSecrets(snap *envoycache.Snapshot) []string {
	referencedSecrets := collectReferencedSecrets(snap)
	secretResources := snap.Resources[envoycachetypes.Secret].Items
	missingSecrets := make([]string, 0, len(referencedSecrets))
	for name := range referencedSecrets {
		if _, ok := secretResources[name]; ok {
			continue
		}
		missingSecrets = append(missingSecrets, name)
	}
	sort.Strings(missingSecrets)

	return missingSecrets
}

func collectReferencedSecrets(snap *envoycache.Snapshot) map[string]struct{} {
	referencedSecrets := map[string]struct{}{}
	for _, resources := range snap.Resources {
		for _, item := range resources.Items {
			if item.Resource == nil {
				continue
			}
			collectProtoSecretReferences(item.Resource, referencedSecrets)
		}
	}
	return referencedSecrets
}

func collectProtoSecretReferences(msg proto.Message, referencedSecrets map[string]struct{}) {
	if msg == nil {
		return
	}

	if sdsSecretConfig, ok := msg.(*envoytlsv3.SdsSecretConfig); ok && requiresSnapshotSecret(sdsSecretConfig) {
		referencedSecrets[sdsSecretConfig.GetName()] = struct{}{}
	}

	collectNestedProtoSecretReferences(msg.ProtoReflect(), referencedSecrets)
}

func collectNestedProtoSecretReferences(
	msg protoreflect.Message,
	referencedSecrets map[string]struct{},
) {
	if !msg.IsValid() {
		return
	}

	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList() && fd.Message() != nil:
			list := v.List()
			for i := 0; i < list.Len(); i++ {
				collectProtoSecretReferencesFromValue(list.Get(i), referencedSecrets)
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			m := v.Map()
			m.Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
				collectProtoSecretReferencesFromValue(value, referencedSecrets)
				return true
			})
		case !fd.IsList() && !fd.IsMap() && fd.Message() != nil:
			collectProtoSecretReferencesFromValue(v, referencedSecrets)
		}
		return true
	})
}

func collectProtoSecretReferencesFromValue(v protoreflect.Value, referencedSecrets map[string]struct{}) {
	msg := v.Message()
	if !msg.IsValid() {
		return
	}

	if anyMsg, ok := msg.Interface().(*anypb.Any); ok {
		nestedMsg, err := anyMsg.UnmarshalNew()
		if err != nil {
			logger.Debug("skipping typed_config during secret reference scan", "type_url", anyMsg.GetTypeUrl(), "error", err)
			return
		}
		collectProtoSecretReferences(nestedMsg, referencedSecrets)
		return
	}

	collectProtoSecretReferences(msg.Interface(), referencedSecrets)
}

func requiresSnapshotSecret(sdsSecretConfig *envoytlsv3.SdsSecretConfig) bool {
	if sdsSecretConfig.GetName() == "" {
		return false
	}
	sdsConfig := sdsSecretConfig.GetSdsConfig()
	if sdsConfig == nil {
		return false
	}

	_, usesADS := sdsConfig.GetConfigSourceSpecifier().(*envoycorev3.ConfigSource_Ads)
	return usesADS
}

func validateXDSResource(responseType envoycachetypes.ResponseType, name string, resource envoycachetypes.Resource) error {
	if resource == nil {
		return fmt.Errorf("%s resource %q is nil", responseTypeName(responseType), name)
	}
	if err := validateXDSResourceType(responseType, resource); err != nil {
		return fmt.Errorf("%s resource %q has wrong type: %w", responseTypeName(responseType), name, err)
	}
	resourceName := envoycache.GetResourceName(resource)
	if resourceName == "" {
		return fmt.Errorf("%s resource %q has empty xds name", responseTypeName(responseType), name)
	}
	if resourceName != name {
		return fmt.Errorf("%s resource map key %q does not match resource name %q", responseTypeName(responseType), name, resourceName)
	}
	if err := proto.CheckInitialized(resource); err != nil {
		return fmt.Errorf("%s resource %q is not initialized: %w", responseTypeName(responseType), name, err)
	}
	if v, ok := resource.(validateAller); ok {
		if err := v.ValidateAll(); err != nil {
			return fmt.Errorf("%s resource %q failed validation: %w", responseTypeName(responseType), name, err)
		}
	} else if v, ok := resource.(resourceValidator); ok {
		if err := v.Validate(); err != nil {
			return fmt.Errorf("%s resource %q failed validation: %w", responseTypeName(responseType), name, err)
		}
	}
	if listener, ok := resource.(*envoylistenerv3.Listener); ok {
		if err := validateListenerFilterChainMatches(listener); err != nil {
			return fmt.Errorf("%s resource %q failed filter chain validation: %w", responseTypeName(responseType), name, err)
		}
	}

	return nil
}

func validateListenerFilterChainMatches(listener *envoylistenerv3.Listener) error {
	seenMatches := make(map[string]string, len(listener.GetFilterChains()))
	for _, filterChain := range listener.GetFilterChains() {
		if filterChain == nil {
			return fmt.Errorf("nil filter chain")
		}
		matchKey := "<nil>"
		if filterChain.GetFilterChainMatch() != nil {
			marshaledMatch, err := proto.MarshalOptions{Deterministic: true}.Marshal(filterChain.GetFilterChainMatch())
			if err != nil {
				return fmt.Errorf("marshal filter chain match for %q: %w", filterChain.GetName(), err)
			}
			matchKey = string(marshaledMatch)
		}
		if previousName, ok := seenMatches[matchKey]; ok {
			return fmt.Errorf("duplicate filter chain match for %q and %q", previousName, filterChain.GetName())
		}
		seenMatches[matchKey] = filterChain.GetName()
	}

	return nil
}

func validateXDSResourceType(responseType envoycachetypes.ResponseType, resource envoycachetypes.Resource) error {
	switch responseType {
	case envoycachetypes.Cluster:
		if _, ok := resource.(*envoyclusterv3.Cluster); !ok {
			return fmt.Errorf("expected Cluster, got %T", resource)
		}
	case envoycachetypes.Endpoint:
		if _, ok := resource.(*envoyendpointv3.ClusterLoadAssignment); !ok {
			return fmt.Errorf("expected ClusterLoadAssignment, got %T", resource)
		}
	case envoycachetypes.Listener:
		if _, ok := resource.(*envoylistenerv3.Listener); !ok {
			return fmt.Errorf("expected Listener, got %T", resource)
		}
	case envoycachetypes.Route:
		if _, ok := resource.(*envoyroutev3.RouteConfiguration); !ok {
			return fmt.Errorf("expected RouteConfiguration, got %T", resource)
		}
	case envoycachetypes.ScopedRoute:
		if _, ok := resource.(*envoyroutev3.ScopedRouteConfiguration); !ok {
			return fmt.Errorf("expected ScopedRouteConfiguration, got %T", resource)
		}
	case envoycachetypes.VirtualHost:
		if _, ok := resource.(*envoyroutev3.VirtualHost); !ok {
			return fmt.Errorf("expected VirtualHost, got %T", resource)
		}
	case envoycachetypes.Secret:
		if _, ok := resource.(*envoytlsv3.Secret); !ok {
			return fmt.Errorf("expected Secret, got %T", resource)
		}
	}

	return nil
}

func responseTypeName(responseType envoycachetypes.ResponseType) string {
	switch responseType {
	case envoycachetypes.Cluster:
		return "cluster"
	case envoycachetypes.Endpoint:
		return "endpoint"
	case envoycachetypes.Listener:
		return "listener"
	case envoycachetypes.Route:
		return "route"
	case envoycachetypes.ScopedRoute:
		return "scoped route"
	case envoycachetypes.VirtualHost:
		return "virtual host"
	case envoycachetypes.Secret:
		return "secret"
	case envoycachetypes.Runtime:
		return "runtime"
	case envoycachetypes.ExtensionConfig:
		return "extension config"
	case envoycachetypes.RateLimitConfig:
		return "rate limit config"
	default:
		return fmt.Sprintf("response type %d", responseType)
	}
}
