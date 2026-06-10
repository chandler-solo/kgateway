package proxy_syncer

import (
	"testing"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/stretchr/testify/require"
)

// Tests for S4 (route-transition readiness) of
// devel/architecture/perclient-xds-publication.md: a route entry or TCP filter
// chain that is new or retargeted activates only once its target is usable;
// entries with unchanged targets always publish (endpoint truth, S3).

func claWithUnhealthyEndpoint(clusterName string) *envoyendpointv3.ClusterLoadAssignment {
	out := cla(clusterName, "10.0.0.66")
	out.GetEndpoints()[0].GetLbEndpoints()[0].HealthStatus = envoycorev3.HealthStatus_UNHEALTHY
	return out
}

func TestClusterLoadAssignmentHasUsableEndpoint(t *testing.T) {
	wrap := func(r envoycachetypes.Resource) envoycachetypes.ResourceWithTTL {
		return envoycachetypes.ResourceWithTTL{Resource: r}
	}

	require.False(t, clusterLoadAssignmentHasUsableEndpoint(wrap(nil)), "nil resource")
	require.False(t, clusterLoadAssignmentHasUsableEndpoint(wrap(edsCluster("not-a-cla"))), "wrong type")
	require.False(t, clusterLoadAssignmentHasUsableEndpoint(wrap(cla("c"))), "empty CLA")
	require.False(t, clusterLoadAssignmentHasUsableEndpoint(wrap(claWithUnhealthyEndpoint("c"))), "only unhealthy endpoints")
	require.True(t, clusterLoadAssignmentHasUsableEndpoint(wrap(cla("c", "10.0.0.1"))), "healthy endpoint")
}

// A retargeted route holds at its previous target until the new target has a
// usable endpoint, then activates.
func TestResolvePublication_S4HoldsRetargetUntilUsable(t *testing.T) {
	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
		resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b")),
		resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-a")),
		resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
	)
	// Retarget r1 to cluster-b, which is present in CDS but has an EMPTY CLA.
	// No missing-cluster metadata: this is purely an S4 case.
	retargeted := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
			resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b")),
			resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-b")),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}

	resolved, stats := resolvePublication(retargeted, prev)
	require.Equal(t, 1, stats.held)
	require.Zero(t, stats.omitted+stats.carried+stats.synthesized)
	heldRC := resolved.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	require.Equal(t, "cluster-a", heldRC.GetVirtualHosts()[0].GetRoutes()[0].GetRoute().GetCluster(),
		"the retargeted route must hold at its previous target while the new one has no usable endpoint")

	// cluster-b gains a usable endpoint; the route activates. prev is now the
	// snapshot that was actually published (with the held route).
	activated := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
			resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b", "10.0.0.2")),
			resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-b")),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}
	resolved2, stats2 := resolvePublication(activated, resolved)
	require.Zero(t, stats2.held+stats2.omitted)
	activatedRC := resolved2.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	require.Equal(t, "cluster-b", activatedRC.GetVirtualHosts()[0].GetRoutes()[0].GetRoute().GetCluster(),
		"once the new target is usable, the route must activate")
}

// A brand-new route to a present-but-endpointless cluster is omitted until the
// cluster becomes usable; other routes are unaffected.
func TestResolvePublication_S4OmitsNewRouteToEmptyCluster(t *testing.T) {
	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
		resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b")),
		resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-a")),
		resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
	)
	rc := routeConfigTo("rc", "vh", "r1", "cluster-a")
	rc.VirtualHosts[0].Routes = append(rc.VirtualHosts[0].Routes,
		routeConfigTo("x", "x", "r-new", "cluster-b").GetVirtualHosts()[0].GetRoutes()[0])
	wrapper := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
			resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b")),
			resourcesOf(rc),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}

	resolved, stats := resolvePublication(wrapper, prev)
	require.Equal(t, 1, stats.omitted)
	require.Zero(t, stats.held)
	outRC := resolved.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	routeNames := []string{}
	for _, r := range outRC.GetVirtualHosts()[0].GetRoutes() {
		routeNames = append(routeNames, r.GetName())
	}
	require.Equal(t, []string{"r1"}, routeNames,
		"the new route to an endpointless cluster is withheld; existing routes publish")
}

// S3 guard: a route whose target did NOT change always publishes, including
// when that target's CLA just became empty (scale-to-zero must propagate).
func TestResolvePublication_S3UnchangedRoutePublishesEmptyCla(t *testing.T) {
	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-c")),
		resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-c", "10.0.0.3")),
		resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-a")),
		resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
	)
	// cluster-a scales to zero (empty CLA) while a NEW route to usable
	// cluster-c is added — so the route resources DID change and the S4 walk
	// runs, but r1's target is unchanged and must not be held.
	rc := routeConfigTo("rc", "vh", "r1", "cluster-a")
	rc.VirtualHosts[0].Routes = append(rc.VirtualHosts[0].Routes,
		routeConfigTo("x", "x", "r2", "cluster-c").GetVirtualHosts()[0].GetRoutes()[0])
	wrapper := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-c")),
			resourcesOf(cla("cluster-a"), cla("cluster-c", "10.0.0.3")),
			resourcesOf(rc),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}

	resolved, stats := resolvePublication(wrapper, prev)
	require.Zero(t, stats.held+stats.omitted,
		"neither the unchanged route (S3) nor the new route to a usable target may be withheld")
	outRC := resolved.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	require.Len(t, outRC.GetVirtualHosts()[0].GetRoutes(), 2)
	publishedCla := resolved.Resources[envoycachetypes.Endpoint].Items["cluster-a"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	require.Empty(t, publishedCla.GetEndpoints(), "the empty CLA is endpoint truth and must publish")
}

// Cold start: with no previously published snapshot there is no baseline to
// define a transition, so the snapshot publishes as built (Envoy's own cluster
// warming covers this window).
func TestResolvePublication_S4SkippedOnColdStart(t *testing.T) {
	wrapper := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-b")),
			resourcesOf(cla("cluster-b")), // endpointless
			resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-b")),
			resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
		),
	}

	resolved, stats := resolvePublication(wrapper, nil)
	require.Zero(t, stats.held+stats.omitted)
	outRC := resolved.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	require.Len(t, outRC.GetVirtualHosts()[0].GetRoutes(), 1, "cold start publishes as built")
}

// Fast path: when the route/listener resources are version-identical to the
// previously published ones, no transition is possible and nothing is held —
// even though a target is endpointless.
func TestResolvePublication_S4SkippedWhenRoutesUnchanged(t *testing.T) {
	snap := snapshotOf(
		resourcesOf(edsCluster("cluster-b")),
		resourcesOf(cla("cluster-b")),
		resourcesOf(routeConfigTo("rc", "vh", "r1", "cluster-b")),
		resourcesOf(&envoylistenerv3.Listener{Name: "listener"}),
	)
	wrapper := XdsSnapWrapper{proxyKey: "ns~gw", snap: snap}

	resolved, stats := resolvePublication(wrapper, snap)
	require.Zero(t, stats.held+stats.omitted)
	outRC := resolved.Resources[envoycachetypes.Route].Items["rc"].Resource.(*envoyroutev3.RouteConfiguration)
	require.Len(t, outRC.GetVirtualHosts()[0].GetRoutes(), 1)
}

// A retargeted TCP filter chain holds at its previous target until the new
// target is usable, then activates.
func TestResolvePublication_S4TcpChainRetargetHeldUntilUsable(t *testing.T) {
	prev := snapshotOf(
		resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
		resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b")),
		resourcesOf(&envoyroutev3.RouteConfiguration{Name: "rc"}),
		resourcesOf(tcpListenerTo("cluster-a")),
	)
	retargeted := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
			resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b")),
			resourcesOf(&envoyroutev3.RouteConfiguration{Name: "rc"}),
			resourcesOf(tcpListenerTo("cluster-b")),
		),
	}

	resolved, stats := resolvePublication(retargeted, prev)
	require.Equal(t, 1, stats.held)
	heldListener := resolved.Resources[envoycachetypes.Listener].Items["listener"].Resource.(*envoylistenerv3.Listener)
	require.Equal(t, []string{"cluster-a"}, tcpFilterChainClusterRefs(heldListener.GetFilterChains()[0]),
		"the retargeted chain must hold at its previous target")

	activated := XdsSnapWrapper{
		proxyKey: "ns~gw",
		snap: snapshotOf(
			resourcesOf(edsCluster("cluster-a"), edsCluster("cluster-b")),
			resourcesOf(cla("cluster-a", "10.0.0.1"), cla("cluster-b", "10.0.0.2")),
			resourcesOf(&envoyroutev3.RouteConfiguration{Name: "rc"}),
			resourcesOf(tcpListenerTo("cluster-b")),
		),
	}
	resolved2, stats2 := resolvePublication(activated, resolved)
	require.Zero(t, stats2.held+stats2.omitted)
	activatedListener := resolved2.Resources[envoycachetypes.Listener].Items["listener"].Resource.(*envoylistenerv3.Listener)
	require.Equal(t, []string{"cluster-b"}, tcpFilterChainClusterRefs(activatedListener.GetFilterChains()[0]),
		"once the new target is usable, the chain must activate")
}
