package proxy_syncer

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
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
			resource.ClusterType:  {&envoyclusterv3.Cluster{Name: "old"}},
			resource.RouteType:    {&envoyroutev3.RouteConfiguration{Name: "unrelated-original"}},
			resource.ListenerType: {&envoylistenerv3.Listener{Name: "original-listener"}},
			resource.SecretType: {&envoytlsv3.Secret{Name: "independent-secret", Type: &envoytlsv3.Secret_GenericSecret{
				GenericSecret: &envoytlsv3.GenericSecret{Secret: &envoycorev3.DataSource{Specifier: &envoycorev3.DataSource_InlineString{
					InlineString: fmt.Sprintf("public-test-revision-%d", revision),
				}}},
			}}},
		}
		if revision > 1 {
			resources[resource.ClusterType] = append(resources[resource.ClusterType], &envoyclusterv3.Cluster{
				Name: "empty", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
				EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: &envoycorev3.ConfigSource{ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{Ads: &envoycorev3.AggregatedConfigSource{}}}},
			})
			resources[resource.EndpointType] = []types.Resource{&envoyendpointv3.ClusterLoadAssignment{ClusterName: "empty"}}
			resources[resource.RouteType] = append(resources[resource.RouteType], &envoyroutev3.RouteConfiguration{Name: "independent-added-route"})
			for _, item := range routeResourcesForClusters("empty").Items {
				resources[resource.RouteType] = append(resources[resource.RouteType], item.Resource)
			}
			resources[resource.ListenerType] = append(resources[resource.ListenerType], httpListenerWithRDS(t, "new-listener", "route-config"))
		}
		snap, err := cache.NewSnapshot(strconv.Itoa(revision), resources)
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
		if served.GetVersion(resource.ClusterType) != strconv.Itoa(revision) {
			t.Fatal("CDS did not continue advancing")
		}
		if _, exists := served.GetResources(resource.EndpointType)["empty"]; !exists {
			t.Fatal("empty CLA not published")
		}
	}
}
