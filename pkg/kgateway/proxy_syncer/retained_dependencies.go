package proxy_syncer

import (
	"strings"

	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

// retainedClusterDependencies is deliberately separate from readiness gating.
// Held LDS/RDS/SDS can still use ancillary clusters, including through a carried
// aggregate cluster. Match strings against the published CDS inventory to
// overapproximate those references without making ancillary readiness a gate.
// An opaque extension or request-time selector retains the eligible inventory.
// Translation errors always win over retention; this is not a revocation API.
func retainedClusterDependencies(published envoycache.ResourceSnapshot, errored []string) map[string]struct{} {
	clusters := published.GetResourcesAndTTL(envoyresourcev3.ClusterType)
	excluded := stringSet(errored)
	refs := map[string]struct{}{}
	add := func(name string) {
		if _, bad := excluded[name]; bad {
			return
		}
		if _, exists := clusters[name]; exists {
			refs[name] = struct{}{}
		}
	}
	complete := true
	for _, typ := range []string{envoyresourcev3.ListenerType, envoyresourcev3.RouteType, envoyresourcev3.SecretType} {
		for _, item := range published.GetResourcesAndTTL(typ) {
			complete = scanRetainedDependencies(item.Resource, add) && complete
		}
	}
	visited := map[string]struct{}{}
	for len(visited) < len(refs) && complete {
		for name := range refs {
			if _, seen := visited[name]; seen {
				continue
			}
			visited[name] = struct{}{}
			complete = scanRetainedDependencies(clusters[name].Resource, add) && complete
		}
	}
	if !complete {
		for name := range clusters {
			add(name)
		}
	}
	return refs
}

func scanRetainedDependencies(msg proto.Message, add func(string)) bool {
	if msg == nil {
		return true
	}
	if a, ok := msg.(*anypb.Any); ok {
		nested, err := a.UnmarshalNew()
		return err == nil && scanRetainedDependencies(nested, add)
	}
	switch m := msg.(type) {
	case *envoyroutev3.RouteAction:
		switch m.GetClusterSpecifier().(type) {
		case *envoyroutev3.RouteAction_ClusterHeader, *envoyroutev3.RouteAction_ClusterSpecifierPlugin, *envoyroutev3.RouteAction_InlineClusterSpecifierPlugin:
			return false
		}
	case *envoyroutev3.WeightedCluster_ClusterWeight:
		if m.GetClusterHeader() != "" {
			return false
		}
	case *envoyroutev3.RouteAction_RequestMirrorPolicy:
		if m.GetClusterHeader() != "" {
			return false
		}
	}
	fullName := string(msg.ProtoReflect().Descriptor().FullName())
	if strings.Contains(fullName, ".lua.") || strings.Contains(fullName, ".wasm.") {
		return false // code may compute names that never occur as proto strings
	}
	complete := true
	visit := func(kind protoreflect.Kind, value protoreflect.Value) {
		switch kind {
		case protoreflect.StringKind:
			add(value.String())
		case protoreflect.MessageKind, protoreflect.GroupKind:
			complete = scanRetainedDependencies(value.Message().Interface(), add) && complete
		}
	}
	msg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			v.Map().Range(func(k protoreflect.MapKey, value protoreflect.Value) bool {
				visit(fd.MapKey().Kind(), k.Value())
				visit(fd.MapValue().Kind(), value)
				return true
			})
		case fd.IsList():
			for i := 0; i < v.List().Len(); i++ {
				visit(fd.Kind(), v.List().Get(i))
			}
		default:
			visit(fd.Kind(), v)
		}
		return true
	})
	return complete
}
