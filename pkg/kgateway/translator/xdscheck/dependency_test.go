package xdscheck

import (
	"context"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoyhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"

	kgatewaywellknown "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

// A second, independent listener -> RDS -> EDS cluster -> CLA chain.
func secondChain(snapshot *Snapshot) {
	hcm := &envoyhcmv3.HttpConnectionManager{
		StatPrefix: "second",
		RouteSpecifier: &envoyhcmv3.HttpConnectionManager_Rds{Rds: &envoyhcmv3.Rds{
			RouteConfigName: "routes-2",
			ConfigSource:    &envoycorev3.ConfigSource{ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{Ads: &envoycorev3.AggregatedConfigSource{}}},
		}},
	}
	snapshot.Listeners = append(snapshot.Listeners, &envoylistenerv3.Listener{
		Name: "listener-2",
		FilterChains: []*envoylistenerv3.FilterChain{{
			Name: "http",
			Filters: []*envoylistenerv3.Filter{{
				Name:       envoywellknown.HTTPConnectionManager,
				ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: mustAny(hcm)},
			}},
		}},
	})
	snapshot.Routes = append(snapshot.Routes, &envoyroutev3.RouteConfiguration{
		Name: "routes-2",
		VirtualHosts: []*envoyroutev3.VirtualHost{{
			Name: "vhost-2", Domains: []string{"*"},
			Routes: []*envoyroutev3.Route{routeWithAction("to-cluster-2", &envoyroutev3.RouteAction{
				ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "cluster-2"},
			})},
		}},
	})
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{
		Name:                 "cluster-2",
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
		EdsClusterConfig:     &envoyclusterv3.Cluster_EdsClusterConfig{},
	})
	snapshot.Endpoints = append(snapshot.Endpoints, &envoyendpointv3.ClusterLoadAssignment{ClusterName: "cluster-2"})
}

// RF-002/RF-021: two chains that share nothing are two publication units.
func TestDependencyGraphSeparatesIndependentChains(t *testing.T) {
	snapshot := validSnapshot()
	secondChain(&snapshot)

	graph, findings := DependencyGraphOf(context.Background(), snapshot)
	require.Empty(t, ErrorFindings(findings))
	components := graph.Components()
	require.Len(t, components, 2)
	require.Equal(t, []string{"Cluster/cluster", "ClusterLoadAssignment/cluster", "Listener/listener", "RouteConfiguration/routes"}, components[0].Nodes)
	require.Equal(t, []string{"Cluster/cluster-2", "ClusterLoadAssignment/cluster-2", "Listener/listener-2", "RouteConfiguration/routes-2"}, components[1].Nodes)
	for _, component := range components {
		require.False(t, component.Opaque)
		require.Empty(t, component.Dangling)
	}
}

// A secret used by both listeners joins the chains: rotating it cannot be
// isolated from either, and holding one chain holds the secret for both.
func TestDependencyGraphSharedSecretJoinsChains(t *testing.T) {
	snapshot := validSnapshot()
	secondChain(&snapshot)
	snapshot.Listeners[0].FilterChains[0].TransportSocket = downstreamTLSTransportSocket("shared-cert")
	snapshot.Listeners[1].FilterChains[0].TransportSocket = downstreamTLSTransportSocket("shared-cert")
	snapshot.Secrets = []*envoytlsv3.Secret{{Name: "shared-cert"}}

	graph, findings := DependencyGraphOf(context.Background(), snapshot)
	require.Empty(t, ErrorFindings(findings))
	components := graph.Components()
	require.Len(t, components, 1)
	require.Contains(t, components[0].Nodes, "Secret/shared-cert")
	require.Len(t, components[0].Nodes, 9)
}

// A dangling reference is part of its component's connectivity and reported.
func TestDependencyGraphReportsDanglingReferences(t *testing.T) {
	snapshot := validSnapshot()
	secondChain(&snapshot)
	snapshot.Routes[1].VirtualHosts[0].Routes = append(snapshot.Routes[1].VirtualHosts[0].Routes,
		routeWithAction("to-ghost", &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "ghost"}}))

	graph, findings := DependencyGraphOf(context.Background(), snapshot)
	require.NotEmpty(t, ErrorFindings(findings))
	components := graph.Components()
	require.Len(t, components, 2)
	require.Empty(t, components[0].Dangling)
	require.Equal(t, []string{"Cluster/ghost"}, components[1].Dangling)
}

// A typed config the checker cannot unpack makes its resource opaque: the
// component may reference anything and is not safe to publish independently.
func TestDependencyGraphMarksUnknownTypedConfigOpaque(t *testing.T) {
	snapshot := validSnapshot()
	secondChain(&snapshot)
	snapshot.Listeners[1].FilterChains[0].Filters[0].ConfigType = &envoylistenerv3.Filter_TypedConfig{
		TypedConfig: &anypb.Any{TypeUrl: "type.googleapis.com/example.UnknownHCM", Value: []byte{1, 2, 3}},
	}

	graph, _ := DependencyGraphOf(context.Background(), snapshot)
	require.Equal(t, CodeUnsupportedHCMTypedConfig, graph.Opaque["Listener/listener-2"])
	components := graph.Components()
	var opaque, transparent int
	for _, component := range components {
		if component.Opaque {
			opaque++
		} else {
			transparent++
		}
	}
	require.Equal(t, 1, opaque, "the listener with an unreadable HCM must be opaque")
	require.GreaterOrEqual(t, transparent, 1, "the intact chain stays a transparent component")
}

// The blackhole sentinel is not a node: routing to it does not connect chains.
func TestDependencyGraphExemptsBlackholeCluster(t *testing.T) {
	snapshot := validSnapshot()
	secondChain(&snapshot)
	for _, routes := range snapshot.Routes {
		routes.VirtualHosts[0].Routes = append(routes.VirtualHosts[0].Routes,
			routeWithAction("to-blackhole", &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: kgatewaywellknown.BlackholeClusterName}}))
	}

	graph, findings := DependencyGraphOf(context.Background(), snapshot)
	require.Empty(t, ErrorFindings(findings))
	require.Len(t, graph.Components(), 2)
}
