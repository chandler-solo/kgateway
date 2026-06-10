package proxy_syncer

import (
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/onsi/gomega"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

func TestMissingSnapshotClusterReferencesReportsMissingRouteCluster(t *testing.T) {
	g := gomega.NewWithT(t)
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Route] = envoycache.NewResourcesWithTTL("routes", []envoycachetypes.ResourceWithTTL{
		{Resource: routeConfigWithCluster("missing-cluster")},
	})

	// Cluster closure is intentionally NOT a hard validation failure: it is a
	// dataflow transient whose publish policy (warm-up deferral, then degraded
	// publish) lives in syncXds, driven by the per-gateway precomputed
	// reference set. The reference must still be detected, and errored
	// clusters must stay exempt.
	g.Expect(validateXDSSnapshotReferences(snap)).To(gomega.Succeed())
	referenced := collectReferencedClusters(snap.Resources[envoycachetypes.Route], snap.Resources[envoycachetypes.Listener])
	clusters := snap.Resources[envoycachetypes.Cluster].Items
	g.Expect(findMissingReferencedClusters(referenced, clusters, nil)).To(gomega.Equal([]string{"missing-cluster"}))
	g.Expect(findMissingReferencedClusters(referenced, clusters, []string{"missing-cluster"})).To(gomega.BeEmpty())
}

func TestValidateXDSSnapshotReferencesRejectsMissingADSSecret(t *testing.T) {
	g := gomega.NewWithT(t)
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = envoycache.NewResourcesWithTTL("clusters", []envoycachetypes.ResourceWithTTL{
		{Resource: clusterWithSDSSecret("cluster", "missing-secret", true)},
	})

	err := validateXDSSnapshotReferences(snap)

	g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("missing-secret")))
}

func TestValidateXDSSnapshotReferencesIgnoresExternalSDSSecret(t *testing.T) {
	g := gomega.NewWithT(t)
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = envoycache.NewResourcesWithTTL("clusters", []envoycachetypes.ResourceWithTTL{
		{Resource: clusterWithSDSSecret("cluster", "external-secret", false)},
	})

	err := validateXDSSnapshotReferences(snap)

	g.Expect(err).NotTo(gomega.HaveOccurred())
}

func TestValidateListenerFilterChainMatchesRejectsDuplicateCatchAllMatches(t *testing.T) {
	g := gomega.NewWithT(t)
	listener := &envoylistenerv3.Listener{
		Name: "listener",
		FilterChains: []*envoylistenerv3.FilterChain{
			{Name: "first"},
			{Name: "second"},
		},
	}

	err := validateListenerFilterChainMatches(listener)

	g.Expect(err).To(gomega.MatchError(gomega.ContainSubstring("duplicate filter chain match")))
}

func TestValidateListenerFilterChainMatchesAllowsDistinctMatches(t *testing.T) {
	g := gomega.NewWithT(t)
	listener := &envoylistenerv3.Listener{
		Name: "listener",
		FilterChains: []*envoylistenerv3.FilterChain{
			{
				Name: "first",
				FilterChainMatch: &envoylistenerv3.FilterChainMatch{
					ServerNames: []string{"one.example.com"},
				},
			},
			{
				Name: "second",
				FilterChainMatch: &envoylistenerv3.FilterChainMatch{
					ServerNames: []string{"two.example.com"},
				},
			},
		},
	}

	err := validateListenerFilterChainMatches(listener)

	g.Expect(err).NotTo(gomega.HaveOccurred())
}

func routeConfigWithCluster(cluster string) *envoyroutev3.RouteConfiguration {
	return &envoyroutev3.RouteConfiguration{
		Name: "route-config",
		VirtualHosts: []*envoyroutev3.VirtualHost{
			{
				Name:    "vhost",
				Domains: []string{"*"},
				Routes: []*envoyroutev3.Route{
					{
						Match: &envoyroutev3.RouteMatch{
							PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"},
						},
						Action: &envoyroutev3.Route_Route{
							Route: &envoyroutev3.RouteAction{
								ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: cluster},
							},
						},
					},
				},
			},
		},
	}
}

func clusterWithSDSSecret(cluster, secret string, ads bool) *envoyclusterv3.Cluster {
	tlsContextAny, err := utils.MessageToAny(&envoytlsv3.UpstreamTlsContext{
		CommonTlsContext: &envoytlsv3.CommonTlsContext{
			TlsCertificateSdsSecretConfigs: []*envoytlsv3.SdsSecretConfig{
				{
					Name:      secret,
					SdsConfig: sdsConfigSource(ads),
				},
			},
		},
	})
	if err != nil {
		panic(err)
	}

	return &envoyclusterv3.Cluster{
		Name: cluster,
		TransportSocket: &envoycorev3.TransportSocket{
			Name: "envoy.transport_sockets.tls",
			ConfigType: &envoycorev3.TransportSocket_TypedConfig{
				TypedConfig: tlsContextAny,
			},
		},
	}
}

func sdsConfigSource(ads bool) *envoycorev3.ConfigSource {
	if ads {
		return &envoycorev3.ConfigSource{
			ResourceApiVersion: envoycorev3.ApiVersion_V3,
			ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{
				Ads: &envoycorev3.AggregatedConfigSource{},
			},
		}
	}

	return &envoycorev3.ConfigSource{
		ResourceApiVersion: envoycorev3.ApiVersion_V3,
		ConfigSourceSpecifier: &envoycorev3.ConfigSource_ApiConfigSource{
			ApiConfigSource: &envoycorev3.ApiConfigSource{},
		},
	}
}
