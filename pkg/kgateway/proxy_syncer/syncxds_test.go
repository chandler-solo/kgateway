package proxy_syncer

import (
	"context"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoyhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/stretchr/testify/require"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

// Tests for the publish-time policy in syncXds
// (devel/architecture/perclient-xds-publication.md): hard validation always
// withholds; required-but-missing CLAs are synthesized empty; missing cluster
// references defer only during a never-published client's warm-up window, then
// publish with a warning.

func syncTestEdsCluster() *envoyclusterv3.Cluster {
	const name = "cluster-a"
	return &envoyclusterv3.Cluster{
		Name:                 name,
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
	}
}

func syncTestCla(name string) *envoyendpointv3.ClusterLoadAssignment {
	return &envoyendpointv3.ClusterLoadAssignment{ClusterName: name}
}

func syncTestRouteTo(cluster string) *envoyroutev3.RouteConfiguration {
	return &envoyroutev3.RouteConfiguration{
		Name: "rc",
		VirtualHosts: []*envoyroutev3.VirtualHost{{
			Name:    "vh",
			Domains: []string{"*"},
			Routes: []*envoyroutev3.Route{{
				Name: "r1",
				Match: &envoyroutev3.RouteMatch{
					PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"},
				},
				Action: &envoyroutev3.Route_Route{
					Route: &envoyroutev3.RouteAction{
						ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: cluster},
					},
				},
			}},
		}},
	}
}

// syncTestListener returns a PGV-valid listener whose HCM references the "rc"
// RouteConfiguration via RDS, keeping go-control-plane's consistency check
// (LDS -> RDS reference matching) satisfied.
func syncTestListener() *envoylistenerv3.Listener {
	hcm, err := utils.MessageToAny(&envoyhcmv3.HttpConnectionManager{
		StatPrefix: "http",
		RouteSpecifier: &envoyhcmv3.HttpConnectionManager_Rds{
			Rds: &envoyhcmv3.Rds{
				RouteConfigName: "rc",
				ConfigSource: &envoycorev3.ConfigSource{
					ResourceApiVersion: envoycorev3.ApiVersion_V3,
					ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{
						Ads: &envoycorev3.AggregatedConfigSource{},
					},
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}
	return &envoylistenerv3.Listener{
		Name: "listener",
		Address: &envoycorev3.Address{
			Address: &envoycorev3.Address_SocketAddress{
				SocketAddress: &envoycorev3.SocketAddress{
					Address:       "0.0.0.0",
					PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 8080},
				},
			},
		},
		FilterChains: []*envoylistenerv3.FilterChain{{
			Filters: []*envoylistenerv3.Filter{{
				Name:       "envoy.filters.network.http_connection_manager",
				ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: hcm},
			}},
		}},
	}
}

func syncTestResources(version string, msgs ...envoycachetypes.Resource) envoycache.Resources {
	items := make([]envoycachetypes.ResourceWithTTL, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, envoycachetypes.ResourceWithTTL{Resource: m})
	}
	return envoycache.NewResourcesWithTTL(version, items)
}

func syncTestSnapshot(clusters, endpoints, routes, listeners envoycache.Resources) *envoycache.Snapshot {
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = clusters
	snap.Resources[envoycachetypes.Endpoint] = endpoints
	snap.Resources[envoycachetypes.Route] = routes
	snap.Resources[envoycachetypes.Listener] = listeners
	return snap
}

func newSyncTestTranslator(gate warmupGate) (*ProxyTranslator, envoycache.SnapshotCache) {
	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	return &ProxyTranslator{xdsCache: cache, warmup: gate}, cache
}

// A coherent snapshot publishes and is retrievable from the cache.
func TestSyncXdsPublishesCoherentSnapshot(t *testing.T) {
	pt, cache := newSyncTestTranslator(nil)
	wrap := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: syncTestSnapshot(
			syncTestResources("c1", syncTestEdsCluster()),
			syncTestResources("e1", syncTestCla("cluster-a")),
			syncTestResources("r1", syncTestRouteTo("cluster-a")),
			syncTestResources("l1", syncTestListener()),
		),
	}

	require.True(t, pt.syncXds(context.Background(), wrap))
	_, err := cache.GetSnapshot("ns~gw")
	require.NoError(t, err)
}

// Hard validation failures (here: a nil resource) withhold publication and
// preserve whatever was published before.
func TestSyncXdsWithholdsInvalidSnapshot(t *testing.T) {
	pt, cache := newSyncTestTranslator(nil)
	wrap := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: syncTestSnapshot(
			envoycache.Resources{Version: "c1", Items: map[string]envoycachetypes.ResourceWithTTL{
				"cluster-a": {Resource: nil},
			}},
			syncTestResources("e1"),
			syncTestResources("r1"),
			syncTestResources("l1"),
		),
	}

	require.False(t, pt.syncXds(context.Background(), wrap))
	_, err := cache.GetSnapshot("ns~gw")
	require.Error(t, err, "nothing must be published for an invalid snapshot")
}

// A required-but-missing CLA is synthesized empty so the snapshot stays
// consistent and publishes.
func TestSyncXdsSynthesizesMissingCla(t *testing.T) {
	pt, cache := newSyncTestTranslator(nil)
	wrap := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: syncTestSnapshot(
			syncTestResources("c1", syncTestEdsCluster()),
			syncTestResources("e1"), // CLA row absent: an endpoints-side hole
			syncTestResources("r1", syncTestRouteTo("cluster-a")),
			syncTestResources("l1", syncTestListener()),
		),
	}

	require.True(t, pt.syncXds(context.Background(), wrap))
	published, err := cache.GetSnapshot("ns~gw")
	require.NoError(t, err)
	cla, ok := published.GetResourcesAndTTL("type.googleapis.com/envoy.config.endpoint.v3.ClusterLoadAssignment")["cluster-a"]
	require.True(t, ok, "an empty CLA must be synthesized for the EDS cluster")
	require.Empty(t, cla.Resource.(*envoyendpointv3.ClusterLoadAssignment).GetEndpoints())
}

// Missing cluster references defer a never-published client during its warm-up
// window, publish once the deadline expires, and never defer a client that has
// already published.
func TestSyncXdsWarmupGateBoundsDeferral(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	reconciler := newPerClientReconciler(nil, nil, time.Minute, 15*time.Second)
	reconciler.now = clk.now
	pt, cache := newSyncTestTranslator(reconciler)

	partial := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: syncTestSnapshot(
			syncTestResources("c1", syncTestEdsCluster()),
			syncTestResources("e1", syncTestCla("cluster-a")),
			syncTestResources("r1", syncTestRouteTo("cluster-missing")),
			syncTestResources("l1", syncTestListener()),
		),
	}

	// Cold client, within budget: deferred.
	require.False(t, pt.syncXds(context.Background(), partial))
	_, err := cache.GetSnapshot("ns~gw")
	require.Error(t, err, "first publish must be deferred during warm-up")

	// Still within budget: still deferred.
	clk.advance(10 * time.Second)
	require.False(t, pt.syncXds(context.Background(), partial))

	// Deadline expired: publishes the best available snapshot. The deferral is
	// bounded by the clock, not by the inputs ever becoming complete.
	clk.advance(10 * time.Second)
	require.True(t, pt.syncXds(context.Background(), partial))
	_, err = cache.GetSnapshot("ns~gw")
	require.NoError(t, err)

	// Once published, a later partial snapshot is never deferred.
	reconciler.observePublished("ns~gw")
	require.True(t, pt.syncXds(context.Background(), partial))
}

// An errored cluster reference is exempt from warm-up deferral, matching the
// previous gate's behavior for translation failures.
func TestSyncXdsWarmupGateExemptsErroredClusters(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	reconciler := newPerClientReconciler(nil, nil, time.Minute, 15*time.Second)
	reconciler.now = clk.now
	pt, _ := newSyncTestTranslator(reconciler)

	wrap := XdsSnapWrapper{
		proxyKey:        "ns~gw",
		erroredClusters: []string{"cluster-errored"},
		snap: syncTestSnapshot(
			syncTestResources("c1", syncTestEdsCluster()),
			syncTestResources("e1", syncTestCla("cluster-a")),
			syncTestResources("r1", syncTestRouteTo("cluster-errored")),
			syncTestResources("l1", syncTestListener()),
		),
	}

	require.True(t, pt.syncXds(context.Background(), wrap),
		"references to errored clusters must not defer publication")
}
