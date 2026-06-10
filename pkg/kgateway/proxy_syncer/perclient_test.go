package proxy_syncer

import (
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	envoyaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoygrpcaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/grpc/v3"
	envoyextauthzv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_authz/v3"
	envoyjwtauthnv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/jwt_authn/v3"
	envoyhttpv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/onsi/gomega"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

func reconcileTestEndpoints(resources envoycache.Resources) endpointsWithUccName {
	return endpointsWithUccName{endpoints: resources}
}

func TestFilterEndpointResourcesForClusters_FiltersStaticClusterCLAs(t *testing.T) {
	// Clusters: one STATIC ("static-cluster"), one EDS ("eds-cluster").
	// Endpoints: CLAs for both. Expect only CLA for "eds-cluster" in result.
	clusters := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{Name: "static-cluster", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC}}},
		{Resource: &envoyclusterv3.Cluster{Name: "eds-cluster", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}}},
	})
	endpoints := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "static-cluster"}},
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "eds-cluster"}},
	})

	out := filterEndpointResourcesForClusters(clusters, reconcileTestEndpoints(endpoints))

	if len(out.Items) != 1 {
		t.Fatalf("expected 1 endpoint resource, got %d", len(out.Items))
	}
	if _, ok := out.Items["eds-cluster"]; !ok {
		t.Errorf("expected CLA for eds-cluster to remain, got keys: %v", slices.Collect(maps.Keys(out.Items)))
	}
	if _, ok := out.Items["static-cluster"]; ok {
		t.Error("expected CLA for static-cluster to be filtered out")
	}
}

func TestFilterEndpointResourcesForClusters_KeepsRequiredCLAs(t *testing.T) {
	// Only EDS clusters; their CLAs are all required and must survive. The
	// version is deterministic for identical inputs.
	clusters := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{Name: "eds-only", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}}},
	})
	endpoints := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "eds-only"}},
	})

	out := filterEndpointResourcesForClusters(clusters, reconcileTestEndpoints(endpoints))

	if len(out.Items) != 1 {
		t.Fatalf("expected 1 endpoint resource, got %d", len(out.Items))
	}
	if _, ok := out.Items["eds-only"]; !ok {
		t.Errorf("expected CLA for eds-only, got keys: %v", slices.Collect(maps.Keys(out.Items)))
	}
	again := filterEndpointResourcesForClusters(clusters, reconcileTestEndpoints(endpoints))
	if out.Version != again.Version {
		t.Errorf("version must be deterministic: got %q then %q", out.Version, again.Version)
	}
}

func TestFilterEndpointResourcesForClusters_EmptyClustersAndEndpoints(t *testing.T) {
	emptyClusters := envoycache.NewResourcesWithTTL("v1", nil)
	emptyEndpoints := envoycache.NewResourcesWithTTL("v1", nil)

	out := filterEndpointResourcesForClusters(emptyClusters, reconcileTestEndpoints(emptyEndpoints))

	if len(out.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(out.Items))
	}
}

func TestFilterEndpointResourcesForClusters_DropsStaleCLAs(t *testing.T) {
	// A CLA whose cluster is no longer in CDS is stale: publishing it makes
	// go-control-plane suppress named state-of-the-world ADS EDS responses,
	// so it must be dropped.
	emptyClusters := envoycache.NewResourcesWithTTL("v1", nil)
	endpoints := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "removed-cluster"}},
	})

	out := filterEndpointResourcesForClusters(emptyClusters, reconcileTestEndpoints(endpoints))

	if len(out.Items) != 0 {
		t.Fatalf("expected stale CLA to be dropped, got %d items: %v", len(out.Items), slices.Collect(maps.Keys(out.Items)))
	}
}

func TestFilterEndpointResourcesForClusters_VersionChangesWhenCLAArrives(t *testing.T) {
	// A required-but-missing CLA leaves the filtered set without it (guard 3
	// defers the snapshot meanwhile); when the CLA arrives, the version must
	// change so a state-of-the-world EDS watch is answered (respondability).
	clusters := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{Name: "eds-cluster", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}}},
	})
	emptyEndpoints := envoycache.NewResourcesWithTTL("v1", nil)

	out := filterEndpointResourcesForClusters(clusters, reconcileTestEndpoints(emptyEndpoints))
	if len(out.Items) != 0 {
		t.Fatalf("expected 0 items while the required CLA is missing, got %v", slices.Collect(maps.Keys(out.Items)))
	}

	realEndpoints := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "eds-cluster"}},
	})
	healed := filterEndpointResourcesForClusters(clusters,
		endpointsWithUccName{endpoints: realEndpoints, claHashes: map[string]uint64{"eds-cluster": 42}})
	if _, ok := healed.Items["eds-cluster"]; !ok {
		t.Fatalf("expected the real CLA to be kept, got %v", slices.Collect(maps.Keys(healed.Items)))
	}
	if healed.Version == out.Version {
		t.Error("version must change when the required CLA arrives (EDS respondability)")
	}
}

func TestFilterEndpointResourcesForClusters_StaticOnlyEmptyEndpoints(t *testing.T) {
	clusters := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{Name: "static-cluster", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC}}},
	})
	emptyEndpoints := envoycache.NewResourcesWithTTL("v1", nil)

	out := filterEndpointResourcesForClusters(clusters, reconcileTestEndpoints(emptyEndpoints))

	if len(out.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(out.Items))
	}
}

func TestFilterEndpointResourcesForClusters_MixedStaticAndNonStatic(t *testing.T) {
	// Two STATIC, two EDS. CLAs for all four. Result should have only the two EDS CLAs.
	clusters := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{Name: "static-a", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC}}},
		{Resource: &envoyclusterv3.Cluster{Name: "eds-a", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}}},
		{Resource: &envoyclusterv3.Cluster{Name: "static-b", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC}}},
		{Resource: &envoyclusterv3.Cluster{Name: "eds-b", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}}},
	})
	endpoints := envoycache.NewResourcesWithTTL("v1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "static-a"}},
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "eds-a"}},
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "static-b"}},
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "eds-b"}},
	})

	out := filterEndpointResourcesForClusters(clusters, reconcileTestEndpoints(endpoints))

	if len(out.Items) != 2 {
		t.Fatalf("expected 2 endpoint resources (eds-a, eds-b), got %d: %v", len(out.Items), slices.Collect(maps.Keys(out.Items)))
	}
	if _, ok := out.Items["eds-a"]; !ok {
		t.Errorf("expected CLA for eds-a, got keys: %v", slices.Collect(maps.Keys(out.Items)))
	}
	if _, ok := out.Items["eds-b"]; !ok {
		t.Errorf("expected CLA for eds-b, got keys: %v", slices.Collect(maps.Keys(out.Items)))
	}
	if _, ok := out.Items["static-a"]; ok {
		t.Error("expected static-a CLA to be filtered out")
	}
	if _, ok := out.Items["static-b"]; ok {
		t.Error("expected static-b CLA to be filtered out")
	}
}

func TestSnapshotPerClientDefersWhenReferencedClusterMissing(t *testing.T) {
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
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{
			Client:         ucc,
			Name:           "cluster-a",
			Cluster:        &envoyclusterv3.Cluster{Name: "cluster-a"},
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
		nil,
	)

	// Guard 2: the snapshot is WITHHELD while a referenced dataplane cluster
	// is absent — publishing a partial CDS would remove working resources from
	// a client that already holds a full snapshot.
	g.Consistently(func() int {
		return len(snapshots.List())
	}, 500*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(0),
		"snapshot must be deferred while a referenced cluster is missing")

	clusterCol.UpdateObject(uccWithCluster{
		Client:         ucc,
		Name:           "cluster-b",
		Cluster:        &envoyclusterv3.Cluster{Name: "cluster-b"},
		ClusterVersion: 2,
	})

	g.Eventually(func() bool {
		list := snapshots.List()
		if len(list) != 1 {
			return false
		}
		items := list[0].snap.Resources[envoycachetypes.Cluster].Items
		_, hasA := items["cluster-a"]
		_, hasB := items["cluster-b"]
		return hasA && hasB
	}, time.Second, 20*time.Millisecond).Should(gomega.BeTrue(),
		"once the cluster appears, the consistent snapshot publishes")
}

func TestSnapshotPerClientDefersUntilEDSClusterHasCLA(t *testing.T) {
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
		nil,
	)

	// Guard 3: the snapshot is WITHHELD while a referenced EDS cluster lacks
	// its CLA — publishing CDS/RDS/LDS before EDS catches up can make Envoy
	// drop all hosts for a route that was healthy before a reconnect.
	g.Consistently(func() int {
		return len(snapshots.List())
	}, 500*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(0),
		"snapshot must be deferred while a referenced EDS cluster's CLA is missing")

	endpointCol.UpdateObject(UccWithEndpoints{
		Client: ucc,
		Endpoints: &envoyendpointv3.ClusterLoadAssignment{
			ClusterName: "cluster-a",
		},
		EndpointsHash: 3,
		endpointsName: "cluster-a",
	})

	g.Eventually(func() bool {
		list := snapshots.List()
		if len(list) != 1 {
			return false
		}
		_, ok := list[0].snap.Resources[envoycachetypes.Endpoint].Items["cluster-a"]
		return ok
	}, time.Second, 20*time.Millisecond).Should(gomega.BeTrue(),
		"once the CLA appears, the consistent snapshot publishes")
}

func TestSnapshotPerClientStillPublishesWhenReferencedClusterErrored(t *testing.T) {
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
							Name: "single-route",
							Action: &envoyroutev3.Route_Route{
								Route: &envoyroutev3.RouteAction{
									ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "cluster-a"},
								},
							},
						},
						{
							Name: "errored-route",
							Action: &envoyroutev3.Route_Route{
								Route: &envoyroutev3.RouteAction{
									ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "cluster-b"},
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
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{
			Client:         ucc,
			Name:           "cluster-a",
			Cluster:        &envoyclusterv3.Cluster{Name: "cluster-a"},
			ClusterVersion: 1,
		},
		{
			Client:         ucc,
			Name:           "cluster-b",
			Cluster:        &envoyclusterv3.Cluster{Name: "cluster-b"},
			ClusterVersion: 2,
			Error:          errors.New("boom"),
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
		nil,
	)

	g.Eventually(func() int {
		return len(snapshots.List())
	}, time.Second, 20*time.Millisecond).Should(gomega.Equal(1))

	snap := snapshots.List()[0]
	g.Expect(snap.erroredClusters).To(gomega.ConsistOf("cluster-b"))
	g.Expect(snap.snap.Resources[envoycachetypes.Cluster].Items).ToNot(gomega.HaveKey("cluster-b"))
}

// TestCollectReferencedClusters_ExcludesAncillaryReferences verifies that
// cluster names reachable only through ancillary / control-plane
// typed_config (access-log GrpcService, JWT jwks HttpUri, etc.) are
// deliberately NOT treated as gated dataplane targets. The plugin that
// emits these filters is responsible for also emitting the referenced
// cluster in the same per-gateway snapshot's ExtraClusters, so there is
// no reconnect race between them. Gating on ancillary references would
// starve the gateway forever on a plugin bug — which is not what this
// readiness guard is for.
func TestCollectReferencedClusters_ExcludesAncillaryReferences(t *testing.T) {
	g := gomega.NewWithT(t)

	hcm := &envoyhttpv3.HttpConnectionManager{
		AccessLog: []*envoyaccesslogv3.AccessLog{
			{
				Name: "envoy.access_loggers.http_grpc",
				ConfigType: &envoyaccesslogv3.AccessLog_TypedConfig{
					TypedConfig: utils.MustMessageToAny(&envoygrpcaccesslogv3.HttpGrpcAccessLogConfig{
						CommonConfig: &envoygrpcaccesslogv3.CommonGrpcAccessLogConfig{
							TransportApiVersion: envoycorev3.ApiVersion_V3,
							LogName:             "grpc-log",
							GrpcService: &envoycorev3.GrpcService{
								TargetSpecifier: &envoycorev3.GrpcService_EnvoyGrpc_{
									EnvoyGrpc: &envoycorev3.GrpcService_EnvoyGrpc{
										ClusterName: "access-log-cluster",
									},
								},
							},
						},
					}),
				},
			},
		},
		HttpFilters: []*envoyhttpv3.HttpFilter{
			{
				Name: "envoy.filters.http.jwt_authn",
				ConfigType: &envoyhttpv3.HttpFilter_TypedConfig{
					TypedConfig: utils.MustMessageToAny(&envoyjwtauthnv3.JwtAuthentication{
						Providers: map[string]*envoyjwtauthnv3.JwtProvider{
							"provider": {
								JwksSourceSpecifier: &envoyjwtauthnv3.JwtProvider_RemoteJwks{
									RemoteJwks: &envoyjwtauthnv3.RemoteJwks{
										HttpUri: &envoycorev3.HttpUri{
											Uri: "https://example.com/jwks",
											HttpUpstreamType: &envoycorev3.HttpUri_Cluster{
												Cluster: "jwks-cluster",
											},
											Timeout: durationpb.New(time.Second),
										},
									},
								},
							},
						},
					}),
				},
			},
		},
	}

	listeners := sliceToResources([]*envoylistenerv3.Listener{
		{
			Name: "listener",
			FilterChains: []*envoylistenerv3.FilterChain{
				{
					Filters: []*envoylistenerv3.Filter{
						{
							Name: envoywellknown.HTTPConnectionManager,
							ConfigType: &envoylistenerv3.Filter_TypedConfig{
								TypedConfig: utils.MustMessageToAny(hcm),
							},
						},
					},
				},
			},
		},
	})

	referenced := collectReferencedClusters(envoycache.Resources{}, listeners)

	g.Expect(referenced).ToNot(gomega.HaveKey("access-log-cluster"),
		"ancillary access-log cluster must not be treated as a gated dataplane target")
	g.Expect(referenced).ToNot(gomega.HaveKey("jwks-cluster"),
		"ancillary JWT jwks cluster must not be treated as a gated dataplane target")
}

// TestFindMissingReferencedClusters_HandlesScalarValueMaps verifies that the
// protoreflect walker does not panic when it encounters a proto field of type
// map<string, scalar> (e.g. map<string, string>). Without the IsMap/IsList
// guard on the fall-through branch, such fields fall through to a code path
// that calls v.Message() on a Map-kind Value and panics with
// "type mismatch: cannot convert map to message".
func TestFindMissingReferencedClusters_HandlesScalarValueMaps(t *testing.T) {
	g := gomega.NewWithT(t)

	hcm := &envoyhttpv3.HttpConnectionManager{
		HttpFilters: []*envoyhttpv3.HttpFilter{
			{
				Name: "envoy.filters.http.ext_authz",
				ConfigType: &envoyhttpv3.HttpFilter_TypedConfig{
					TypedConfig: utils.MustMessageToAny(&envoyextauthzv3.ExtAuthzPerRoute{
						Override: &envoyextauthzv3.ExtAuthzPerRoute_CheckSettings{
							CheckSettings: &envoyextauthzv3.CheckSettings{
								ContextExtensions: map[string]string{
									"key1": "value1",
									"key2": "value2",
								},
							},
						},
					}),
				},
			},
		},
	}

	listeners := sliceToResources([]*envoylistenerv3.Listener{
		{
			Name: "listener",
			FilterChains: []*envoylistenerv3.FilterChain{
				{
					Filters: []*envoylistenerv3.Filter{
						{
							Name: envoywellknown.HTTPConnectionManager,
							ConfigType: &envoylistenerv3.Filter_TypedConfig{
								TypedConfig: utils.MustMessageToAny(hcm),
							},
						},
					},
				},
			},
		},
	})

	g.Expect(func() {
		referenced := collectReferencedClusters(envoycache.Resources{}, listeners)
		findMissingReferencedClusters(referenced, nil, nil)
	}).ToNot(gomega.Panic())
}

// TestSnapshotPerClientPublishesEvenWithUnresolvableBackendRef verifies that
// a user BackendRef typo — e.g. an HTTPRoute pointing at a Service that does
// not exist — does not starve the readiness gate on startup. IR-time
// resolution substitutes wellknown.BlackholeClusterName for the unresolved
// target, and the gate explicitly skips blackhole so valid routes still
// reach Envoy.
func TestSnapshotPerClientPublishesEvenWithUnresolvableBackendRef(t *testing.T) {
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
							Name: "good-route",
							Action: &envoyroutev3.Route_Route{
								Route: &envoyroutev3.RouteAction{
									ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "cluster-a"},
								},
							},
						},
						{
							// Simulates a BackendRef whose target Service does
							// not exist: IR translation substitutes blackhole.
							Name: "typo-backendref-route",
							Action: &envoyroutev3.Route_Route{
								Route: &envoyroutev3.RouteAction{
									ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: wellknown.BlackholeClusterName},
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

	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{
			Client:         ucc,
			Name:           "cluster-a",
			Cluster:        &envoyclusterv3.Cluster{Name: "cluster-a"},
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
		nil,
	)

	g.Eventually(func() int {
		return len(snapshots.List())
	}, 2*time.Second, 20*time.Millisecond).Should(gomega.Equal(1),
		"a snapshot must publish so Envoy can serve the good route even when another route's BackendRef is unresolvable")
}

// TestSnapshotPerClientKeepsPublishingWhenMisconfiguredBackendRefArrivesAtRuntime
// verifies that a BackendRef pointing at a nonexistent Service arriving after
// the control plane is already serving does not cause the per-client snapshot
// to be withdrawn. IR translation substitutes blackhole for the unresolved
// target, which the gate skips, so the snapshot re-publishes with the new
// route set rather than being withdrawn. The existing good route keeps
// flowing; the typo'd route blackholes in Envoy — no 500/NC for valid
// traffic.
func TestSnapshotPerClientKeepsPublishingWhenMisconfiguredBackendRefArrivesAtRuntime(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection[ir.UniqlyConnectedClient](nil, []ir.UniqlyConnectedClient{ucc})

	goodRoutes := sliceToResources([]*envoyroutev3.RouteConfiguration{
		{
			Name: "route-config",
			VirtualHosts: []*envoyroutev3.VirtualHost{{
				Name:    "vhost",
				Domains: []string{"*"},
				Routes: []*envoyroutev3.Route{{
					Name: "good-route",
					Action: &envoyroutev3.Route_Route{
						Route: &envoyroutev3.RouteAction{
							ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "cluster-a"},
						},
					},
				}},
			}},
		},
	})
	listeners := sliceToResources([]*envoylistenerv3.Listener{{Name: "listener"}})
	initial := GatewayXdsResources{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             goodRoutes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(goodRoutes, listeners),
	}
	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{initial})

	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, []uccWithCluster{
		{
			Client:         ucc,
			Name:           "cluster-a",
			Cluster:        &envoyclusterv3.Cluster{Name: "cluster-a"},
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
		nil,
	)

	g.Eventually(func() int {
		return len(snapshots.List())
	}, time.Second, 20*time.Millisecond).Should(gomega.Equal(1))

	// A misconfigured route arrives whose BackendRef cannot be resolved;
	// IR translation substitutes blackhole.
	badRoutes := sliceToResources([]*envoyroutev3.RouteConfiguration{
		{
			Name: "route-config",
			VirtualHosts: []*envoyroutev3.VirtualHost{{
				Name:    "vhost",
				Domains: []string{"*"},
				Routes: []*envoyroutev3.Route{
					{
						Name: "good-route",
						Action: &envoyroutev3.Route_Route{
							Route: &envoyroutev3.RouteAction{
								ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "cluster-a"},
							},
						},
					},
					{
						Name: "typo-backendref-route",
						Action: &envoyroutev3.Route_Route{
							Route: &envoyroutev3.RouteAction{
								ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: wellknown.BlackholeClusterName},
							},
						},
					},
				},
			}},
		},
	})
	updated := initial
	updated.Routes = badRoutes
	updated.ReferencedClusters = collectReferencedClusters(badRoutes, listeners)
	mostXdsSnapshots.UpdateObject(updated)

	g.Consistently(func() int {
		return len(snapshots.List())
	}, 500*time.Millisecond, 20*time.Millisecond).Should(gomega.Equal(1),
		"snapshot must stay published after an unresolvable BackendRef arrives; withdrawing it strands Envoy on restart")
}

// TestSnapshotPerClientPublishesWhenAllRoutesAreRedirectOnly pins the
// zero-cluster behavior of snapshotPerClient: the per-client cluster
// transform ALWAYS emits a row, even with zero clusters (a gateway whose
// HTTPRoutes only use RequestRedirect or DirectResponse), so the snapshot
// builds and publishes rather than deferring. A nil row — as distinct from an
// empty one — unambiguously means "not derived yet" and defers; without the
// always-emit guarantee this distinction would not exist and a zero-cluster
// gateway would stall indefinitely.
func TestSnapshotPerClientPublishesWhenAllRoutesAreRedirectOnly(t *testing.T) {
	g := gomega.NewWithT(t)

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection[ir.UniqlyConnectedClient](nil, []ir.UniqlyConnectedClient{ucc})

	// Routes with only a Redirect action — no cluster references.
	routes := sliceToResources([]*envoyroutev3.RouteConfiguration{
		{
			Name: "route-config",
			VirtualHosts: []*envoyroutev3.VirtualHost{{
				Name:    "vhost",
				Domains: []string{"*"},
				Routes: []*envoyroutev3.Route{{
					Name: "redirect-only",
					Action: &envoyroutev3.Route_Redirect{
						Redirect: &envoyroutev3.RedirectAction{
							SchemeRewriteSpecifier: &envoyroutev3.RedirectAction_HttpsRedirect{HttpsRedirect: true},
						},
					},
				}},
			}},
		},
	})
	listeners := sliceToResources([]*envoylistenerv3.Listener{{Name: "listener"}})
	referencedClusters := collectReferencedClusters(routes, listeners)
	g.Expect(referencedClusters).To(gomega.BeEmpty(), "redirect-only routes must not produce cluster references")

	mostXdsSnapshots := krt.NewStaticCollection[GatewayXdsResources](nil, []GatewayXdsResources{{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: referencedClusters,
	}})

	// No per-client clusters at all for this UCC — simulates a deployment with
	// only redirect-only routes and no Service-backed backends contributing
	// clusters. clusterSnapshot's collection handler returns nil for this UCC.
	clusterCol := krt.NewStaticCollection[uccWithCluster](nil, nil)
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
		nil,
	)

	g.Eventually(func() int {
		return len(snapshots.List())
	}, 2*time.Second, 20*time.Millisecond).Should(gomega.Equal(1),
		"a snapshot must publish even when there are zero per-client clusters and zero referenced clusters")

	snap := snapshots.List()[0].snap
	g.Expect(snap.Resources[envoycachetypes.Cluster].Items).To(gomega.BeEmpty())
	g.Expect(snap.Resources[envoycachetypes.Listener].Items).To(gomega.HaveKey("listener"))
}
