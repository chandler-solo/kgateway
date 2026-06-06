package xdscheck

import (
	"context"
	"strings"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoyoauth2v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/oauth2/v3"
	envoyhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	proxyprotocolv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/proxy_protocol/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestCheckSnapshotValidLDSRDSCDSEDS(t *testing.T) {
	findings := CheckSnapshot(context.Background(), validSnapshot())
	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingRDSReferencedByListener(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes = nil

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingRouteConfiguration,
		Resource: "Listener/listener FilterChain/http",
		Message:  `listener "listener" filter chain "http" references missing RDS route configuration "routes"`,
	})
}

func TestCheckSnapshotMissingCDSClusterReferencedByRoute(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters = nil

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "RouteConfiguration/routes VirtualHost/vhost Route/to-cluster",
		Message:  `route configuration "routes" virtual host "vhost" route "to-cluster" references missing cluster "cluster"`,
	})
}

func TestCheckSnapshotMissingEDSAssignmentReferencedByEDSCluster(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Endpoints = nil

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingClusterLoadAssignment,
		Resource: "Cluster/cluster",
		Message:  `cluster "cluster" uses EDS resource "cluster" but no matching ClusterLoadAssignment was emitted`,
	})
}

func TestCheckSnapshotEDSAssignmentUsesServiceNameWhenSet(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters[0].EdsClusterConfig.ServiceName = "backend-service"
	snapshot.Endpoints[0].ClusterName = "backend-service"

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingEDSAssignmentUsesServiceNameInFinding(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters[0].EdsClusterConfig.ServiceName = "backend-service"

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingClusterLoadAssignment,
		Resource: "Cluster/cluster",
		Message:  `cluster "cluster" uses EDS resource "backend-service" but no matching ClusterLoadAssignment was emitted`,
	})
}

func TestCheckSnapshotDuplicateResourceNames(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeDuplicateResourceName,
		Resource: "Cluster/cluster",
		Message:  `duplicate Cluster resource name "cluster" at indexes 0 and 1`,
	})
}

func TestCheckSnapshotWeightedClusterWithMissingCluster(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes[0].VirtualHosts[0].Routes[0] = routeWithAction("weighted", &envoyroutev3.RouteAction{
		ClusterSpecifier: &envoyroutev3.RouteAction_WeightedClusters{
			WeightedClusters: &envoyroutev3.WeightedCluster{
				Clusters: []*envoyroutev3.WeightedCluster_ClusterWeight{
					{Name: "cluster", Weight: wrapperspb.UInt32(90)},
					{Name: "missing", Weight: wrapperspb.UInt32(10)},
				},
			},
		},
	})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "RouteConfiguration/routes VirtualHost/vhost Route/weighted WeightedCluster/1",
		Message:  `route configuration "routes" virtual host "vhost" route "weighted" references missing cluster "missing"`,
	})
}

func TestCheckSnapshotClusterHeaderRouteIsWarningOnly(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes[0].VirtualHosts[0].Routes[0] = routeWithAction("dynamic-cluster", &envoyroutev3.RouteAction{
		ClusterSpecifier: &envoyroutev3.RouteAction_ClusterHeader{
			ClusterHeader: "x-envoy-cluster",
		},
	})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(ErrorFindings(findings)) != 0 {
		t.Fatalf("expected cluster_header to produce no error findings, got %#v", findings)
	}
	requireFinding(t, findings, Finding{
		Severity: SeverityWarning,
		Code:     CodeUnsupportedRouteClusterHeader,
		Resource: "RouteConfiguration/routes VirtualHost/vhost Route/dynamic-cluster",
		Message:  `route configuration "routes" virtual host "vhost" route "dynamic-cluster" uses cluster_header "x-envoy-cluster"; static cluster existence cannot be verified`,
	})
}

func TestCheckSnapshotUnknownHCMTypedConfigDoesNotPanic(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Listeners[0].FilterChains[0].Filters[0].ConfigType = &envoylistenerv3.Filter_TypedConfig{
		TypedConfig: &anypb.Any{
			TypeUrl: "type.googleapis.com/example.UnknownHCM",
			Value:   []byte{1, 2, 3},
		},
	}

	var findings []Finding
	didPanic := true
	func() {
		defer func() {
			didPanic = recover() != nil
		}()
		findings = CheckSnapshot(context.Background(), snapshot)
	}()
	if didPanic {
		t.Fatal("CheckSnapshot panicked on unknown HCM typed_config")
	}
	requireFindingContaining(t, findings, SeverityWarning, CodeUnsupportedHCMTypedConfig, "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager", "example.UnknownHCM")
}

func TestCheckSnapshotInlineRouteConfigurationIsChecked(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithInlineRouteConfig(&envoyroutev3.RouteConfiguration{
		Name: "inline-routes",
		VirtualHosts: []*envoyroutev3.VirtualHost{{
			Name:    "inline-vhost",
			Domains: []string{"*"},
			Routes: []*envoyroutev3.Route{
				routeWithAction("inline-to-missing", &envoyroutev3.RouteAction{
					ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "missing"},
				}),
			},
		}},
	}))
	snapshot.Routes = nil

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http InlineRouteConfiguration VirtualHost/inline-vhost Route/inline-to-missing",
		Message:  `route configuration "inline-routes" virtual host "inline-vhost" route "inline-to-missing" references missing cluster "missing"`,
	})
}

func TestCheckSnapshotValidDownstreamSDSSecretReference(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Listeners[0].FilterChains[0].TransportSocket = downstreamTLSTransportSocket("server-cert")
	snapshot.Secrets = []*envoytlsv3.Secret{{Name: "server-cert"}}

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingDownstreamSDSSecret(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Listeners[0].FilterChains[0].TransportSocket = downstreamTLSTransportSocket("server-cert")

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http TransportSocket",
		Message:  `tls_certificate_sds_secret_configs[0] references missing SDS secret "server-cert"`,
	})
}

func TestCheckSnapshotMissingUpstreamValidationContextSDSSecret(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters[0].TransportSocket = upstreamValidationContextTransportSocket("backend-ca")

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Cluster/cluster TransportSocket",
		Message:  `combined_validation_context.validation_context_sds_secret_config references missing SDS secret "backend-ca"`,
	})
}

func TestCheckSnapshotMissingTransportSocketMatchSDSSecret(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters[0].TransportSocketMatches = []*envoyclusterv3.Cluster_TransportSocketMatch{{
		Name:            "tls-match",
		TransportSocket: upstreamValidationContextTransportSocket("backend-ca"),
	}}

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Cluster/cluster TransportSocketMatch/tls-match TransportSocket",
		Message:  `combined_validation_context.validation_context_sds_secret_config references missing SDS secret "backend-ca"`,
	})
}

func TestCheckSnapshotMissingNestedProxyProtocolSDSSecret(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters[0].TransportSocket = upstreamProxyProtocolTransportSocket(
		upstreamValidationContextTransportSocket("backend-ca"),
	)

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Cluster/cluster TransportSocket InnerTransportSocket",
		Message:  `combined_validation_context.validation_context_sds_secret_config references missing SDS secret "backend-ca"`,
	})
}

func TestCheckSnapshotValidOAuth2HTTPFilterSDSSecretReferences(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		oauth2HTTPFilter("envoy.filters.http.oauth2/default/provider", "oauth-token", "oauth-hmac"),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{
		{Name: "oauth-token"},
		{Name: "oauth-hmac"},
	}

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingOAuth2HTTPFilterTokenSecret(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		oauth2HTTPFilter("envoy.filters.http.oauth2/default/provider", "oauth-token", "oauth-hmac"),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{{Name: "oauth-hmac"}}

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.oauth2/default/provider",
		Message:  `config.credentials.token_secret references missing SDS secret "oauth-token"`,
	})
}

func TestCheckSnapshotMissingOAuth2HTTPFilterHMACSecret(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		oauth2HTTPFilter("envoy.filters.http.oauth2/default/provider", "oauth-token", "oauth-hmac"),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{{Name: "oauth-token"}}

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.oauth2/default/provider",
		Message:  `config.credentials.hmac_secret references missing SDS secret "oauth-hmac"`,
	})
}

func TestCheckSnapshotUnknownOAuth2HTTPFilterTypedConfigDoesNotPanic(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(&envoyhcmv3.HttpFilter{
		Name: "envoy.filters.http.oauth2/default/provider",
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: &anypb.Any{
				TypeUrl: "type.googleapis.com/example.NotOAuth2",
				Value:   []byte{1, 2, 3},
			},
		},
	}))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityWarning,
		Code:     CodeUnsupportedHTTPFilterTypedConfig,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.oauth2/default/provider",
		Message:  `OAuth2 HTTP filter has typed_config "type.googleapis.com/example.NotOAuth2"; SDS references were not validated`,
	})
}

func validSnapshot() Snapshot {
	return Snapshot{
		Listeners: []*envoylistenerv3.Listener{{
			Name: "listener",
			FilterChains: []*envoylistenerv3.FilterChain{{
				Name: "http",
				Filters: []*envoylistenerv3.Filter{{
					Name: envoywellknown.HTTPConnectionManager,
					ConfigType: &envoylistenerv3.Filter_TypedConfig{
						TypedConfig: mustAny(hcmWithHTTPFilters()),
					},
				}},
			}},
		}},
		Routes: []*envoyroutev3.RouteConfiguration{{
			Name: "routes",
			VirtualHosts: []*envoyroutev3.VirtualHost{{
				Name:    "vhost",
				Domains: []string{"*"},
				Routes: []*envoyroutev3.Route{
					routeWithAction("to-cluster", &envoyroutev3.RouteAction{
						ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{
							Cluster: "cluster",
						},
					}),
				},
			}},
		}},
		Clusters: []*envoyclusterv3.Cluster{{
			Name: "cluster",
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
				Type: envoyclusterv3.Cluster_EDS,
			},
			EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{},
		}},
		Endpoints: []*envoyendpointv3.ClusterLoadAssignment{{
			ClusterName: "cluster",
		}},
	}
}

func setHCM(snapshot *Snapshot, hcm *envoyhcmv3.HttpConnectionManager) {
	snapshot.Listeners[0].FilterChains[0].Filters[0].ConfigType = &envoylistenerv3.Filter_TypedConfig{
		TypedConfig: mustAny(hcm),
	}
}

func hcmWithHTTPFilters(httpFilters ...*envoyhcmv3.HttpFilter) *envoyhcmv3.HttpConnectionManager {
	return &envoyhcmv3.HttpConnectionManager{
		StatPrefix:  "http",
		HttpFilters: httpFilters,
		RouteSpecifier: &envoyhcmv3.HttpConnectionManager_Rds{
			Rds: &envoyhcmv3.Rds{
				ConfigSource: &envoycorev3.ConfigSource{
					ResourceApiVersion: envoycorev3.ApiVersion_V3,
					ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{
						Ads: &envoycorev3.AggregatedConfigSource{},
					},
				},
				RouteConfigName: "routes",
			},
		},
	}
}

func hcmWithInlineRouteConfig(routeConfig *envoyroutev3.RouteConfiguration) *envoyhcmv3.HttpConnectionManager {
	return &envoyhcmv3.HttpConnectionManager{
		StatPrefix: "http",
		RouteSpecifier: &envoyhcmv3.HttpConnectionManager_RouteConfig{
			RouteConfig: routeConfig,
		},
	}
}

func routeWithAction(name string, action *envoyroutev3.RouteAction) *envoyroutev3.Route {
	return &envoyroutev3.Route{
		Name: name,
		Match: &envoyroutev3.RouteMatch{
			PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"},
		},
		Action: &envoyroutev3.Route_Route{Route: action},
	}
}

func oauth2HTTPFilter(name, tokenSecret, hmacSecret string) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoyoauth2v3.OAuth2{
				Config: &envoyoauth2v3.OAuth2Config{
					Credentials: &envoyoauth2v3.OAuth2Credentials{
						ClientId:    "client-id",
						TokenSecret: &envoytlsv3.SdsSecretConfig{Name: tokenSecret},
						TokenFormation: &envoyoauth2v3.OAuth2Credentials_HmacSecret{
							HmacSecret: &envoytlsv3.SdsSecretConfig{Name: hmacSecret},
						},
					},
				},
			}),
		},
	}
}

func downstreamTLSTransportSocket(secretName string) *envoycorev3.TransportSocket {
	return &envoycorev3.TransportSocket{
		Name: envoywellknown.TransportSocketTls,
		ConfigType: &envoycorev3.TransportSocket_TypedConfig{
			TypedConfig: mustAny(&envoytlsv3.DownstreamTlsContext{
				CommonTlsContext: &envoytlsv3.CommonTlsContext{
					TlsCertificateSdsSecretConfigs: []*envoytlsv3.SdsSecretConfig{{Name: secretName}},
				},
			}),
		},
	}
}

func upstreamValidationContextTransportSocket(secretName string) *envoycorev3.TransportSocket {
	return &envoycorev3.TransportSocket{
		Name: envoywellknown.TransportSocketTls,
		ConfigType: &envoycorev3.TransportSocket_TypedConfig{
			TypedConfig: mustAny(&envoytlsv3.UpstreamTlsContext{
				CommonTlsContext: &envoytlsv3.CommonTlsContext{
					ValidationContextType: &envoytlsv3.CommonTlsContext_CombinedValidationContext{
						CombinedValidationContext: &envoytlsv3.CommonTlsContext_CombinedCertificateValidationContext{
							ValidationContextSdsSecretConfig: &envoytlsv3.SdsSecretConfig{Name: secretName},
						},
					},
				},
			}),
		},
	}
}

func upstreamProxyProtocolTransportSocket(inner *envoycorev3.TransportSocket) *envoycorev3.TransportSocket {
	return &envoycorev3.TransportSocket{
		Name: "envoy.transport_sockets.upstream_proxy_protocol",
		ConfigType: &envoycorev3.TransportSocket_TypedConfig{
			TypedConfig: mustAny(&proxyprotocolv3.ProxyProtocolUpstreamTransport{
				TransportSocket: inner,
			}),
		},
	}
}

func mustAny(msg proto.Message) *anypb.Any {
	out, err := anypb.New(msg)
	if err != nil {
		panic(err)
	}
	return out
}

func requireFinding(t *testing.T, findings []Finding, want Finding) {
	t.Helper()
	for _, got := range findings {
		if got == want {
			return
		}
	}
	t.Fatalf("missing finding\nwant: %#v\ngot:  %#v", want, findings)
}

func requireFindingContaining(t *testing.T, findings []Finding, severity, code, resource, messagePart string) {
	t.Helper()
	for _, got := range findings {
		if got.Severity == severity && got.Code == code && got.Resource == resource && strings.Contains(got.Message, messagePart) {
			return
		}
	}
	t.Fatalf("missing finding containing %q\nseverity=%s code=%s resource=%s\ngot: %#v", messagePart, severity, code, resource, findings)
}
