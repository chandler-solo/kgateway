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
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// TestSnapshotPerClientPublishesWhenReferencedEDSClusterCLAAbsent verifies the
// inverse of the old defer-forever behavior: a referenced EDS cluster that is
// present in CDS but whose ClusterLoadAssignment has not yet arrived must NOT
// freeze the whole client's snapshot forever. The snapshot publishes anyway and
// Envoy relies on its initial_fetch_timeout for the genuinely-late CLA. A
// present-but-empty CLA is a valid steady state for Kube Services, so an absent
// CLA is not a reason to strand the client on stale endpoints indefinitely.
func TestSnapshotPerClientPublishesWhenReferencedEDSClusterCLAAbsent(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})

	uccs := krt.NewStaticCollection[ir.UniqlyConnectedClient](nil, []ir.UniqlyConnectedClient{ucc})
	routes := sliceToResources([]*envoyroutev3.RouteConfiguration{
		{
			Name: "route-config",
			VirtualHosts: []*envoyroutev3.VirtualHost{
				{
					Name:    "vhost",
					Domains: []string{"*"},
					Routes: []*envoyroutev3.Route{
						{
							Name: "eds-route",
							Action: &envoyroutev3.Route_Route{
								Route: &envoyroutev3.RouteAction{
									ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "cluster-a"},
								},
							},
						},
					},
				},
			},
		},
	})
	listeners := sliceToResources([]*envoylistenerv3.Listener{{Name: "listener"}})
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}})
	// cluster-a is an EDS cluster present in CDS, but no CLA is ever delivered.
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{
			Client: ucc,
			Name:   "cluster-a",
			Cluster: &envoyclusterv3.Cluster{
				Name: "cluster-a",
				ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
					Type: envoyclusterv3.Cluster_EDS,
				},
			},
			ClusterVersion: 1,
		},
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
			index: krtpkg.UnnamedIndex(clusterCol, func(cluster uccWithCluster) []string {
				return []string{cluster.Client.ResourceName()}
			}),
		},
	)

	g.Eventually(func() int {
		return len(snapshots.List())
	}, 2*time.Second, 20*time.Millisecond).Should(gomega.Equal(1),
		"a referenced EDS cluster without a CLA must not freeze the snapshot forever")

	snap := snapshots.List()[0].snap
	g.Expect(snap.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-a"),
		"the published snapshot must contain the referenced EDS cluster")
}

// TestSnapshotPerClientPublishesPresentCLAWhenOtherClusterCLAMissing verifies
// that a missing CLA for one referenced EDS cluster (cluster-b) does not prevent
// another referenced EDS cluster's (cluster-a) present CLA from being published
// in the same snapshot. The whole client is no longer stranded just because a
// single cluster's CLA is still in flight.
func TestSnapshotPerClientPublishesPresentCLAWhenOtherClusterCLAMissing(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})

	uccs := krt.NewStaticCollection[ir.UniqlyConnectedClient](nil, []ir.UniqlyConnectedClient{ucc})
	routes := sliceToResources([]*envoyroutev3.RouteConfiguration{
		{
			Name: "route-config",
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
										WeightedClusters: &envoyroutev3.WeightedCluster{
											Clusters: []*envoyroutev3.WeightedCluster_ClusterWeight{
												{Name: "cluster-a", Weight: wrapperspb.UInt32(1)},
												{Name: "cluster-b", Weight: wrapperspb.UInt32(1)},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	listeners := sliceToResources([]*envoylistenerv3.Listener{{Name: "listener"}})
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}})
	// Both cluster-a and cluster-b are EDS clusters present in CDS.
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{
			Client: ucc,
			Name:   "cluster-a",
			Cluster: &envoyclusterv3.Cluster{
				Name: "cluster-a",
				ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
					Type: envoyclusterv3.Cluster_EDS,
				},
			},
			ClusterVersion: 1,
		},
		{
			Client: ucc,
			Name:   "cluster-b",
			Cluster: &envoyclusterv3.Cluster{
				Name: "cluster-b",
				ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
					Type: envoyclusterv3.Cluster_EDS,
				},
			},
			ClusterVersion: 2,
		},
	})
	// Only cluster-a's CLA has arrived (an explicit empty CLA is a valid steady
	// state). cluster-b's CLA is still missing.
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, []UccWithEndpoints{
		{
			Client: ucc,
			Endpoints: &envoyendpointv3.ClusterLoadAssignment{
				ClusterName: "cluster-a",
			},
			EndpointsHash: 3,
			endpointsName: "cluster-a",
		},
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

	g.Eventually(func() int {
		return len(snapshots.List())
	}, 2*time.Second, 20*time.Millisecond).Should(gomega.Equal(1),
		"a missing CLA for cluster-b must not prevent publishing cluster-a's CLA")

	snap := snapshots.List()[0].snap
	g.Expect(snap.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-a"))
	g.Expect(snap.Resources[envoycachetypes.Cluster].Items).To(gomega.HaveKey("cluster-b"))
	g.Expect(snap.Resources[envoycachetypes.Endpoint].Items).To(gomega.HaveKey("cluster-a"),
		"cluster-a's present CLA must be published even though cluster-b's CLA is missing")
}
