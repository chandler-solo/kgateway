package proxy_syncer

// Regression tests for EDS snapshot consistency. An EDS cluster present in the
// per-client CDS must always have a matching ClusterLoadAssignment in the
// published snapshot (go-control-plane's Snapshot.Consistent() invariant),
// even when no route references it and it has no endpoints yet —
// filterEndpointResourcesForClusters synthesizes an empty assignment for such
// clusters. The gap was found by the randomized property test
// (perclient_property_test.go); xds_consistency_probe_test.go documents the
// go-control-plane tolerance the fix no longer leans on.

import (
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/onsi/gomega"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

func TestFilterEndpointResourcesForClusters_SynthesizesEmptyCLAForEDSClusterWithoutCLA(t *testing.T) {
	g := gomega.NewWithT(t)

	clusters := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{
			Name:                 "eds-x",
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
		}},
	})
	endpoints := envoycache.NewResourcesWithTTL("v1", nil) // no CLA for eds-x

	out := filterEndpointResourcesForClusters(clusters, endpoints)

	g.Expect(out.Items).To(gomega.HaveKey("eds-x"), "an EDS cluster without a CLA must get a synthesized assignment")
	cla, ok := out.Items["eds-x"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	g.Expect(ok).To(gomega.BeTrue())
	g.Expect(cla.GetClusterName()).To(gomega.Equal("eds-x"))
	g.Expect(cla.GetEndpoints()).To(gomega.BeEmpty(), "the synthesized assignment is empty (active with no hosts)")
}

// TestSnapshotPerClientPublishesConsistentSnapshotForUnreferencedEDSClusterWithoutEndpoints
// exercises the production path: an EDS cluster lands in the per-client CDS
// that no route references and has no endpoints. The published snapshot must
// still be EDS-consistent (Snapshot.Consistent() passes), which before the fix
// it was not.
func TestSnapshotPerClientPublishesConsistentSnapshotForUnreferencedEDSClusterWithoutEndpoints(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniquelyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, []ir.UniquelyConnectedClient{ucc})

	listeners := sliceToResources([]*envoylistenerv3.Listener{httpListenerWithRDS(t, "listener", "route-config")})
	routes := routeResourcesForClusters() // no route references any cluster
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}})

	// An unreferenced EDS cluster in CDS, with no endpoints anywhere.
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		edsClusterForClient(ucc, "cluster-0", 1),
	})
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, nil)

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
			index: krtpkg.UnnamedIndex(clusterCol, func(c uccWithCluster) []string {
				return []string{c.Client.ResourceName()}
			}),
		},
	)

	snap := eventuallySingleSnapshot(t, snapshots)
	g.Expect(snap.Consistent()).ToNot(gomega.HaveOccurred(),
		"published snapshot must be EDS-consistent: the unreferenced EDS cluster gets a synthesized empty CLA")
	g.Expect(snap.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-0"))
	g.Expect(snap.Resources[envoycachetypes.Endpoint].Items).To(gomega.HaveKey("cluster-0"),
		"the EDS cluster must have a (synthesized empty) ClusterLoadAssignment")
	assertNoXDSCheckErrors(t, snap)
}
