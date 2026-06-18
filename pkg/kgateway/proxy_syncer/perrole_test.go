package proxy_syncer

import (
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/onsi/gomega"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// roleSnapshot builds a single-vhost RouteConfiguration referencing the given
// clusters plus a minimal Listener, and returns a GatewayXdsResources keyed by
// the (ns, name) role. Mirrors the helpers in perclient_test.go.
func roleSnapshot(ns, name string, weightedClusters ...string) GatewayXdsResources {
	clusterWeights := make([]*envoyroutev3.WeightedCluster_ClusterWeight, 0, len(weightedClusters))
	for _, c := range weightedClusters {
		clusterWeights = append(clusterWeights, &envoyroutev3.WeightedCluster_ClusterWeight{Name: c, Weight: wrapperspb.UInt32(1)})
	}
	routes := sliceToResources([]*envoyroutev3.RouteConfiguration{
		{
			Name: "route-config-" + name,
			VirtualHosts: []*envoyroutev3.VirtualHost{
				{
					Name:    "vhost",
					Domains: []string{"*"},
					Routes: []*envoyroutev3.Route{
						{
							Name: "weighted-route",
							Action: &envoyroutev3.Route_Route{
								Route: &envoyroutev3.RouteAction{
									ClusterSpecifier: &envoyroutev3.RouteAction_WeightedClusters{
										WeightedClusters: &envoyroutev3.WeightedCluster{Clusters: clusterWeights},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	listeners := sliceToResources([]*envoylistenerv3.Listener{{Name: "listener-" + name}})
	return GatewayXdsResources{
		NamespacedName:     types.NamespacedName{Namespace: ns, Name: name},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}
}

// TestSnapshotPerRoleDefersUntilReferencedClustersAreReady proves the #13868
// coherence guard is preserved on the locality-agnostic path: a role whose route
// references a cluster that has not yet been translated publishes nothing, and
// begins publishing once the missing cluster appears.
func TestSnapshotPerRoleDefersUntilReferencedClustersAreReady(t *testing.T) {
	g := gomega.NewWithT(t)

	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{
		roleSnapshot("ns", "gw", "cluster-a", "cluster-b"),
	})
	// Backend-keyed (client-agnostic) clusters: only cluster-a is present at first.
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{Client: agnosticUCC, Name: "cluster-a", Cluster: &envoyclusterv3.Cluster{Name: "cluster-a"}, ClusterVersion: 1},
	})
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, nil)

	snapshots := snapshotPerRole(krtutil.KrtOptions{}, mostXdsSnapshots, endpointCol, clusterCol)

	g.Consistently(func() int {
		return len(snapshots.List())
	}, 200*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(0), "must defer while cluster-b is missing")

	clusterCol.UpdateObject(uccWithCluster{Client: agnosticUCC, Name: "cluster-b", Cluster: &envoyclusterv3.Cluster{Name: "cluster-b"}, ClusterVersion: 2})

	g.Eventually(func() int {
		return len(snapshots.List())
	}, time.Second, 20*time.Millisecond).Should(gomega.Equal(1))

	snap := snapshots.List()[0].snap
	g.Expect(snap.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-a"))
	g.Expect(snap.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-b"))
}

// TestSnapshotPerRoleDefersUntilReferencedEDSClustersHaveEndpoints proves the
// EDS coherence guard is preserved per role: an EDS cluster without its CLA
// defers publication until the CLA arrives.
func TestSnapshotPerRoleDefersUntilReferencedEDSClustersHaveEndpoints(t *testing.T) {
	g := gomega.NewWithT(t)

	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{
		roleSnapshot("ns", "gw", "cluster-a"),
	})
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{
			Client: agnosticUCC,
			Name:   "cluster-a",
			Cluster: &envoyclusterv3.Cluster{
				Name:                 "cluster-a",
				ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
			},
			ClusterVersion: 1,
		},
	})
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, nil)

	snapshots := snapshotPerRole(krtutil.KrtOptions{}, mostXdsSnapshots, endpointCol, clusterCol)

	g.Consistently(func() int {
		return len(snapshots.List())
	}, 200*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(0), "must defer while EDS CLA is missing")

	endpointCol.UpdateObject(UccWithEndpoints{
		Client:        agnosticUCC,
		Endpoints:     &envoyendpointv3.ClusterLoadAssignment{ClusterName: "cluster-a"},
		EndpointsHash: 3,
		endpointsName: "cluster-a",
	})

	g.Eventually(func() int {
		return len(snapshots.List())
	}, time.Second, 20*time.Millisecond).Should(gomega.Equal(1))

	snap := snapshots.List()[0].snap
	g.Expect(snap.Resources[envoycachetypes.Endpoint].Items).To(gomega.HaveKey("cluster-a"))
}

// TestSnapshotPerRoleBuildsOneSnapshotPerRole proves the locality-agnostic graph
// produces exactly one snapshot per gateway role (keyed by role, not by
// connected client) and that the single client-agnostic backend cluster is
// shared across both roles' snapshots.
func TestSnapshotPerRoleBuildsOneSnapshotPerRole(t *testing.T) {
	g := gomega.NewWithT(t)

	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{
		roleSnapshot("ns", "gw1", "cluster-a"),
		roleSnapshot("ns", "gw2", "cluster-a"),
	})
	// A single backend-keyed cluster row, shared by every role (no per-client fan-out).
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{Client: agnosticUCC, Name: "cluster-a", Cluster: &envoyclusterv3.Cluster{Name: "cluster-a"}, ClusterVersion: 1},
	})
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, nil)

	snapshots := snapshotPerRole(krtutil.KrtOptions{}, mostXdsSnapshots, endpointCol, clusterCol)

	g.Eventually(func() int {
		return len(snapshots.List())
	}, time.Second, 20*time.Millisecond).Should(gomega.Equal(2), "one snapshot per gateway role")

	wantKeys := map[string]bool{
		xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw1"): false,
		xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw2"): false,
	}
	for _, w := range snapshots.List() {
		_, ok := wantKeys[w.proxyKey]
		g.Expect(ok).To(gomega.BeTrue(), "unexpected proxyKey %q", w.proxyKey)
		wantKeys[w.proxyKey] = true
		g.Expect(w.snap.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-a"))
	}
	for key, seen := range wantKeys {
		g.Expect(seen).To(gomega.BeTrue(), "missing snapshot for role %q", key)
	}
}
