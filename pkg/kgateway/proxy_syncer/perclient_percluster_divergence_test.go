package proxy_syncer

// Characterization tests pinning where snapshotPerClient currently diverges
// from the per-cluster readiness spec
// (devel/formal/lean/XdsSpec/PerClusterReadiness.lean).
//
// The spec's safe system requires per-cluster make-before-break: a
// previously-active cluster's truth must always publish (empty included,
// case C2), and a warming newly-referenced cluster may hold only the route
// flip, not unrelated updates (case C3 isolation). The current code applies
// guard #3 at whole-snapshot granularity, which is exactly the spec's
// `wholeSnapshotDeferBugSystem` — the model checker reproduces it as a
// liveness violation whose stuck state has Envoy holding endpoints that no
// longer exist.
//
// These tests assert the CURRENT (divergent) behavior on purpose, so the
// divergence is load-bearing in CI rather than prose: when the per-cluster
// synthesis lands in snapshotPerClient, they MUST fail and be rewritten to
// assert the spec's safe-system behavior. The mapping is gated by
// devel/testing/formal-model-map.yaml.

import (
	"testing"
	"time"

	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/onsi/gomega"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// TestSnapshotPerClientScaleToZeroCurrentlyDefersInsteadOfPublishingEmptyCLA
// pins spec case C2 (divergent): when a previously-active cluster's Service
// scales to zero, the spec requires publishing the now-empty
// ClusterLoadAssignment so Envoy stops routing to dead endpoints. The
// current whole-snapshot gate instead defers forever — Envoy keeps the
// stale endpoints (the original "traffic to upstream endpoints which no
// longer exist" complaint). Spec counterexample: `WholeSnapshotDeferBug`
// violating `TruthLagsA ~> TruthPublishedA`.
func TestSnapshotPerClientScaleToZeroCurrentlyDefersInsteadOfPublishingEmptyCLA(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniquelyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, []ir.UniquelyConnectedClient{ucc})

	listeners := sliceToResources([]*envoylistenerv3.Listener{httpListenerWithRDS(t, "listener", "route-config")})
	routes := routeResourcesForClusters("cluster-old")
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}})

	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		edsClusterForClient(ucc, "cluster-old", 1),
	})
	endpointReady := endpointsForClient(ucc, "cluster-old", 2)
	endpointScaledToZero := emptyEndpointsForClient(ucc, "cluster-old", 3)
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, []UccWithEndpoints{endpointReady})

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
	)

	initialSnap := eventuallySingleSnapshot(t, snapshots)
	g.Expect(initialSnap.Resources[envoycachetypes.Endpoint].Items).To(gomega.HaveKey("cluster-old"),
		"steady state: the active cluster's CLA is published")

	// The Service scales 3 -> 0: the CLA still exists but has no usable
	// endpoint.
	endpointCol.UpdateObject(endpointScaledToZero)

	// CURRENT (divergent) behavior: the whole snapshot is withheld, so the
	// empty CLA never reaches Envoy and the served snapshot keeps the dead
	// endpoints. Per the spec's safe system this update must publish; when
	// the per-cluster synthesis lands, flip this assertion to expect a
	// published snapshot whose cluster-old CLA is empty.
	g.Eventually(func() int {
		return len(snapshots.List())
	}, time.Second, 20*time.Millisecond).Should(gomega.Equal(0),
		"divergence pin (spec case C2): scale-to-zero currently defers the whole snapshot")
	g.Consistently(func() int {
		return len(snapshots.List())
	}, 200*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(0),
		"the defer is indefinite: nothing re-publishes the empty CLA")
}

// TestSnapshotPerClientWarmingClusterCurrentlyBlocksUnrelatedEndpointUpdates
// pins spec case C3's isolation half (divergent): while a newly-referenced
// cluster is warming, the spec holds only the route flip; updates to other,
// already-active clusters must keep publishing. The current whole-snapshot
// gate blocks them too — with a perpetually-unready backend (the customer's
// probe backends sit at zero endpoints by design) every other update for
// the client is stranded behind it.
func TestSnapshotPerClientWarmingClusterCurrentlyBlocksUnrelatedEndpointUpdates(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniquelyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, []ir.UniquelyConnectedClient{ucc})

	listeners := sliceToResources([]*envoylistenerv3.Listener{httpListenerWithRDS(t, "listener", "route-config")})
	initialRoutes := routeResourcesForClusters("cluster-old")
	initial := GatewayXdsResources{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             initialRoutes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(initialRoutes, listeners),
	}
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{initial})

	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		edsClusterForClient(ucc, "cluster-old", 1),
	})
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, []UccWithEndpoints{
		endpointsForClient(ucc, "cluster-old", 2),
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
	)

	initialSnap := eventuallySingleSnapshot(t, snapshots)
	g.Expect(initialSnap.Resources[envoycachetypes.Endpoint].Items).To(gomega.HaveKey("cluster-old"))

	// Routes now additionally reference cluster-new (a probe backend that
	// will sit at zero endpoints indefinitely). Its cluster and empty CLA
	// arrive; the cluster never becomes usable.
	updatedRoutes := routeResourcesForClusters("cluster-old", "cluster-new")
	updated := initial
	updated.Routes = updatedRoutes
	updated.ReferencedClusters = collectReferencedClusters(updatedRoutes, listeners)
	mostXdsSnapshots.UpdateObject(updated)
	clusterCol.UpdateObject(edsClusterForClient(ucc, "cluster-new", 3))
	endpointCol.UpdateObject(emptyEndpointsForClient(ucc, "cluster-new", 4))

	// Meanwhile cluster-old's endpoints change. Per the spec's safe system
	// (publication isolation), this update must publish regardless of
	// cluster-new's readiness.
	endpointCol.UpdateObject(endpointsForClient(ucc, "cluster-old", 5))

	// CURRENT (divergent) behavior: cluster-new's unreadiness withholds the
	// whole snapshot, so cluster-old's endpoint update is stranded too.
	// When the per-cluster synthesis lands, flip this to expect a published
	// snapshot carrying cluster-old's new endpoints while the route flip to
	// cluster-new remains held.
	g.Eventually(func() int {
		return len(snapshots.List())
	}, time.Second, 20*time.Millisecond).Should(gomega.Equal(0),
		"divergence pin (spec case C3 isolation): a warming cluster currently blocks unrelated updates")
	g.Consistently(func() int {
		return len(snapshots.List())
	}, 200*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(0),
		"the block persists for as long as the probe backend stays empty")
}
