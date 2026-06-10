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
// withholds; incomplete inputs (missing referenced clusters, synthesized
// CLAs) defer within a bounded episode — budget expiry observed via the
// heartbeat's pending-retry path on quiet inputs — then publish marked
// degraded.

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

// syncTestWrapper builds a wrapper around a coherent snapshot routing to
// routeTarget, with the wrapper-level reference metadata snapshotPerClient
// would have attached.
func syncTestWrapper(routeTarget string) XdsSnapWrapper {
	return XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: syncTestSnapshot(
			syncTestResources("c1", syncTestEdsCluster()),
			syncTestResources("e1", syncTestCla("cluster-a")),
			syncTestResources("r1", syncTestRouteTo(routeTarget)),
			syncTestResources("l1", syncTestListener()),
		),
		referencedClusters: map[string]struct{}{routeTarget: {}},
	}
}

func newSyncTestTranslator() (*ProxyTranslator, envoycache.SnapshotCache) {
	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	return &ProxyTranslator{xdsCache: cache}, cache
}

// newSyncTestTranslatorWithGate wires a real reconciler (sharing the cache) as
// the publication gate, with a fake clock and the given warm-up budget.
func newSyncTestTranslatorWithGate(clk *fakeClock, budget time.Duration) (*ProxyTranslator, envoycache.SnapshotCache, *perClientReconciler) {
	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	reconciler := newPerClientReconciler(cache, nil, time.Minute, budget)
	reconciler.now = clk.now
	return &ProxyTranslator{xdsCache: cache, gate: reconciler}, cache, reconciler
}

// A coherent snapshot publishes and is retrievable from the cache.
func TestSyncXdsPublishesCoherentSnapshot(t *testing.T) {
	pt, cache := newSyncTestTranslator()
	require.True(t, pt.syncXds(context.Background(), syncTestWrapper("cluster-a")))
	_, err := cache.GetSnapshot("ns~gw")
	require.NoError(t, err)
}

// Hard validation failures (here: a nil resource) withhold publication and
// preserve whatever was published before.
func TestSyncXdsWithholdsInvalidSnapshot(t *testing.T) {
	pt, cache := newSyncTestTranslator()
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

// A snapshot built with synthesized empty CLAs is incomplete: it defers within
// the episode budget (Envoy keeps its last-good endpoints), then publishes
// marked degraded (stuck), so the heartbeat keeps recomputing; the next clean
// publish counts as the recovery.
func TestSyncXdsSynthesizedClasDeferThenPublishDegraded(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	pt, cache, reconciler := newSyncTestTranslatorWithGate(clk, 15*time.Second)

	wrap := syncTestWrapper("cluster-a")
	wrap.synthesizedClas = []string{"cluster-a"}

	// Within budget: withheld, so a transient endpoints-row hole never wipes a
	// backend's live endpoints.
	require.False(t, pt.syncXds(context.Background(), wrap))
	_, err := cache.GetSnapshot("ns~gw")
	require.Error(t, err)

	// Budget expired: publishes, marked degraded.
	clk.advance(20 * time.Second)
	require.True(t, pt.syncXds(context.Background(), wrap))
	_, err = cache.GetSnapshot("ns~gw")
	require.NoError(t, err)

	// Degraded => still stuck (whitebox: nil uccs makes hasStuckClients
	// inapplicable here, so assert on the recorded state directly).
	reconciler.mu.Lock()
	st := reconciler.clients["ns~gw"]
	degraded := st != nil && st.degraded
	reconciler.mu.Unlock()
	require.True(t, degraded, "a publish with synthesized CLAs must be recorded as degraded")

	// The next clean publish is the recovery.
	_, recovered := reconciler.commitPublish(context.Background(), syncTestWrapper("cluster-a"), false, nil)
	require.True(t, recovered, "clean publish after a degraded one must count as a recovery")
}

// Missing cluster references defer publication within the episode budget, then
// publish (degraded) once it expires; the still-open episode lets further
// incomplete updates flow immediately, and a clean publish ends it so a later
// NEW incompleteness episode defers afresh.
func TestSyncXdsIncompleteGateBoundsDeferral(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	pt, cache, reconciler := newSyncTestTranslatorWithGate(clk, 15*time.Second)

	partial := syncTestWrapper("cluster-missing")

	// Cold client, within budget: deferred.
	require.False(t, pt.syncXds(context.Background(), partial))
	_, err := cache.GetSnapshot("ns~gw")
	require.Error(t, err, "first publish must be deferred while inputs are incomplete")

	// Still within budget: still deferred.
	clk.advance(10 * time.Second)
	require.False(t, pt.syncXds(context.Background(), partial))

	// Budget expired: publishes the best available snapshot. The deferral is
	// bounded by the clock, not by the inputs ever becoming complete.
	clk.advance(10 * time.Second)
	require.True(t, pt.syncXds(context.Background(), partial))
	_, err = cache.GetSnapshot("ns~gw")
	require.NoError(t, err)

	// The episode stays open after a degraded publish, so further incomplete
	// updates flow immediately instead of re-deferring (endpoint updates for
	// healthy clusters must not freeze).
	require.True(t, pt.syncXds(context.Background(), partial))

	// Degraded publishes keep the client stuck for the heartbeat.
	reconciler.mu.Lock()
	degraded := reconciler.clients["ns~gw"].degraded
	reconciler.mu.Unlock()
	require.True(t, degraded, "a missing-cluster publish must be recorded as degraded")

	// A clean publish ends the episode...
	require.True(t, pt.syncXds(context.Background(), syncTestWrapper("cluster-a")))
	// ...so a NEW incompleteness (e.g. a transiently gutted rebuild for this
	// already-published client) defers again rather than regressing Envoy's
	// state-of-the-world config.
	require.False(t, pt.syncXds(context.Background(), partial),
		"a fresh incomplete episode for a published client must defer, not regress the published config")
}

// Regression (review finding / #14184 class): with quiet inputs there is no
// KRT event to re-run syncXds after the deferral budget expires — KRT
// suppresses unchanged recomputes — so budget expiry MUST be observable via
// the retained pending snapshot, exactly as the heartbeat loop drives it.
func TestSyncXdsBudgetExpiryViaPendingRetry(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	pt, cache, reconciler := newSyncTestTranslatorWithGate(clk, 15*time.Second)

	partial := syncTestWrapper("cluster-missing")

	// Cold client: the one and only event-driven attempt defers and retains
	// the wrapper for retry.
	require.False(t, pt.syncXds(context.Background(), partial))
	pending := reconciler.pendingRetries()
	require.Len(t, pending, 1, "an incomplete-inputs withhold must retain the snapshot for retry")

	// A retry tick before the budget expires keeps deferring (and keeps the wrapper).
	clk.advance(10 * time.Second)
	require.False(t, pt.syncXdsAttempt(context.Background(), pending[0].wrap, &pending[0].seq))
	require.Len(t, reconciler.pendingRetries(), 1)

	// A retry tick after the budget expires publishes — with NO new KRT event.
	clk.advance(10 * time.Second)
	retry := reconciler.pendingRetries()
	require.Len(t, retry, 1)
	require.True(t, pt.syncXdsAttempt(context.Background(), retry[0].wrap, &retry[0].seq),
		"budget expiry must be acted on by the retry path on quiet inputs")
	_, err := cache.GetSnapshot("ns~gw")
	require.NoError(t, err)
	require.Empty(t, reconciler.pendingRetries(), "a published retry must clear the pending entry")
}

// An errored cluster reference is exempt from incomplete-inputs deferral,
// matching the previous gate's behavior for translation failures.
func TestSyncXdsIncompleteGateExemptsErroredClusters(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	pt, _, _ := newSyncTestTranslatorWithGate(clk, 10*time.Second)

	wrap := syncTestWrapper("cluster-errored")
	wrap.erroredClusters = []string{"cluster-errored"}

	require.True(t, pt.syncXds(context.Background(), wrap),
		"references to errored clusters must not defer publication")
}
