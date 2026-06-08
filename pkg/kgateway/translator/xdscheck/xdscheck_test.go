package xdscheck

import (
	"context"
	"strings"
	"testing"

	xdscorev3 "github.com/cncf/xds/go/xds/core/v3"
	xdsmatcherv3 "github.com/cncf/xds/go/xds/type/matcher/v3"
	envoyaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyratelimitconfigv3 "github.com/envoyproxy/go-control-plane/envoy/config/ratelimit/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytracev3 "github.com/envoyproxy/go-control-plane/envoy/config/trace/v3"
	envoyfileaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/file/v3"
	envoygrpcaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/grpc/v3"
	envoyotelaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/access_loggers/open_telemetry/v3"
	envoymatchingv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/common/matching/v3"
	envoycompositev3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/composite/v3"
	envoycredentialinjectorv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/credential_injector/v3"
	envoyextauthzv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_authz/v3"
	envoyextprocv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_proc/v3"
	envoyjwtauthnv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/jwt_authn/v3"
	envoyoauth2v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/oauth2/v3"
	envoyratelimitv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ratelimit/v3"
	envoyhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	envoygenericsecretformatterv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/formatter/generic_secret/v3"
	envoymetadataformatterv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/formatter/metadata/v3"
	envoyreqwithoutqueryformatterv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/formatter/req_without_query/v3"
	envoygenericcredentialv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/http/injected_credentials/generic/v3"
	envoyoauth2credentialv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/http/injected_credentials/oauth2/v3"
	proxyprotocolv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/proxy_protocol/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	kgatewaywellknown "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
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

func TestCheckSnapshotOrphanClusterLoadAssignment(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Endpoints = append(snapshot.Endpoints, &envoyendpointv3.ClusterLoadAssignment{
		ClusterName: "stale-cluster",
	})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeOrphanClusterLoadAssignment,
		Resource: "ClusterLoadAssignment/stale-cluster",
		Message:  `ClusterLoadAssignment "stale-cluster" has no matching EDS cluster; ADS named EDS snapshots should not include endpoint resources Envoy will not request`,
	})
}

func TestCheckSnapshotStaticClusterLoadAssignmentIsOrphan(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{
		Name: "static-cluster",
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
			Type: envoyclusterv3.Cluster_STATIC,
		},
	})
	snapshot.Endpoints = append(snapshot.Endpoints, &envoyendpointv3.ClusterLoadAssignment{
		ClusterName: "static-cluster",
	})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeOrphanClusterLoadAssignment,
		Resource: "ClusterLoadAssignment/static-cluster",
		Message:  `ClusterLoadAssignment "static-cluster" has no matching EDS cluster; ADS named EDS snapshots should not include endpoint resources Envoy will not request`,
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

func TestCheckSnapshotBlackholeClusterRouteIsIntentionalSentinel(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes[0].VirtualHosts[0].Routes[0] = routeWithAction("unresolved-backend", &envoyroutev3.RouteAction{
		ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{
			Cluster: kgatewaywellknown.BlackholeClusterName,
		},
	})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected blackhole cluster sentinel to produce no findings, got %#v", findings)
	}
}

func TestCheckSnapshotWeightedBlackholeClusterEntryIsIntentionalSentinel(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes[0].VirtualHosts[0].Routes[0] = routeWithAction("weighted-with-unresolved-backend", &envoyroutev3.RouteAction{
		ClusterSpecifier: &envoyroutev3.RouteAction_WeightedClusters{
			WeightedClusters: &envoyroutev3.WeightedCluster{
				Clusters: []*envoyroutev3.WeightedCluster_ClusterWeight{
					{Name: "cluster", Weight: wrapperspb.UInt32(90)},
					{Name: kgatewaywellknown.BlackholeClusterName, Weight: wrapperspb.UInt32(10)},
				},
			},
		},
	})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected weighted blackhole cluster sentinel to produce no findings, got %#v", findings)
	}
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

func TestCheckSnapshotSystemCASecretReferenceIsSatisfiedByBootstrap(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Clusters[0].TransportSocket = upstreamValidationContextTransportSocket(systemCASecretName)

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected system CA SDS reference to produce no findings, got %#v", findings)
	}
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

func TestCheckSnapshotValidOAuth2HTTPFilterTokenEndpointClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		oauth2HTTPFilter("envoy.filters.http.oauth2/default/provider", "oauth-token", "oauth-hmac", "oauth-cluster"),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{
		{Name: "oauth-token"},
		{Name: "oauth-hmac"},
	}
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "oauth-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingOAuth2HTTPFilterTokenEndpointCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		oauth2HTTPFilter("envoy.filters.http.oauth2/default/provider", "oauth-token", "oauth-hmac", "oauth-cluster"),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{
		{Name: "oauth-token"},
		{Name: "oauth-hmac"},
	}

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.oauth2/default/provider",
		Message:  `config.token_endpoint.cluster references missing cluster "oauth-cluster"`,
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

func TestCheckSnapshotValidGenericInjectedCredentialSecretReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		credentialInjectorHTTPFilter("envoy.filters.http.credential_injector", "generic-credential", genericInjectedCredential("credential-secret")),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{{Name: "credential-secret"}}

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingGenericInjectedCredentialSecret(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		credentialInjectorHTTPFilter("envoy.filters.http.credential_injector", "generic-credential", genericInjectedCredential("credential-secret")),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.credential_injector Credential/generic-credential",
		Message:  `credential references missing SDS secret "credential-secret"`,
	})
}

func TestCheckSnapshotMissingOAuth2InjectedCredentialClientSecret(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		credentialInjectorHTTPFilter("envoy.filters.http.credential_injector", "oauth2-credential", oauth2InjectedCredential("oauth-client-secret", "oauth-token-cluster")),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "oauth-token-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.credential_injector Credential/oauth2-credential",
		Message:  `client_credentials.client_secret references missing SDS secret "oauth-client-secret"`,
	})
}

func TestCheckSnapshotMissingOAuth2InjectedCredentialTokenEndpointCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		credentialInjectorHTTPFilter("envoy.filters.http.credential_injector", "oauth2-credential", oauth2InjectedCredential("oauth-client-secret", "oauth-token-cluster")),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{{Name: "oauth-client-secret"}}

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.credential_injector Credential/oauth2-credential",
		Message:  `token_endpoint.cluster references missing cluster "oauth-token-cluster"`,
	})
}

func TestCheckSnapshotValidJWTAuthnRemoteJWKSClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		jwtAuthnHTTPFilter("envoy.filters.http.jwt_authn", "issuer", "jwks-cluster"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "jwks-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingJWTAuthnRemoteJWKSCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		jwtAuthnHTTPFilter("envoy.filters.http.jwt_authn", "issuer", "jwks-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.jwt_authn",
		Message:  `providers[issuer].remote_jwks.http_uri.cluster references missing cluster "jwks-cluster"`,
	})
}

func TestCheckSnapshotValidExtAuthzHTTPServiceClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		extAuthzHTTPServiceFilter("envoy.filters.http.ext_authz", "authz-cluster"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "authz-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingExtAuthzHTTPServiceCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		extAuthzHTTPServiceFilter("envoy.filters.http.ext_authz", "authz-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.ext_authz",
		Message:  `http_service.server_uri.cluster references missing cluster "authz-cluster"`,
	})
}

func TestCheckSnapshotValidExtAuthzEnvoyGRPCServiceClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		extAuthzGRPCServiceFilter("envoy.filters.http.ext_authz", "authz-cluster"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "authz-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingExtAuthzEnvoyGRPCServiceCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		extAuthzGRPCServiceFilter("envoy.filters.http.ext_authz", "authz-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.ext_authz",
		Message:  `grpc_service.envoy_grpc.cluster_name references missing cluster "authz-cluster"`,
	})
}

func TestCheckSnapshotValidExtProcEnvoyGRPCServiceClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		extProcHTTPFilter("envoy.filters.http.ext_proc", "extproc-cluster"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "extproc-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingExtProcEnvoyGRPCServiceCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		extProcHTTPFilter("envoy.filters.http.ext_proc", "extproc-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.ext_proc",
		Message:  `grpc_service.envoy_grpc.cluster_name references missing cluster "extproc-cluster"`,
	})
}

func TestCheckSnapshotMissingCompositeExtProcEnvoyGRPCServiceCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		compositeExtProcHTTPFilter("ext_proc/default/extproc", "extproc-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/ext_proc/default/extproc XdsMatcher MatcherList/0 Action/composite-action TypedConfig/envoy.filters.http.ext_proc",
		Message:  `grpc_service.envoy_grpc.cluster_name references missing cluster "extproc-cluster"`,
	})
}

func TestCheckSnapshotValidRateLimitEnvoyGRPCServiceClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		rateLimitHTTPFilter("envoy.filters.http.ratelimit", "ratelimit-cluster"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "ratelimit-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingRateLimitEnvoyGRPCServiceCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithHTTPFilters(
		rateLimitHTTPFilter("envoy.filters.http.ratelimit", "ratelimit-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager HttpFilter/envoy.filters.http.ratelimit",
		Message:  `rate_limit_service.grpc_service.envoy_grpc.cluster_name references missing cluster "ratelimit-cluster"`,
	})
}

func TestCheckSnapshotValidHTTPGRPCAccessLogClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		httpGRPCAccessLog("envoy.access_loggers.http_grpc", "access-log-cluster"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "access-log-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingHTTPGRPCAccessLogCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		httpGRPCAccessLog("envoy.access_loggers.http_grpc", "access-log-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager AccessLog/0/envoy.access_loggers.http_grpc",
		Message:  `common_config.grpc_service.envoy_grpc.cluster_name references missing cluster "access-log-cluster"`,
	})
}

func TestCheckSnapshotMissingOpenTelemetryAccessLogGRPCCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		openTelemetryGRPCAccessLog("envoy.access_loggers.open_telemetry", "otel-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager AccessLog/0/envoy.access_loggers.open_telemetry",
		Message:  `grpc_service.envoy_grpc.cluster_name references missing cluster "otel-cluster"`,
	})
}

func TestCheckSnapshotMissingOpenTelemetryAccessLogHTTPCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		openTelemetryHTTPAccessLog("envoy.access_loggers.open_telemetry", "otel-http-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager AccessLog/0/envoy.access_loggers.open_telemetry",
		Message:  `http_service.http_uri.cluster references missing cluster "otel-http-cluster"`,
	})
}

func TestCheckSnapshotValidFileAccessLogGenericSecretFormatter(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		fileAccessLogWithGenericSecretFormatter("envoy.access_loggers.file", "api-token", "api-token-secret"),
	))
	snapshot.Secrets = []*envoytlsv3.Secret{{Name: "api-token-secret"}}

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingFileAccessLogGenericSecretFormatterSecret(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		fileAccessLogWithGenericSecretFormatter("envoy.access_loggers.file", "api-token", "api-token-secret"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager AccessLog/0/envoy.access_loggers.file LogFormat Formatter/0/envoy.formatter.generic_secret",
		Message:  `secret_configs[api-token] references missing SDS secret "api-token-secret"`,
	})
}

func TestCheckSnapshotRecognizedNoReferenceFormatters(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		fileAccessLogWithFormatters(
			"envoy.access_loggers.file",
			formatter("envoy.formatter.req_without_query", &envoyreqwithoutqueryformatterv3.ReqWithoutQuery{}),
			formatter("envoy.formatter.metadata", &envoymetadataformatterv3.Metadata{}),
		),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected recognized formatters to produce no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingOpenTelemetryAccessLogHTTPServiceGenericSecretFormatterSecret(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithAccessLogs(
		openTelemetryHTTPAccessLogWithFormatter("envoy.access_loggers.open_telemetry", "otel-http-cluster", "api-token", "api-token-secret"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "otel-http-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager AccessLog/0/envoy.access_loggers.open_telemetry http_service Formatter/0/envoy.formatter.generic_secret",
		Message:  `secret_configs[api-token] references missing SDS secret "api-token-secret"`,
	})
}

func TestCheckSnapshotValidOpenTelemetryTracingGRPCClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithTracing(
		openTelemetryGRPCTracingProvider("envoy.tracers.opentelemetry", "otel-cluster"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "otel-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingOpenTelemetryTracingGRPCCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithTracing(
		openTelemetryGRPCTracingProvider("envoy.tracers.opentelemetry", "otel-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager Tracing/envoy.tracers.opentelemetry",
		Message:  `grpc_service.envoy_grpc.cluster_name references missing cluster "otel-cluster"`,
	})
}

func TestCheckSnapshotMissingOpenTelemetryTracingHTTPCluster(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithTracing(
		openTelemetryHTTPTracingProvider("envoy.tracers.opentelemetry", "otel-http-cluster"),
	))

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager Tracing/envoy.tracers.opentelemetry",
		Message:  `http_service.http_uri.cluster references missing cluster "otel-http-cluster"`,
	})
}

func TestCheckSnapshotMissingOpenTelemetryTracingHTTPServiceGenericSecretFormatterSecret(t *testing.T) {
	snapshot := validSnapshot()
	setHCM(&snapshot, hcmWithTracing(
		openTelemetryHTTPTracingProviderWithFormatter("envoy.tracers.opentelemetry", "otel-http-cluster", "api-token", "api-token-secret"),
	))
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "otel-http-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingSecret,
		Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager Tracing/envoy.tracers.opentelemetry http_service Formatter/0/envoy.formatter.generic_secret",
		Message:  `secret_configs[api-token] references missing SDS secret "api-token-secret"`,
	})
}

func TestCheckSnapshotMissingRecognizedTracingProviderClusters(t *testing.T) {
	tests := []struct {
		name        string
		provider    *envoytracev3.Tracing_Http
		message     string
		clusterName string
	}{
		{
			name: "Datadog",
			provider: tracingProvider("envoy.tracers.datadog", &envoytracev3.DatadogConfig{
				CollectorCluster: "datadog-cluster",
				ServiceName:      "kgateway",
			}),
			message:     `collector_cluster references missing cluster "datadog-cluster"`,
			clusterName: "datadog-cluster",
		},
		{
			name: "Lightstep",
			provider: tracingProvider("envoy.tracers.lightstep", &envoytracev3.LightstepConfig{
				CollectorCluster: "lightstep-cluster",
			}),
			message:     `collector_cluster references missing cluster "lightstep-cluster"`,
			clusterName: "lightstep-cluster",
		},
		{
			name: "SkyWalking",
			provider: tracingProvider("envoy.tracers.skywalking", &envoytracev3.SkyWalkingConfig{
				GrpcService: envoyGRPCService("skywalking-cluster"),
			}),
			message:     `grpc_service.envoy_grpc.cluster_name references missing cluster "skywalking-cluster"`,
			clusterName: "skywalking-cluster",
		},
		{
			name: "ZipkinLegacyCollectorCluster",
			provider: tracingProvider("envoy.tracers.zipkin", &envoytracev3.ZipkinConfig{
				CollectorCluster:  "zipkin-cluster",
				CollectorEndpoint: "/api/v2/spans",
			}),
			message:     `collector_cluster references missing cluster "zipkin-cluster"`,
			clusterName: "zipkin-cluster",
		},
		{
			name: "ZipkinCollectorService",
			provider: tracingProvider("envoy.tracers.zipkin", &envoytracev3.ZipkinConfig{
				CollectorService: httpService("zipkin-service-cluster", "https://zipkin.example.com/api/v2/spans"),
			}),
			message:     `collector_service.http_uri.cluster references missing cluster "zipkin-service-cluster"`,
			clusterName: "zipkin-service-cluster",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := validSnapshot()
			setHCM(&snapshot, hcmWithTracing(tt.provider))

			findings := CheckSnapshot(context.Background(), snapshot)

			requireFinding(t, findings, Finding{
				Severity: SeverityError,
				Code:     CodeMissingCluster,
				Resource: "Listener/listener FilterChain/http Filter/envoy.filters.network.http_connection_manager Tracing/" + tt.provider.GetName(),
				Message:  tt.message,
			})

			snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: tt.clusterName})
			findings = CheckSnapshot(context.Background(), snapshot)
			if len(findings) != 0 {
				t.Fatalf("expected no findings after adding %q, got %#v", tt.clusterName, findings)
			}
		})
	}
}

func TestCheckSnapshotValidVirtualHostExtProcPerRouteClusterReference(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes[0].VirtualHosts[0].TypedPerFilterConfig = map[string]*anypb.Any{
		"envoy.filters.http.ext_proc": extProcPerRouteTypedConfig("extproc-cluster"),
	}
	snapshot.Clusters = append(snapshot.Clusters, &envoyclusterv3.Cluster{Name: "extproc-cluster"})

	findings := CheckSnapshot(context.Background(), snapshot)

	if len(findings) != 0 {
		t.Fatalf("expected no findings, got %#v", findings)
	}
}

func TestCheckSnapshotMissingRouteExtProcPerRouteCluster(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes[0].VirtualHosts[0].Routes[0].TypedPerFilterConfig = map[string]*anypb.Any{
		"envoy.filters.http.ext_proc": extProcPerRouteTypedConfig("extproc-cluster"),
	}

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "RouteConfiguration/routes VirtualHost/vhost Route/to-cluster TypedPerFilterConfig/envoy.filters.http.ext_proc",
		Message:  `overrides.grpc_service.envoy_grpc.cluster_name references missing cluster "extproc-cluster"`,
	})
}

func TestCheckSnapshotMissingWrappedWeightedClusterExtProcPerRouteCluster(t *testing.T) {
	snapshot := validSnapshot()
	snapshot.Routes[0].VirtualHosts[0].Routes[0] = routeWithAction("weighted", &envoyroutev3.RouteAction{
		ClusterSpecifier: &envoyroutev3.RouteAction_WeightedClusters{
			WeightedClusters: &envoyroutev3.WeightedCluster{
				Clusters: []*envoyroutev3.WeightedCluster_ClusterWeight{{
					Name: "cluster",
					TypedPerFilterConfig: map[string]*anypb.Any{
						"envoy.filters.http.ext_proc": routeFilterConfig(extProcPerRouteTypedConfig("extproc-cluster")),
					},
				}},
			},
		},
	})

	findings := CheckSnapshot(context.Background(), snapshot)

	requireFinding(t, findings, Finding{
		Severity: SeverityError,
		Code:     CodeMissingCluster,
		Resource: "RouteConfiguration/routes VirtualHost/vhost Route/weighted WeightedCluster/0 TypedPerFilterConfig/envoy.filters.http.ext_proc Config",
		Message:  `overrides.grpc_service.envoy_grpc.cluster_name references missing cluster "extproc-cluster"`,
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

func hcmWithAccessLogs(accessLogs ...*envoyaccesslogv3.AccessLog) *envoyhcmv3.HttpConnectionManager {
	hcm := hcmWithHTTPFilters()
	hcm.AccessLog = accessLogs
	return hcm
}

func hcmWithTracing(provider *envoytracev3.Tracing_Http) *envoyhcmv3.HttpConnectionManager {
	hcm := hcmWithHTTPFilters()
	hcm.Tracing = &envoyhcmv3.HttpConnectionManager_Tracing{
		Provider: provider,
	}
	return hcm
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

func httpGRPCAccessLog(name, cluster string) *envoyaccesslogv3.AccessLog {
	return &envoyaccesslogv3.AccessLog{
		Name: name,
		ConfigType: &envoyaccesslogv3.AccessLog_TypedConfig{
			TypedConfig: mustAny(&envoygrpcaccesslogv3.HttpGrpcAccessLogConfig{
				CommonConfig: &envoygrpcaccesslogv3.CommonGrpcAccessLogConfig{
					GrpcService: envoyGRPCService(cluster),
				},
			}),
		},
	}
}

func openTelemetryGRPCAccessLog(name, cluster string) *envoyaccesslogv3.AccessLog {
	return &envoyaccesslogv3.AccessLog{
		Name: name,
		ConfigType: &envoyaccesslogv3.AccessLog_TypedConfig{
			TypedConfig: mustAny(&envoyotelaccesslogv3.OpenTelemetryAccessLogConfig{
				GrpcService: envoyGRPCService(cluster),
			}),
		},
	}
}

func openTelemetryHTTPAccessLog(name, cluster string) *envoyaccesslogv3.AccessLog {
	return &envoyaccesslogv3.AccessLog{
		Name: name,
		ConfigType: &envoyaccesslogv3.AccessLog_TypedConfig{
			TypedConfig: mustAny(&envoyotelaccesslogv3.OpenTelemetryAccessLogConfig{
				HttpService: &envoycorev3.HttpService{
					HttpUri: &envoycorev3.HttpUri{
						Uri: "https://otel.example.com/v1/logs",
						HttpUpstreamType: &envoycorev3.HttpUri_Cluster{
							Cluster: cluster,
						},
					},
				},
			}),
		},
	}
}

func fileAccessLogWithGenericSecretFormatter(name, lookupName, secretName string) *envoyaccesslogv3.AccessLog {
	return fileAccessLogWithFormatters(name, genericSecretFormatter(lookupName, secretName))
}

func fileAccessLogWithFormatters(name string, formatters ...*envoycorev3.TypedExtensionConfig) *envoyaccesslogv3.AccessLog {
	return &envoyaccesslogv3.AccessLog{
		Name: name,
		ConfigType: &envoyaccesslogv3.AccessLog_TypedConfig{
			TypedConfig: mustAny(&envoyfileaccesslogv3.FileAccessLog{
				Path: "/tmp/access.log",
				AccessLogFormat: &envoyfileaccesslogv3.FileAccessLog_LogFormat{
					LogFormat: &envoycorev3.SubstitutionFormatString{
						Format: &envoycorev3.SubstitutionFormatString_TextFormat{
							TextFormat: "%REQ(:PATH)%\n",
						},
						Formatters: formatters,
					},
				},
			}),
		},
	}
}

func formatter(name string, config proto.Message) *envoycorev3.TypedExtensionConfig {
	return &envoycorev3.TypedExtensionConfig{
		Name:        name,
		TypedConfig: mustAny(config),
	}
}

func openTelemetryHTTPAccessLogWithFormatter(name, cluster, lookupName, secretName string) *envoyaccesslogv3.AccessLog {
	service := httpService(cluster, "https://otel.example.com/v1/logs")
	service.Formatters = []*envoycorev3.TypedExtensionConfig{
		genericSecretFormatter(lookupName, secretName),
	}
	return &envoyaccesslogv3.AccessLog{
		Name: name,
		ConfigType: &envoyaccesslogv3.AccessLog_TypedConfig{
			TypedConfig: mustAny(&envoyotelaccesslogv3.OpenTelemetryAccessLogConfig{
				HttpService: service,
			}),
		},
	}
}

func genericSecretFormatter(lookupName, secretName string) *envoycorev3.TypedExtensionConfig {
	return &envoycorev3.TypedExtensionConfig{
		Name: "envoy.formatter.generic_secret",
		TypedConfig: mustAny(&envoygenericsecretformatterv3.GenericSecret{
			SecretConfigs: map[string]*envoytlsv3.SdsSecretConfig{
				lookupName: {Name: secretName},
			},
		}),
	}
}

func openTelemetryGRPCTracingProvider(name, cluster string) *envoytracev3.Tracing_Http {
	return &envoytracev3.Tracing_Http{
		Name: name,
		ConfigType: &envoytracev3.Tracing_Http_TypedConfig{
			TypedConfig: mustAny(&envoytracev3.OpenTelemetryConfig{
				GrpcService: envoyGRPCService(cluster),
			}),
		},
	}
}

func openTelemetryHTTPTracingProvider(name, cluster string) *envoytracev3.Tracing_Http {
	return tracingProvider(name, &envoytracev3.OpenTelemetryConfig{
		HttpService: httpService(cluster, "https://otel.example.com/v1/traces"),
	})
}

func openTelemetryHTTPTracingProviderWithFormatter(name, cluster, lookupName, secretName string) *envoytracev3.Tracing_Http {
	service := httpService(cluster, "https://otel.example.com/v1/traces")
	service.Formatters = []*envoycorev3.TypedExtensionConfig{
		genericSecretFormatter(lookupName, secretName),
	}
	return tracingProvider(name, &envoytracev3.OpenTelemetryConfig{
		HttpService: service,
	})
}

func tracingProvider(name string, config proto.Message) *envoytracev3.Tracing_Http {
	return &envoytracev3.Tracing_Http{
		Name: name,
		ConfigType: &envoytracev3.Tracing_Http_TypedConfig{
			TypedConfig: mustAny(config),
		},
	}
}

func oauth2HTTPFilter(name, tokenSecret, hmacSecret string, tokenEndpointClusters ...string) *envoyhcmv3.HttpFilter {
	config := &envoyoauth2v3.OAuth2Config{
		Credentials: &envoyoauth2v3.OAuth2Credentials{
			ClientId:    "client-id",
			TokenSecret: &envoytlsv3.SdsSecretConfig{Name: tokenSecret},
			TokenFormation: &envoyoauth2v3.OAuth2Credentials_HmacSecret{
				HmacSecret: &envoytlsv3.SdsSecretConfig{Name: hmacSecret},
			},
		},
	}
	if len(tokenEndpointClusters) > 0 {
		config.TokenEndpoint = &envoycorev3.HttpUri{
			Uri: "https://oauth.example.com/token",
			HttpUpstreamType: &envoycorev3.HttpUri_Cluster{
				Cluster: tokenEndpointClusters[0],
			},
		}
	}

	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoyoauth2v3.OAuth2{
				Config: config,
			}),
		},
	}
}

func credentialInjectorHTTPFilter(name, credentialName string, credential proto.Message) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoycredentialinjectorv3.CredentialInjector{
				Credential: &envoycorev3.TypedExtensionConfig{
					Name:        credentialName,
					TypedConfig: mustAny(credential),
				},
			}),
		},
	}
}

func genericInjectedCredential(secretName string) *envoygenericcredentialv3.Generic {
	return &envoygenericcredentialv3.Generic{
		Credential: &envoytlsv3.SdsSecretConfig{Name: secretName},
		Header:     "Authorization",
	}
}

func oauth2InjectedCredential(clientSecretName, tokenEndpointCluster string) *envoyoauth2credentialv3.OAuth2 {
	return &envoyoauth2credentialv3.OAuth2{
		TokenEndpoint: &envoycorev3.HttpUri{
			Uri: "https://oauth.example.com/token",
			HttpUpstreamType: &envoycorev3.HttpUri_Cluster{
				Cluster: tokenEndpointCluster,
			},
		},
		FlowType: &envoyoauth2credentialv3.OAuth2_ClientCredentials_{
			ClientCredentials: &envoyoauth2credentialv3.OAuth2_ClientCredentials{
				ClientId:     "client-id",
				ClientSecret: &envoytlsv3.SdsSecretConfig{Name: clientSecretName},
			},
		},
	}
}

func jwtAuthnHTTPFilter(name, providerName, jwksCluster string) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoyjwtauthnv3.JwtAuthentication{
				Providers: map[string]*envoyjwtauthnv3.JwtProvider{
					providerName: {
						JwksSourceSpecifier: &envoyjwtauthnv3.JwtProvider_RemoteJwks{
							RemoteJwks: &envoyjwtauthnv3.RemoteJwks{
								HttpUri: &envoycorev3.HttpUri{
									Uri: "https://issuer.example.com/.well-known/jwks.json",
									HttpUpstreamType: &envoycorev3.HttpUri_Cluster{
										Cluster: jwksCluster,
									},
								},
							},
						},
					},
				},
			}),
		},
	}
}

func extAuthzHTTPServiceFilter(name, cluster string) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoyextauthzv3.ExtAuthz{
				Services: &envoyextauthzv3.ExtAuthz_HttpService{
					HttpService: &envoyextauthzv3.HttpService{
						ServerUri: &envoycorev3.HttpUri{
							Uri: "https://authz.example.com/check",
							HttpUpstreamType: &envoycorev3.HttpUri_Cluster{
								Cluster: cluster,
							},
						},
					},
				},
			}),
		},
	}
}

func extAuthzGRPCServiceFilter(name, cluster string) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoyextauthzv3.ExtAuthz{
				Services: &envoyextauthzv3.ExtAuthz_GrpcService{
					GrpcService: envoyGRPCService(cluster),
				},
			}),
		},
	}
}

func extProcHTTPFilter(name, cluster string) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoyextprocv3.ExternalProcessor{
				GrpcService: envoyGRPCService(cluster),
			}),
		},
	}
}

func compositeExtProcHTTPFilter(name, cluster string) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoymatchingv3.ExtensionWithMatcher{
				ExtensionConfig: &envoycorev3.TypedExtensionConfig{
					Name:        "composite_ext_proc",
					TypedConfig: mustAny(&envoycompositev3.Composite{}),
				},
				XdsMatcher: &xdsmatcherv3.Matcher{
					MatcherType: &xdsmatcherv3.Matcher_MatcherList_{
						MatcherList: &xdsmatcherv3.Matcher_MatcherList{
							Matchers: []*xdsmatcherv3.Matcher_MatcherList_FieldMatcher{{
								OnMatch: &xdsmatcherv3.Matcher_OnMatch{
									OnMatch: &xdsmatcherv3.Matcher_OnMatch_Action{
										Action: &xdscorev3.TypedExtensionConfig{
											Name: "composite-action",
											TypedConfig: mustAny(&envoycompositev3.ExecuteFilterAction{
												TypedConfig: &envoycorev3.TypedExtensionConfig{
													Name: "envoy.filters.http.ext_proc",
													TypedConfig: mustAny(&envoyextprocv3.ExternalProcessor{
														GrpcService: envoyGRPCService(cluster),
													}),
												},
											}),
										},
									},
								},
							}},
						},
					},
				},
			}),
		},
	}
}

func rateLimitHTTPFilter(name, cluster string) *envoyhcmv3.HttpFilter {
	return &envoyhcmv3.HttpFilter{
		Name: name,
		ConfigType: &envoyhcmv3.HttpFilter_TypedConfig{
			TypedConfig: mustAny(&envoyratelimitv3.RateLimit{
				RateLimitService: &envoyratelimitconfigv3.RateLimitServiceConfig{
					GrpcService: envoyGRPCService(cluster),
				},
			}),
		},
	}
}

func extProcPerRouteTypedConfig(cluster string) *anypb.Any {
	return mustAny(&envoyextprocv3.ExtProcPerRoute{
		Override: &envoyextprocv3.ExtProcPerRoute_Overrides{
			Overrides: &envoyextprocv3.ExtProcOverrides{
				GrpcService: envoyGRPCService(cluster),
			},
		},
	})
}

func routeFilterConfig(config *anypb.Any) *anypb.Any {
	return mustAny(&envoyroutev3.FilterConfig{
		Config: config,
	})
}

func envoyGRPCService(cluster string) *envoycorev3.GrpcService {
	return &envoycorev3.GrpcService{
		TargetSpecifier: &envoycorev3.GrpcService_EnvoyGrpc_{
			EnvoyGrpc: &envoycorev3.GrpcService_EnvoyGrpc{
				ClusterName: cluster,
			},
		},
	}
}

func httpService(cluster, uri string) *envoycorev3.HttpService {
	return &envoycorev3.HttpService{
		HttpUri: &envoycorev3.HttpUri{
			Uri: uri,
			HttpUpstreamType: &envoycorev3.HttpUri_Cluster{
				Cluster: cluster,
			},
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
