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
	envoydiscoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	envoystreamv3 "github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
	"github.com/onsi/gomega"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// These tests verify ADS respondability end to end against a real
// go-control-plane SnapshotCache: in state-of-the-world ADS mode, the cache
// answers a named EDS watch only if the snapshot's EDS version differs from the
// watch's and every EDS resource in the snapshot is named in the request. Two
// properties of the published snapshot make that hold:
//
//   - the EDS subset filter (S2) drops CLAs for removed/STATIC clusters, and
//   - the filtered EDS version is derived from the FILTERED content, so any
//     change to the published CLA set — including a cluster being removed and
//     later re-added while the underlying endpoint inputs are unchanged —
//     produces a new version. A version derived from the unfiltered inputs
//     would stay constant across such transitions and leave the watch
//     "up to date", stalling Envoy on initial_fetch_timeout.

func respondabilityRoutes(clusterNames ...string) envoycache.Resources {
	routes := make([]*envoyroutev3.Route, 0, len(clusterNames))
	for _, clusterName := range clusterNames {
		routes = append(routes, &envoyroutev3.Route{
			Name: "route-" + clusterName,
			Action: &envoyroutev3.Route_Route{
				Route: &envoyroutev3.RouteAction{
					ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: clusterName},
				},
			},
		})
	}
	return sliceToResources([]*envoyroutev3.RouteConfiguration{{
		Name: "route-config",
		VirtualHosts: []*envoyroutev3.VirtualHost{{
			Name:    "vhost",
			Domains: []string{"*"},
			Routes:  routes,
		}},
	}})
}

func respondabilityEdsCluster(ucc ir.UniqlyConnectedClient, name string, version uint64) uccWithCluster {
	return uccWithCluster{
		Client: ucc,
		Name:   name,
		Cluster: &envoyclusterv3.Cluster{
			Name:                 name,
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
		},
		ClusterVersion: version,
	}
}

func respondabilityEdsClusterWithServiceName(ucc ir.UniqlyConnectedClient, name, serviceName string, version uint64) uccWithCluster {
	cluster := respondabilityEdsCluster(ucc, name, version)
	cluster.Cluster.EdsClusterConfig = &envoyclusterv3.Cluster_EdsClusterConfig{
		ServiceName: serviceName,
	}
	return cluster
}

// respondabilityEndpoints builds a CLA row; the fixture assigns the client.
func respondabilityEndpoints(name string, hash uint64) UccWithEndpoints {
	return UccWithEndpoints{
		Endpoints: &envoyendpointv3.ClusterLoadAssignment{
			ClusterName: name,
			Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{
				LbEndpoints: []*envoyendpointv3.LbEndpoint{{
					HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{
						Endpoint: &envoyendpointv3.Endpoint{
							Address: &envoycorev3.Address{
								Address: &envoycorev3.Address_SocketAddress{
									SocketAddress: &envoycorev3.SocketAddress{
										Address:       "127.0.0.1",
										PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 8080},
									},
								},
							},
						},
					},
				}},
			}},
		},
		EndpointsHash: hash,
		endpointsName: name,
	}
}

type respondabilityFixture struct {
	ucc              ir.UniqlyConnectedClient
	listeners        envoycache.Resources
	mostXdsSnapshots krt.StaticCollection[GatewayXdsResources]
	clusterCol       krt.StaticCollection[uccWithCluster]
	snapshots        krt.Collection[XdsSnapWrapper]
	initial          GatewayXdsResources
}

func newRespondabilityFixture(
	clusters []uccWithCluster,
	endpoints []UccWithEndpoints,
	routes envoycache.Resources,
) *respondabilityFixture {
	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
	for i := range clusters {
		clusters[i].Client = ucc
	}
	for i := range endpoints {
		endpoints[i].Client = ucc
	}

	uccs := krt.NewStaticCollection[ir.UniqlyConnectedClient](nil, []ir.UniqlyConnectedClient{ucc})
	listeners := sliceToResources([]*envoylistenerv3.Listener{{Name: "listener"}})
	initial := GatewayXdsResources{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{initial})
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, clusters)
	endpointCol := krt.NewStaticCollection[UccWithEndpoints](nil, endpoints)

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

	return &respondabilityFixture{
		ucc:              ucc,
		listeners:        listeners,
		mostXdsSnapshots: mostXdsSnapshots,
		clusterCol:       clusterCol,
		snapshots:        snapshots,
		initial:          initial,
	}
}

func (f *respondabilityFixture) retargetRoutes(clusterNames ...string) {
	routes := respondabilityRoutes(clusterNames...)
	updated := f.initial
	updated.Routes = routes
	updated.ReferencedClusters = collectReferencedClusters(routes, f.listeners)
	f.mostXdsSnapshots.UpdateObject(updated)
}

func (f *respondabilityFixture) eventuallySnapshotWithEndpointKeys(t *testing.T, want, dontWant []string) *envoycache.Snapshot {
	t.Helper()
	g := gomega.NewWithT(t)
	var snap *envoycache.Snapshot
	g.Eventually(func() bool {
		list := f.snapshots.List()
		if len(list) != 1 || list[0].needsResolution() {
			return false
		}
		snap = list[0].snap
		items := snap.Resources[envoycachetypes.Endpoint].Items
		for _, k := range want {
			if _, ok := items[k]; !ok {
				return false
			}
		}
		for _, k := range dontWant {
			if _, ok := items[k]; ok {
				return false
			}
		}
		return true
	}, time.Second, 20*time.Millisecond).Should(gomega.BeTrue(),
		"snapshot should contain CLAs %v and not %v", want, dontWant)
	return snap
}

// expectNamedEdsWatchResponds sets the snapshot on a real ADS SnapshotCache and
// asserts a named SotW EDS watch, last synced at lastVersion with lastReturned
// resource versions, receives a response containing exactly wantResources.
func expectNamedEdsWatchResponds(
	t *testing.T,
	snap *envoycache.Snapshot,
	nodeID string,
	names []string,
	lastVersion string,
	lastReturned map[string]string,
	wantResources []string,
) {
	t.Helper()
	g := gomega.NewWithT(t)

	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	g.Expect(cache.SetSnapshot(context.Background(), nodeID, snap)).ToNot(gomega.HaveOccurred())

	req := &envoydiscoveryv3.DiscoveryRequest{
		Node:          &envoycorev3.Node{Id: nodeID},
		TypeUrl:       envoyresourcev3.EndpointType,
		ResourceNames: names,
		VersionInfo:   lastVersion,
	}
	sub := envoystreamv3.NewSotwSubscription(req.GetResourceNames(), true)
	sub.SetReturnedResources(lastReturned)
	responses := make(chan envoycache.Response, 1)
	_, err := cache.CreateWatch(req, sub, responses)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	wantVersion := snap.Resources[envoycachetypes.Endpoint].Version
	select {
	case response := <-responses:
		g.Expect(response.GetResponseVersion()).To(gomega.Equal(wantVersion))
		for _, name := range wantResources {
			g.Expect(response.GetReturnedResources()).To(gomega.HaveKeyWithValue(name, wantVersion))
		}
		discoveryResponse, err := response.GetDiscoveryResponse()
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(discoveryResponse.GetResources()).To(gomega.HaveLen(len(wantResources)))
	case <-time.After(time.Second):
		t.Fatal("expected the snapshot to answer the named ADS EDS request")
	}
}

// After a cluster is removed, the filtered EDS set must get a NEW version and
// the cache must answer the narrowed named EDS request.
func TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved(t *testing.T) {
	g := gomega.NewWithT(t)
	f := newRespondabilityFixture(
		[]uccWithCluster{
			respondabilityEdsCluster(ir.UniqlyConnectedClient{}, "cluster-a", 1),
			respondabilityEdsCluster(ir.UniqlyConnectedClient{}, "cluster-b", 2),
		},
		[]UccWithEndpoints{
			respondabilityEndpoints("cluster-a", 3),
			respondabilityEndpoints("cluster-b", 4),
		},
		respondabilityRoutes("cluster-a", "cluster-b"),
	)

	initialSnap := f.eventuallySnapshotWithEndpointKeys(t, []string{"cluster-a", "cluster-b"}, nil)
	initialVersion := initialSnap.Resources[envoycachetypes.Endpoint].Version

	f.retargetRoutes("cluster-a")
	f.clusterCol.DeleteObject(f.ucc.ResourceName() + "/cluster-b")

	updatedSnap := f.eventuallySnapshotWithEndpointKeys(t, []string{"cluster-a"}, []string{"cluster-b"})
	updatedVersion := updatedSnap.Resources[envoycachetypes.Endpoint].Version
	g.Expect(updatedVersion).ToNot(gomega.Equal(initialVersion),
		"removing a cluster changes the published EDS set, so the version must change")

	expectNamedEdsWatchResponds(t, updatedSnap, f.ucc.ResourceName(),
		[]string{"cluster-a"},
		initialVersion,
		map[string]string{"cluster-a": initialVersion, "cluster-b": initialVersion},
		[]string{"cluster-a"},
	)
}

// Same property when the EDS cluster names its CLA via EdsClusterConfig.ServiceName.
func TestSnapshotPerClientServiceNameEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved(t *testing.T) {
	g := gomega.NewWithT(t)
	f := newRespondabilityFixture(
		[]uccWithCluster{
			respondabilityEdsClusterWithServiceName(ir.UniqlyConnectedClient{}, "cluster-a", "service-a", 1),
			respondabilityEdsClusterWithServiceName(ir.UniqlyConnectedClient{}, "cluster-b", "service-b", 2),
		},
		[]UccWithEndpoints{
			respondabilityEndpoints("service-a", 3),
			respondabilityEndpoints("service-b", 4),
		},
		respondabilityRoutes("cluster-a", "cluster-b"),
	)

	initialSnap := f.eventuallySnapshotWithEndpointKeys(t, []string{"service-a", "service-b"}, nil)
	initialVersion := initialSnap.Resources[envoycachetypes.Endpoint].Version

	f.retargetRoutes("cluster-a")
	f.clusterCol.DeleteObject(f.ucc.ResourceName() + "/cluster-b")

	updatedSnap := f.eventuallySnapshotWithEndpointKeys(t, []string{"service-a"}, []string{"service-b"})
	g.Expect(updatedSnap.Resources[envoycachetypes.Endpoint].Version).ToNot(gomega.Equal(initialVersion))

	expectNamedEdsWatchResponds(t, updatedSnap, f.ucc.ResourceName(),
		[]string{"service-a"},
		initialVersion,
		map[string]string{"service-a": initialVersion, "service-b": initialVersion},
		[]string{"service-a"},
	)
}

// The inverse transition: a cluster removed and then RE-ADDED with unchanged
// endpoint inputs must also produce a version change relative to the narrowed
// snapshot, so a watch synced during the removal window is answered with the
// re-added CLA instead of stalling on initial_fetch_timeout.
func TestSnapshotPerClientEdsSnapshotRespondsAfterClusterReAdded(t *testing.T) {
	g := gomega.NewWithT(t)
	f := newRespondabilityFixture(
		[]uccWithCluster{
			respondabilityEdsCluster(ir.UniqlyConnectedClient{}, "cluster-a", 1),
			respondabilityEdsCluster(ir.UniqlyConnectedClient{}, "cluster-b", 2),
		},
		[]UccWithEndpoints{
			respondabilityEndpoints("cluster-a", 3),
			respondabilityEndpoints("cluster-b", 4),
		},
		respondabilityRoutes("cluster-a", "cluster-b"),
	)

	f.eventuallySnapshotWithEndpointKeys(t, []string{"cluster-a", "cluster-b"}, nil)

	// Remove cluster-b (its CLA is filtered from the published set).
	f.retargetRoutes("cluster-a")
	f.clusterCol.DeleteObject(f.ucc.ResourceName() + "/cluster-b")
	narrowedSnap := f.eventuallySnapshotWithEndpointKeys(t, []string{"cluster-a"}, []string{"cluster-b"})
	narrowedVersion := narrowedSnap.Resources[envoycachetypes.Endpoint].Version

	// Re-add cluster-b; the underlying endpoint inputs never changed.
	f.clusterCol.UpdateObject(respondabilityEdsCluster(f.ucc, "cluster-b", 2))
	f.retargetRoutes("cluster-a", "cluster-b")
	readdedSnap := f.eventuallySnapshotWithEndpointKeys(t, []string{"cluster-a", "cluster-b"}, nil)
	readdedVersion := readdedSnap.Resources[envoycachetypes.Endpoint].Version
	g.Expect(readdedVersion).ToNot(gomega.Equal(narrowedVersion),
		"re-adding a cluster changes the published EDS set, so the version must change")

	// A watch that synced during the removal window asks for both names again;
	// the cache must answer with the re-added CLA.
	expectNamedEdsWatchResponds(t, readdedSnap, f.ucc.ResourceName(),
		[]string{"cluster-a", "cluster-b"},
		narrowedVersion,
		map[string]string{"cluster-a": narrowedVersion},
		[]string{"cluster-a", "cluster-b"},
	)
}
