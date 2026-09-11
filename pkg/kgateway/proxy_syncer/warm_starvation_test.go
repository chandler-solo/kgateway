package proxy_syncer

import (
	"context"
	"fmt"
	"testing"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	tls "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/proto"
)

// RF-002: repeated candidates cannot rotate an unrelated secret or add an
// unrelated route configuration while a newly referenced backend remains empty.
// This is a cache-boundary probe; it does not submit these fixtures to Envoy.
// This pins the research policy's limitation, not a desired product guarantee.
func TestWarmEmptyBackendHoldsUnrelatedRouteAndSecret(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, cache.IDHash{}, nil)
	translator := NewProxyTranslator(c)
	makeSnapshot := func(revision int) *cache.Snapshot {
		t.Helper()
		resources := map[resource.Type][]types.Resource{
			resource.ClusterType:  {&cluster.Cluster{Name: "old"}},
			resource.RouteType:    {&route.RouteConfiguration{Name: "unrelated-original"}},
			resource.ListenerType: {&listener.Listener{Name: "original-listener"}},
			resource.SecretType: {&tls.Secret{Name: "independent-secret", Type: &tls.Secret_GenericSecret{
				GenericSecret: &tls.GenericSecret{Secret: &core.DataSource{Specifier: &core.DataSource_InlineString{
					InlineString: fmt.Sprintf("public-test-revision-%d", revision),
				}}},
			}}},
		}
		if revision > 1 {
			resources[resource.ClusterType] = append(resources[resource.ClusterType], &cluster.Cluster{
				Name: "empty", ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_EDS},
				EdsClusterConfig: &cluster.Cluster_EdsClusterConfig{EdsConfig: &core.ConfigSource{ConfigSourceSpecifier: &core.ConfigSource_Ads{Ads: &core.AggregatedConfigSource{}}}},
			})
			resources[resource.EndpointType] = []types.Resource{&endpoint.ClusterLoadAssignment{ClusterName: "empty"}}
			resources[resource.RouteType] = append(resources[resource.RouteType], &route.RouteConfiguration{Name: "independent-added-route"})
			for _, item := range routeResourcesForClusters("empty").Items {
				resources[resource.RouteType] = append(resources[resource.RouteType], item.Resource)
			}
			resources[resource.ListenerType] = append(resources[resource.ListenerType], httpListenerWithRDS(t, "new-listener", "route-config"))
		}
		snap, err := cache.NewSnapshot(fmt.Sprint(revision), resources)
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}
	initial := makeSnapshot(1)
	translator.syncXds(ctx, XdsSnapWrapper{snap: initial, proxyKey: "warm"})
	for revision := 2; revision <= 10; revision++ {
		candidate := makeSnapshot(revision)
		translator.syncXds(ctx, XdsSnapWrapper{snap: candidate, proxyKey: "warm", deferred: true, unusableReferenced: []string{"empty"}})
		served, err := c.GetSnapshot("warm")
		if err != nil {
			t.Fatal(err)
		}
		for _, typ := range []resource.Type{resource.RouteType, resource.ListenerType, resource.SecretType} {
			if served.GetVersion(typ) != "1" {
				t.Fatalf("revision %d: held type %s changed; reassess RF-002", revision, typ)
			}
		}
		if _, exists := served.GetResources(resource.RouteType)["independent-added-route"]; exists {
			t.Fatal("unrelated route was not held; reassess RF-002")
		}
		if !proto.Equal(served.GetResources(resource.SecretType)["independent-secret"], initial.GetResources(resource.SecretType)["independent-secret"]) {
			t.Fatal("secret rotated; reassess RF-002")
		}
		if served.GetVersion(resource.ClusterType) != fmt.Sprint(revision) {
			t.Fatal("CDS did not continue advancing")
		}
		if _, exists := served.GetResources(resource.EndpointType)["empty"]; !exists {
			t.Fatal("empty CLA not published")
		}
	}
}
