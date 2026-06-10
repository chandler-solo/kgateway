package proxy_syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoyhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoytcpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/tcp_proxy/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/onsi/gomega"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// Tests for the publish-time resolution rules of
// devel/architecture/perclient-xds-publication.md: R1 (publish endpoint truth,
// including empty CLAs), R2 (carry referenced-but-absent clusters forward), R3
// (hold/omit only the referencing routes), the S2 EDS subset filter, and the
// legacy escape hatch.

func edsCluster(name string) *envoyclusterv3.Cluster {
	return &envoyclusterv3.Cluster{
		Name:                 name,
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
	}
}

func staticCluster(name string) *envoyclusterv3.Cluster {
	return &envoyclusterv3.Cluster{
		Name:                 name,
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC},
	}
}

func cla(clusterName string, addresses ...string) *envoyendpointv3.ClusterLoadAssignment {
	out := &envoyendpointv3.ClusterLoadAssignment{ClusterName: clusterName}
	if len(addresses) == 0 {
		return out
	}
	eps := make([]*envoyendpointv3.LbEndpoint, 0, len(addresses))
	for _, addr := range addresses {
		eps = append(eps, &envoyendpointv3.LbEndpoint{
			HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{
				Endpoint: &envoyendpointv3.Endpoint{
					Address: &envoycorev3.Address{
						Address: &envoycorev3.Address_SocketAddress{
							SocketAddress: &envoycorev3.SocketAddress{
								Address:       addr,
								PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 8080},
							},
						},
					},
				},
			},
		})
	}
	out.Endpoints = []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: eps}}
	return out
}

func routeConfigTo(rcName, vhName, routeName, cluster string) *envoyroutev3.RouteConfiguration {
	return &envoyroutev3.RouteConfiguration{
		Name: rcName,
		VirtualHosts: []*envoyroutev3.VirtualHost{{
			Name:    vhName,
			Domains: []string{"*"},
			Routes: []*envoyroutev3.Route{{
				Name: routeName,
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

func tcpListenerTo(cluster string) *envoylistenerv3.Listener {
	tcpProxy, err := utils.MessageToAny(&envoytcpv3.TcpProxy{
		StatPrefix:       "tcp",
		ClusterSpecifier: &envoytcpv3.TcpProxy_Cluster{Cluster: cluster},
	})
	if err != nil {
		panic(err)
	}
	return &envoylistenerv3.Listener{
		Name: "listener",
		FilterChains: []*envoylistenerv3.FilterChain{{
			Name: "chain",
			Filters: []*envoylistenerv3.Filter{{
				Name:       "envoy.filters.network.tcp_proxy",
				ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: tcpProxy},
			}},
		}},
	}
}

func resourcesOf(msgs ...envoycachetypes.Resource) envoycache.Resources {
	items := make([]envoycachetypes.ResourceWithTTL, 0, len(msgs))
	var hash uint64
	for _, m := range msgs {
		items = append(items, envoycachetypes.ResourceWithTTL{Resource: m})
		hash ^= utils.HashProto(m)
	}
	return envoycache.NewResourcesWithTTL(fmt.Sprintf("%d", hash), items)
}

func snapshotOf(clusters, endpoints, routes, listeners envoycache.Resources) *envoycache.Snapshot {
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = clusters
	snap.Resources[envoycachetypes.Endpoint] = endpoints
	snap.Resources[envoycachetypes.Route] = routes
	snap.Resources[envoycachetypes.Listener] = listeners
	return snap
}

// R1: an empty CLA is endpoint truth (scale-to-zero) and publishes as-is —
// resolution is not triggered and the empty CLA is preserved.
func TestResolvePublication_EmptyClaIsTruthNotAHole(t *testing.T) {
	wrapper := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a")),
			resourcesOf(cla("cluster-a")), // empty CLA: zero endpoints
			resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-a")),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}
	require.False(t, wrapper.needsResolution(),
		"an empty CLA satisfies the cluster; it must not be treated as a hole")
}

// R2: a referenced cluster absent from current inputs is carried forward from
// the previously published snapshot together with its CLA.
func TestResolvePublication_CarriesAbsentClusterFromPrevious(t *testing.T) {
	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
		resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b", "10.0.0.2")),
		resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-b")),
		resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
	)
	wrapper := XdsSnapWrapper{
		proxyKey:        "ns~gw",
		missingClusters: []string{"cluster-b"},
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a")),
			resourcesOf(cla("cluster-a", "10.0.0.1")),
			resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-b")),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}

	resolved, stats := resolvePublication(wrapper, prev)
	require.Equal(t, 1, stats.carried)
	require.Zero(t, stats.held+stats.omitted+stats.synthesized)
	require.Contains(t, resolved.Resources[envoycachetypes.Cluster].Items, "cluster-b",
		"cluster-b must be carried from the previous snapshot")
	require.Contains(t, resolved.Resources[envoycachetypes.Endpoint].Items, "cluster-b",
		"cluster-b's CLA must travel with it (S2)")
	require.Contains(t, resolved.Resources[envoycachetypes.Cluster].Items, "cluster-a")
}

// R3 omit: a brand-new route to a never-published cluster is omitted; every
// other route publishes.
func TestResolvePublication_OmitsNewRouteToUnknownCluster(t *testing.T) {
	rc := &envoyroutev3.RouteConfiguration{
		Name: "rc",
		VirtualHosts: []*envoyroutev3.VirtualHost{{
			Name:    "vh",
			Domains: []string{"*"},
			Routes: []*envoyroutev3.Route{
				routeConfigTo("x", "x", "good-route", "cluster-a").GetVirtualHosts()[0].GetRoutes()[0],
				routeConfigTo("x", "x", "new-route", "cluster-new").GetVirtualHosts()[0].GetRoutes()[0],
			},
		}},
	}
	wrapper := XdsSnapWrapper{
		proxyKey:        "ns~gw",
		missingClusters: []string{"cluster-new"},
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a")),
			resourcesOf(cla("cluster-a", "10.0.0.1")),
			resourcesOf(rc),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}

	resolved, stats := resolvePublication(wrapper, nil) // cold start: no previous snapshot
	require.Equal(t, 1, stats.omitted)
	require.Zero(t, stats.held)
	outRC := resolved.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	routeNames := []string{}
	for _, r := range outRC.GetVirtualHosts()[0].GetRoutes() {
		routeNames = append(routeNames, r.GetName())
	}
	require.Equal(t, []string{"good-route"}, routeNames,
		"only the route referencing the unknown cluster is withheld")
}

// R3 hold: a route retargeted to a never-published cluster is held at its
// previously published version.
func TestResolvePublication_HoldsRetargetedRouteAtPreviousVersion(t *testing.T) {
	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a")),
		resourcesOf(cla("cluster-a", "10.0.0.1")),
		resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-a")),
		resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
	)
	wrapper := XdsSnapWrapper{
		proxyKey:        "ns~gw",
		missingClusters: []string{"cluster-new"},
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a")),
			resourcesOf(cla("cluster-a", "10.0.0.1")),
			resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-new")), // retargeted
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}

	resolved, stats := resolvePublication(wrapper, prev)
	require.Equal(t, 1, stats.held)
	require.Zero(t, stats.omitted)
	outRC := resolved.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	route := outRC.GetVirtualHosts()[0].GetRoutes()[0]
	require.Equal(t, "cluster-a", route.GetRoute().GetCluster(),
		"the route must be held at its previous target until the new cluster exists")
}

// R3 for TCP: a filter chain whose TcpProxy targets a never-published cluster
// is held at its previous version, or omitted when there is none.
func TestResolvePublication_PrunesTcpFilterChains(t *testing.T) {
	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a")),
		resourcesOf(cla("cluster-a", "10.0.0.1")),
		resourcesOf(&envoyroutev3.RouteConfiguration{Name: "rc"}),
		resourcesOf(tcpListenerTo("cluster-a")),
	)
	wrapper := XdsSnapWrapper{
		proxyKey:        "ns~gw",
		missingClusters: []string{"cluster-new"},
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a")),
			resourcesOf(cla("cluster-a", "10.0.0.1")),
			resourcesOf(&envoyroutev3.RouteConfiguration{Name: "rc"}),
			resourcesOf(tcpListenerTo("cluster-new")), // retargeted
		),
	}

	resolved, stats := resolvePublication(wrapper, prev)
	require.Equal(t, 1, stats.held)
	outListener := resolved.Resources[envoycachetypes.Listener].Items["listener"].Resource.(*envoylistenerv3.Listener)
	refs := tcpFilterChainClusterRefs(outListener.GetFilterChains()[0])
	require.Equal(t, []string{"cluster-a"}, refs,
		"the TCP chain must be held at its previous target")

	// Without a previous version, the chain is omitted.
	resolved, stats = resolvePublication(wrapper, nil)
	require.Equal(t, 1, stats.omitted)
	outListener = resolved.Resources[envoycachetypes.Listener].Items["listener"].Resource.(*envoylistenerv3.Listener)
	require.Empty(t, outListener.GetFilterChains())
}

// R1 missing-CLA edge: a present EDS cluster lacking its CLA gets the previous
// CLA when one was published, else a synthesized empty CLA.
func TestResolvePublication_MissingClaCarriedOrSynthesized(t *testing.T) {
	base := func() XdsSnapWrapper {
		return XdsSnapWrapper{
			proxyKey:                "ns~gw",
			missingEndpointClusters: map[string]string{"cluster-a": "cluster-a"},
			snap: snapshotOf(
				resourcesOf(edsCluster("cluster-a")),
				resourcesOf(), // CLA row absent: an endpoints-side hole
				resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-a")),
				resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
			),
		}
	}

	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a")),
		resourcesOf(cla("cluster-a", "10.0.0.9")),
		resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-a")),
		resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
	)
	resolved, stats := resolvePublication(base(), prev)
	require.Zero(t, stats.synthesized, "previous CLA must be preferred over synthesis")
	carried := resolved.Resources[envoycachetypes.Endpoint].Items["cluster-a"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	require.NotEmpty(t, carried.GetEndpoints(), "the previous CLA's endpoints are carried")

	resolved, stats = resolvePublication(base(), nil)
	require.Equal(t, 1, stats.synthesized)
	synthesized := resolved.Resources[envoycachetypes.Endpoint].Items["cluster-a"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	require.Empty(t, synthesized.GetEndpoints(), "synthesized CLA is empty")
}

// S2: the build-time filter keeps only CLAs required by EDS clusters in CDS —
// STATIC clusters' CLAs and stale CLAs for removed clusters are dropped.
func TestFilterEndpointResourcesForClusters_EnforcesEdsSubset(t *testing.T) {
	clusters := resourcesOf(edsCluster("eds-a"), staticCluster("static-b"))
	endpoints := resourcesOf(
		cla("eds-a", "10.0.0.1"),
		cla("static-b", "10.0.0.2"),  // not requested: STATIC
		cla("removed-c", "10.0.0.3"), // not requested: cluster gone from CDS
	)

	filtered := filterEndpointResourcesForClusters(clusters, endpointsWithUccName{endpoints: endpoints})
	require.Len(t, filtered.Items, 1)
	require.Contains(t, filtered.Items, "eds-a")
}

// S1 enforcement at publish: a reference shape the pruner does not model (here
// an inline HCM route config inside a listener, which
// collectReferencedClusters walks via protoreflect but pruneListeners does not
// rewrite) must cause syncXds to withhold publication rather than hand Envoy a
// dangling reference.
func TestSyncXdsWithholdsWhenClosureUnrestorable(t *testing.T) {
	hcm, err := utils.MessageToAny(&envoyhcmv3.HttpConnectionManager{
		StatPrefix: "http",
		RouteSpecifier: &envoyhcmv3.HttpConnectionManager_RouteConfig{
			RouteConfig: routeConfigTo("inline-rc", "vh", "r1", "cluster-missing"),
		},
	})
	require.NoError(t, err)
	listener := &envoylistenerv3.Listener{
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
			Name: "chain",
			Filters: []*envoylistenerv3.Filter{{
				Name:       "envoy.filters.network.http_connection_manager",
				ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: hcm},
			}},
		}},
	}
	wrapper := XdsSnapWrapper{
		proxyKey:           "ns~gw",
		missingClusters:    []string{"cluster-missing"},
		referencedClusters: map[string]struct{}{"cluster-missing": {}},
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a")),
			resourcesOf(cla("cluster-a", "10.0.0.1")),
			resourcesOf(), // no RDS: the route config is inline in the listener
			resourcesOf(listener),
		),
	}

	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	reconciler := newPerClientReconciler(cache, nil, time.Minute, 15*time.Second)
	reconciler.now = clk.now
	pt := &ProxyTranslator{xdsCache: cache, gate: reconciler}

	// Within the deferral budget: withheld, never a dangling reference.
	require.False(t, pt.syncXds(context.Background(), wrapper),
		"a surviving dangling reference must withhold publication, not violate S1")
	_, err = cache.GetSnapshot("ns~gw")
	require.Error(t, err, "nothing may be published while closure is unrestorable and the budget has not elapsed")

	// The withheld wrapper is retained, and a retry tick past the budget
	// publishes the best available snapshot, marked degraded — bounded
	// deferral, with NO new KRT event required (quiet inputs produce none).
	pending := reconciler.pendingRetries()
	require.Len(t, pending, 1, "an unresolvable-references withhold must retain the snapshot for retry")
	clk.advance(20 * time.Second)
	require.True(t, pt.syncXdsAttempt(context.Background(), pending[0].wrap, &pending[0].seq),
		"budget expiry must be acted on by the retry path")
	_, err = cache.GetSnapshot("ns~gw")
	require.NoError(t, err)
}

// Legacy escape hatch: KGW_LEGACY_SNAPSHOT_GATE restores the #13868 behavior of
// withholding the whole snapshot while a referenced cluster is absent.
func TestSnapshotPerClientLegacyGateDefers(t *testing.T) {
	legacySnapshotGate = true
	t.Cleanup(func() { legacySnapshotGate = false })
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection[ir.UniqlyConnectedClient](nil, []ir.UniqlyConnectedClient{ucc})
	routes := sliceToResources([]*envoyroutev3.RouteConfiguration{routeConfigTo("rc", "vh", "r1", "cluster-b")})
	listeners := sliceToResources([]*envoylistenerv3.Listener{{Name: "listener"}})
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}})
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{Client: ucc, Name: "cluster-a", Cluster: edsCluster("cluster-a"), ClusterVersion: 1},
	})
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, []UccWithEndpoints{
		{Client: ucc, Endpoints: cla("cluster-a", "10.0.0.1"), EndpointsHash: 1, endpointsName: "cluster-a"},
	})

	snapshots := snapshotPerClient(
		krtutil.KrtOptions{},
		uccs,
		mostXdsSnapshots,
		PerClientEnvoyEndpoints{
			endpoints: endpointCol,
			index: krtpkg.UnnamedIndex(endpointCol, func(ep UccWithEndpoints) []string {
				return []string{ep.Client.ResourceName()}
			}),
		},
		PerClientEnvoyClusters{
			clusters: clusterCol,
			index: krtpkg.UnnamedIndex(clusterCol, func(cluster uccWithCluster) []string {
				return []string{cluster.Client.ResourceName()}
			}),
		},
		nil,
	)

	g.Consistently(func() int {
		return len(snapshots.List())
	}, 200*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(0),
		"legacy gate must withhold the snapshot while cluster-b is missing")
}
