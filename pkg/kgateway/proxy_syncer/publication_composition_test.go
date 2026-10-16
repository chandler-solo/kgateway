package proxy_syncer

import (
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	aggregate "github.com/envoyproxy/go-control-plane/envoy/extensions/clusters/aggregate/v3"
	jwt "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/jwt_authn/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	types "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestCarryPreservesCurrentSharedAssignment(t *testing.T) {
	eds := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{
			Name: name, ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
			EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{ServiceName: "shared"},
		}
	}
	old := &envoyendpointv3.ClusterLoadAssignment{ClusterName: "shared", Policy: &envoyendpointv3.ClusterLoadAssignment_Policy{OverprovisioningFactor: nil}}
	for _, current := range []*envoyendpointv3.ClusterLoadAssignment{
		{ClusterName: "shared"}, // derived empty truth must also beat the carried bytes
		{ClusterName: "shared", Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{Locality: &envoycorev3.Locality{Zone: "new"}}}},
	} {
		published := &cache.Snapshot{}
		published.Resources[types.Cluster] = cache.NewResources("old", []types.Resource{eds("a")})
		published.Resources[types.Endpoint] = cache.NewResources("111", []types.Resource{old})
		w := XdsSnapWrapper{snap: &cache.Snapshot{}, missingReferenced: []string{"a"}, endpointHashes: map[string]uint64{"shared": 222}}
		w.snap.Resources[types.Cluster] = cache.NewResources("new", []types.Resource{eds("b")})
		w.snap.Resources[types.Endpoint] = cache.NewResources("222", []types.Resource{current})
		resolved, hashes := resolveDeferredPerCluster(w, published, func(string) (uint64, bool) { return 111, true })
		require.NoError(t, resolved.Consistent())
		require.True(t, proto.Equal(current, resolved.GetResources(rsrc.EndpointType)["shared"]))
		require.Equal(t, uint64(222), hashes["shared"])
		require.True(t, proto.Equal(current, w.snap.GetResources(rsrc.EndpointType)["shared"]), "candidate must remain immutable")
		// If no current assignment exists, the old bytes and old contribution travel together.
		w.snap.Resources[types.Endpoint] = cache.Resources{}
		w.endpointHashes = nil
		resolved, hashes = resolveDeferredPerCluster(w, published, func(string) (uint64, bool) { return 111, true })
		require.True(t, proto.Equal(old, resolved.GetResources(rsrc.EndpointType)["shared"]))
		require.Equal(t, uint64(111), hashes["shared"])
	}
}

func TestHeldListenersKeepAncillaryDependencies(t *testing.T) {
	filter := &jwt.JwtAuthentication{Providers: map[string]*jwt.JwtProvider{"jwt": {
		JwksSourceSpecifier: &jwt.JwtProvider_RemoteJwks{RemoteJwks: &jwt.RemoteJwks{
			HttpUri: &envoycorev3.HttpUri{HttpUpstreamType: &envoycorev3.HttpUri_Cluster{Cluster: "jwks"}},
		}},
	}}}
	l := &envoylistenerv3.Listener{Name: "listener", FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{
		Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: mustMessageToAny(t, &hcm.HttpConnectionManager{
			HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.jwt_authn", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: mustMessageToAny(t, filter)}}},
		})},
	}}}}}
	pub := &cache.Snapshot{}
	pub.Resources[types.Listener] = cache.NewResources("old", []types.Resource{l})
	pub.Resources[types.Cluster] = cache.NewResources("old", []types.Resource{
		&envoyclusterv3.Cluster{Name: "a"},
		&envoyclusterv3.Cluster{Name: "jwks", ClusterDiscoveryType: &envoyclusterv3.Cluster_ClusterType{ClusterType: &envoyclusterv3.Cluster_CustomClusterType{
			Name: "envoy.clusters.aggregate", TypedConfig: mustMessageToAny(t, &aggregate.ClusterConfig{Clusters: []string{"jwks-leaf"}}),
		}}},
		&envoyclusterv3.Cluster{Name: "jwks-leaf"}, &envoyclusterv3.Cluster{Name: "unused"},
	})
	pub.Resources[types.Route] = routeResourcesForClusters("a")
	w := XdsSnapWrapper{snap: &cache.Snapshot{}, missingEndpointsReferenced: []string{"b"}}
	w.snap.Resources[types.Cluster] = cache.NewResources("new", []types.Resource{&envoyclusterv3.Cluster{Name: "b"}})
	w.snap.Resources[types.Route] = routeResourcesForClusters("b")
	resolved, _ := resolveDeferredPerCluster(w, pub, nil)
	require.Equal(t, "old", resolved.GetVersion(rsrc.ListenerType))
	for _, name := range []string{"a", "jwks", "jwks-leaf"} {
		require.Contains(t, resolved.GetResources(rsrc.ClusterType), name)
	}
	require.NotContains(t, resolved.GetResources(rsrc.ClusterType), "unused")
	w.erroredClusters = []string{"jwks"}
	resolved, _ = resolveDeferredPerCluster(w, pub, nil)
	require.NotContains(t, resolved.GetResources(rsrc.ClusterType), "jwks", "translation errors override retention")
	require.Empty(t, pub.GetResources(rsrc.ClusterType)["a"].(*envoyclusterv3.Cluster).GetEdsClusterConfig())
}

func TestRetainedDependenciesConservativelyHandleDynamicAndOpaqueConfig(t *testing.T) {
	for _, msg := range []proto.Message{
		&anypb.Any{TypeUrl: "type.googleapis.com/unlinked.Config", Value: []byte{10, 1, 'x'}},
		&envoyroutev3.WeightedCluster_ClusterWeight{ClusterHeader: "x-destination"},
		&envoyroutev3.RouteAction_RequestMirrorPolicy{ClusterHeader: "x-mirror"},
	} {
		require.False(t, scanRetainedDependencies(msg, func(string) {}))
	}
	pub := &cache.Snapshot{}
	pub.Resources[types.Cluster] = cache.NewResources("v", []types.Resource{&envoyclusterv3.Cluster{Name: "keep"}, &envoyclusterv3.Cluster{Name: "errored"}})
	pub.Resources[types.Listener] = cache.NewResources("v", []types.Resource{&envoylistenerv3.Listener{
		Name: "opaque", FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{ConfigType: &envoylistenerv3.Filter_TypedConfig{
			TypedConfig: &anypb.Any{TypeUrl: "type.googleapis.com/unlinked.Config"},
		}}}}},
	}})
	require.Equal(t, map[string]struct{}{"keep": {}}, retainedClusterDependencies(pub, []string{"errored"}))
}

func TestWrapperEqualityIncludesDerivationState(t *testing.T) {
	a := XdsSnapWrapper{snap: &cache.Snapshot{}, proxyKey: "client"}
	b := a
	b.deferred = true
	b.missingEndpointsReferenced = []string{"backend"}
	require.False(t, a.Equals(b), "equal snapshot bytes must not hide changed publication eligibility")
	b = a
	b.erroredClusters = []string{"backend"}
	require.False(t, a.Equals(b))
	require.True(t, a.Equals(a))
}
