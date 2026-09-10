package proxy_syncer

// Tests for the per-cluster readiness resolution (spec:
// devel/formal/lean/XdsSpec/PerClusterReadiness.lean, safe system).
//
// snapshotPerClient no longer defers the whole snapshot when a referenced
// cluster is not ready; syncXds resolves the gaps per cluster against the
// currently-published snapshot:
//
//   - C0: first cache publication permits empty CLAs once CDS is complete;
//   - C2: a previously-referenced cluster whose endpoints scale to zero
//     publishes its truth — the empty CLA — so Envoy stops routing to
//     endpoints that no longer exist;
//   - C3 isolation: a newly-referenced cluster that is not yet ready holds
//     the route/listener/secret types; other clusters' endpoints and the
//     warming cluster's own CDS entry keep publishing.
//
// These replace the divergence pins that asserted the old whole-snapshot
// defer (the spec's wholeSnapshotDeferBugSystem); the mapping is gated by
// devel/testing/formal-model-map.yaml.

import (
	"context"
	"errors"
	"testing"
	"time"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/onsi/gomega"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// TestSnapshotPerClientScaleToZeroPublishesEmptyCLA covers spec case C2:
// when a previously-active cluster's Service scales to zero, the now-empty
// ClusterLoadAssignment publishes so Envoy stops routing to dead endpoints
// (the original "traffic to upstream endpoints which no longer exist"
// complaint). The old whole-snapshot gate deferred this forever — the spec's
// WholeSnapshotDeferBug liveness counterexample.
func TestSnapshotPerClientScaleToZeroPublishesEmptyCLA(t *testing.T) {
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

	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	registerSyncXds(snapshots, NewProxyTranslator(cache))
	nodeID := ucc.ResourceName()

	initialServed := eventuallyCacheSnapshot(t, cache, nodeID)
	g.Expect(initialServed.Resources[envoycachetypes.Endpoint].Items).To(gomega.HaveKey("cluster-old"),
		"steady state: the active cluster's CLA is served")

	// The Service scales 3 -> 0: the CLA still exists but has no usable
	// endpoint.
	endpointCol.UpdateObject(endpointScaledToZero)

	// C2: truth wins. The empty CLA reaches the served cache so Envoy drops
	// the dead endpoints; the routes stay as they are (only that cluster's
	// traffic degrades, accurately).
	g.Eventually(func() bool {
		resourceSnapshot, err := cache.GetSnapshot(nodeID)
		if err != nil {
			return false
		}
		snap, ok := resourceSnapshot.(*envoycache.Snapshot)
		if !ok {
			return false
		}
		item, ok := snap.Resources[envoycachetypes.Endpoint].Items["cluster-old"]
		if !ok {
			return false
		}
		cla, ok := item.Resource.(*envoyendpointv3.ClusterLoadAssignment)
		return ok && len(cla.GetEndpoints()) == 0
	}, time.Second, 20*time.Millisecond).Should(gomega.BeTrue(),
		"the empty CLA must publish so Envoy stops routing to endpoints that no longer exist")
	served := eventuallyCacheSnapshot(t, cache, nodeID)
	g.Expect(snapshotReferencesCluster(served, "cluster-old")).To(gomega.BeTrue(),
		"the routes are unchanged; only the cluster's endpoint truth changed")
	assertNoXDSCheckErrors(t, served)
}

// TestSnapshotPerClientWarmingClusterHoldsOnlyRouteFlip covers spec case C3's
// isolation half: while a newly-referenced cluster is warming (a probe
// backend that sits at zero endpoints indefinitely), only the route flip is
// held. The warming cluster's CDS entry and every unrelated update keep
// publishing — the old whole-snapshot gate stranded them all behind the one
// unready backend.
func TestSnapshotPerClientWarmingClusterHoldsOnlyRouteFlip(t *testing.T) {
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

	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	registerSyncXds(snapshots, NewProxyTranslator(cache))
	nodeID := ucc.ResourceName()

	initialServed := eventuallyCacheSnapshot(t, cache, nodeID)
	g.Expect(initialServed.Resources[envoycachetypes.Endpoint].Items).To(gomega.HaveKey("cluster-old"))
	initialEndpointVersion := initialServed.Resources[envoycachetypes.Endpoint].Version
	initialRouteVersion := initialServed.Resources[envoycachetypes.Route].Version

	// Routes now additionally reference cluster-new (a probe backend that
	// will sit at zero endpoints indefinitely). Its cluster and empty CLA
	// arrive; the cluster never becomes usable. Meanwhile cluster-old's
	// endpoints change.
	updatedRoutes := routeResourcesForClusters("cluster-old", "cluster-new")
	updated := initial
	updated.Routes = updatedRoutes
	updated.ReferencedClusters = collectReferencedClusters(updatedRoutes, listeners)
	mostXdsSnapshots.UpdateObject(updated)
	clusterCol.UpdateObject(edsClusterForClient(ucc, "cluster-new", 3))
	endpointCol.UpdateObject(emptyEndpointsForClient(ucc, "cluster-new", 4))
	endpointCol.UpdateObject(endpointsForClient(ucc, "cluster-old", 5))

	// C3 isolation: only the flip onto cluster-new is held. The warming
	// cluster reaches the served CDS, and cluster-old's endpoint update
	// publishes — nothing is stranded behind the unready backend.
	var heldServed *envoycache.Snapshot
	g.Eventually(func() bool {
		heldServed = eventuallyCacheSnapshot(t, cache, nodeID)
		return hasResource(heldServed.Resources[envoycachetypes.Cluster].Items, "cluster-new") &&
			heldServed.Resources[envoycachetypes.Endpoint].Version != initialEndpointVersion
	}, time.Second, 20*time.Millisecond).Should(gomega.BeTrue(),
		"the warming cluster and the unrelated endpoint update must both reach the served cache")
	g.Expect(snapshotReferencesCluster(heldServed, "cluster-new")).To(gomega.BeFalse(),
		"the route flip onto the unready cluster is held")
	g.Expect(snapshotReferencesCluster(heldServed, "cluster-old")).To(gomega.BeTrue(),
		"the previously-served routes keep flowing traffic")
	g.Expect(heldServed.Resources[envoycachetypes.Route].Version).To(gomega.Equal(initialRouteVersion),
		"held routes keep their published version")
	assertNoXDSCheckErrors(t, heldServed)
}

// TestSnapshotPerClientErroredClusterIsNotCarriedDuringHeldFlip pins the
// fail-closed rule for errored clusters: even while a route flip is held for
// an unrelated warming cluster, a previously-referenced cluster whose current
// translation is errored is dropped from the served CDS instead of being
// carried forward from the published snapshot. Serving it with its stale
// (pre-error) config would silently bypass the policy whose failure errored
// it — Gateway API conformance requires requests to a backend targeted by an
// invalid BackendTLSPolicy to receive a 5xx
// (BackendTLSPolicyInvalidCACertificateRef); the fail-open variant was
// rejected in PR #13976.
func TestSnapshotPerClientErroredClusterIsNotCarriedDuringHeldFlip(t *testing.T) {
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

	clusterOld := edsClusterForClient(ucc, "cluster-old", 1)
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{clusterOld})
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

	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	registerSyncXds(snapshots, NewProxyTranslator(cache))
	nodeID := ucc.ResourceName()

	initialServed := eventuallyCacheSnapshot(t, cache, nodeID)
	g.Expect(initialServed.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-old"))

	// Simultaneously: routes retarget to additionally reference cluster-new
	// (which never becomes ready, so the flip is held), and cluster-old's
	// translation goes errored (e.g. its BackendTLSPolicy became invalid).
	updatedRoutes := routeResourcesForClusters("cluster-old", "cluster-new")
	updated := initial
	updated.Routes = updatedRoutes
	updated.ReferencedClusters = collectReferencedClusters(updatedRoutes, listeners)
	mostXdsSnapshots.UpdateObject(updated)
	clusterCol.UpdateObject(edsClusterForClient(ucc, "cluster-new", 3))
	endpointCol.UpdateObject(emptyEndpointsForClient(ucc, "cluster-new", 4))
	erroredOld := clusterOld
	erroredOld.Error = errors.New("backend tls policy references a nonexistent ca certificate")
	clusterCol.UpdateObject(erroredOld)

	// Fail closed: the errored cluster must leave the served CDS (its held
	// routes 5xx) even though the flip is held for cluster-new; the warming
	// cluster still reaches the served CDS. The served snapshot legitimately
	// contains a route to the dropped errored cluster, so no xdscheck
	// assertion applies here.
	var heldServed *envoycache.Snapshot
	g.Eventually(func() bool {
		heldServed = eventuallyCacheSnapshot(t, cache, nodeID)
		clusters := heldServed.Resources[envoycachetypes.Cluster].Items
		return hasResource(clusters, "cluster-new") && !hasResource(clusters, "cluster-old")
	}, time.Second, 20*time.Millisecond).Should(gomega.BeTrue(),
		"the errored cluster must not be carried forward; the warming cluster still publishes")
	g.Expect(heldServed.Resources[envoycachetypes.Endpoint].Items).ToNot(gomega.HaveKey("cluster-old"),
		"the errored cluster's CLA leaves with it")
	g.Expect(snapshotReferencesCluster(heldServed, "cluster-old")).To(gomega.BeTrue(),
		"the held routes still name the errored cluster, which now 5xxes (fail closed)")
	g.Expect(snapshotReferencesCluster(heldServed, "cluster-new")).To(gomega.BeFalse(),
		"the flip onto the warming cluster remains held")
}

// First publication is gated on CDS closure, not backend availability. Keep
// the empty backend empty through a cache restart and an unrelated route update.
func TestSnapshotPerClientFirstPublishWithEmptyEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name          string
		missingCDS    bool
		explicitEmpty bool
	}{
		{name: "synthesized-empty"},
		{name: "explicit-empty", explicitEmpty: true},
		{name: "missing-CDS", missingCDS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
			ucc := ir.NewUniquelyConnectedClient(role, "", nil, ir.PodLocality{})
			uccs := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, []ir.UniquelyConnectedClient{ucc})
			listeners := sliceToResources([]*envoylistenerv3.Listener{httpListenerWithRDS(t, "listener", "route-config")})
			routes := routeResourcesForClusters("healthy", "empty")
			input := GatewayXdsResources{
				NamespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"},
				Routes:         routes, Listeners: listeners,
				ReferencedClusters: collectReferencedClusters(routes, listeners),
			}
			inputs := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{input})
			clusters := []uccWithCluster{edsClusterForClient(ucc, "healthy", 1)}
			// Exercise service_name resolution as well as synthesized assignments.
			emptyCluster := edsClusterForClientWithServiceName(ucc, "empty", "empty-eds", 2)
			if !tc.missingCDS {
				clusters = append(clusters, emptyCluster)
			}
			clusterCol := krt.NewStaticCollection[uccWithCluster](nil, clusters)
			endpoints := []UccWithEndpoints{endpointsForClient(ucc, "healthy", 1)}
			if tc.explicitEmpty {
				endpoints = append(endpoints, emptyEndpointsForClient(ucc, "empty-eds", 2))
			}
			endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, endpoints)
			snapshots := snapshotPerClient(krtutil.KrtOptions{}, uccs, inputs,
				PerClientEnvoyEndpoints{endpoints: endpointCol, index: krtpkg.UnnamedIndex(endpointCol, func(ep UccWithEndpoints) []string { return []string{ep.Client.ResourceName()} })},
				PerClientEnvoyClusters{clusters: clusterCol, index: krtpkg.UnnamedIndex(clusterCol, func(c uccWithCluster) []string { return []string{c.Client.ResourceName()} })})
			cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
			translator := NewProxyTranslator(cache)
			wrap := eventuallyDeferredWrapper(t, snapshots)
			translator.syncXds(context.Background(), wrap)
			if tc.missingCDS {
				g.Expect(wrap.missingReferenced).To(gomega.ConsistOf("empty"))
				_, err := cache.GetSnapshot(ucc.ResourceName())
				g.Expect(err).To(gomega.MatchError("no snapshot found for node " + ucc.ResourceName()))
			}
			registerSyncXds(snapshots, translator)
			if tc.missingCDS {
				clusterCol.UpdateObject(emptyCluster)
			}
			served := eventuallyCacheSnapshot(t, cache, ucc.ResourceName())
			g.Expect(snapshotReferencesCluster(served, "empty")).To(gomega.BeTrue())
			g.Expect(served.Resources[envoycachetypes.Endpoint].Items["empty-eds"].Resource.(*envoyendpointv3.ClusterLoadAssignment).GetEndpoints()).To(gomega.BeEmpty())
			assertNoXDSCheckErrors(t, served)

			// A controller restart loses the cache even when Envoy has an old
			// configuration. Re-publish the same empty truth without endpoints
			// recovering. This checks the cache boundary, not a live Envoy.
			restartedCache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
			restartedTranslator := NewProxyTranslator(restartedCache)
			wrap = eventuallyDeferredWrapper(t, snapshots)
			restartedTranslator.syncXds(context.Background(), wrap)
			assertNoXDSCheckErrors(t, eventuallyCacheSnapshot(t, restartedCache, ucc.ResourceName()))
			registerSyncXds(snapshots, restartedTranslator)

			// Add another route to a backend already in CDS while empty stays
			// empty. No newly referenced backend is introduced (warm C3 is separate).
			updatedRoutes := routeResourcesForClusters("healthy", "empty", "healthy")
			updatedConfig := updatedRoutes.Items["route-config"].Resource.(*envoyroutev3.RouteConfiguration)
			updatedConfig.VirtualHosts[0].Routes[2].Name = "additional-healthy-route"
			updatedRoutes = sliceToResources([]*envoyroutev3.RouteConfiguration{updatedConfig})
			input.Routes = updatedRoutes
			inputs.UpdateObject(input)
			for _, c := range []envoycache.SnapshotCache{cache, restartedCache} {
				g.Eventually(func() string {
					s, err := c.GetSnapshot(ucc.ResourceName())
					if err != nil {
						return ""
					}
					return s.GetVersion(envoyresourcev3.RouteType)
				}, time.Second, 20*time.Millisecond).Should(gomega.Equal(updatedRoutes.Version), "unrelated route updates must not wait for empty endpoints, including after restart")
			}

			// Endpoints recover on the same client; EDS changes without reconnect.
			emptyVersion := eventuallyCacheSnapshot(t, cache, ucc.ResourceName()).Resources[envoycachetypes.Endpoint].Version
			endpointCol.UpdateObject(endpointsForClient(ucc, "empty-eds", 3))
			g.Eventually(func() bool {
				s, err := cache.GetSnapshot(ucc.ResourceName())
				if err != nil {
					return false
				}
				return s.GetVersion(envoyresourcev3.EndpointType) != emptyVersion && clusterLoadAssignmentHasUsableEndpoint(s.GetResourcesAndTTL(envoyresourcev3.EndpointType)["empty-eds"])
			}, time.Second, 20*time.Millisecond).Should(gomega.BeTrue(), "endpoint recovery must publish without reconnect")
		})
	}
}

func TestSnapshotPerClientFirstPublishPreservesExemptions(t *testing.T) {
	g := gomega.NewWithT(t)
	ucc := ir.NewUniquelyConnectedClient("test-role", "", nil, ir.PodLocality{})
	cluster := edsClusterForClient(ucc, "empty", 1)
	routes := routeResourcesForClusters("empty", "errored", wellknown.BlackholeClusterName)
	listeners := sliceToResources([]*envoylistenerv3.Listener{httpListenerWithRDS(t, "listener", "route-config")})
	refs := collectReferencedClusters(routes, listeners)
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = envoycache.NewResources("cds", []envoycachetypes.Resource{cluster.Cluster})
	snap.Resources[envoycachetypes.Endpoint] = sliceToResources([]*envoyendpointv3.ClusterLoadAssignment{{ClusterName: "empty"}})
	snap.Resources[envoycachetypes.Route] = routes
	snap.Resources[envoycachetypes.Listener] = listeners
	missing := findMissingReferencedClusters(refs, snap.Resources[envoycachetypes.Cluster].Items, []string{"errored"})
	g.Expect(missing).To(gomega.BeEmpty(), "errored and blackhole references do not block first publication")
	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	translator := NewProxyTranslator(cache)
	translator.syncXds(context.Background(), XdsSnapWrapper{
		snap: snap, proxyKey: ucc.ResourceName(), deferred: true,
		missingReferenced: missing, unusableReferenced: []string{"empty"}, erroredClusters: []string{"errored"},
	})
	served := eventuallyCacheSnapshot(t, cache, ucc.ResourceName())
	g.Expect(served.Resources[envoycachetypes.Cluster].Items).ToNot(gomega.HaveKey("errored"), "errored backend stays absent (fail closed)")
	g.Expect(snapshotReferencesCluster(served, "empty")).To(gomega.BeTrue(), "empty backend does not starve publication")
}
