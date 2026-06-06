package xdscheck

import (
	"context"
	"fmt"
	"strings"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoyextauthzv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/ext_authz/v3"
	envoyjwtauthnv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/jwt_authn/v3"
	envoyoauth2v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/oauth2/v3"
	envoyhcmv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	proxyprotocolv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/proxy_protocol/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"

	CodeDuplicateResourceName             = "duplicate_resource_name"
	CodeMissingRouteConfiguration         = "missing_route_configuration"
	CodeMissingCluster                    = "missing_cluster"
	CodeMissingClusterLoadAssignment      = "missing_cluster_load_assignment"
	CodeUnsupportedHCMTypedConfig         = "unsupported_hcm_typed_config"
	CodeUnsupportedHCMConfigType          = "unsupported_hcm_config_type"
	CodeUnsupportedHCMRouteSpecifier      = "unsupported_hcm_route_specifier"
	CodeUnsupportedRouteClusterHeader     = "unsupported_route_cluster_header"
	CodeUnsupportedClusterSpecifierPlugin = "unsupported_cluster_specifier_plugin"
	CodeUnsupportedWeightedClusterHeader  = "unsupported_weighted_cluster_header"
	CodeUnsupportedInlineClusterSpecifier = "unsupported_inline_cluster_specifier"
	CodeMissingSecret                     = "missing_secret"
	CodeUnsupportedTLSTypedConfig         = "unsupported_tls_typed_config"
	CodeUnsupportedHTTPFilterTypedConfig  = "unsupported_http_filter_typed_config"
	CodeCanceled                          = "check_canceled"
)

const (
	oauth2HTTPFilterTypeURL   = "type.googleapis.com/envoy.extensions.filters.http.oauth2.v3.OAuth2"
	oauth2HTTPFilterPrefix    = "envoy.filters.http.oauth2"
	extAuthzHTTPFilterTypeURL = "type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz"
	jwtAuthnHTTPFilterTypeURL = "type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.JwtAuthentication"
)

// Snapshot is the Envoy xDS resource set checked by this package.
type Snapshot struct {
	Listeners []*envoylistenerv3.Listener
	Routes    []*envoyroutev3.RouteConfiguration
	Clusters  []*envoyclusterv3.Cluster
	Endpoints []*envoyendpointv3.ClusterLoadAssignment
	Secrets   []*envoytlsv3.Secret
}

// Finding describes a precise invariant result for a concrete xDS snapshot.
type Finding struct {
	Severity string
	Code     string
	Resource string
	Message  string
}

// CheckSnapshot checks concrete LDS/RDS/CDS/EDS dependency invariants without
// invoking Envoy or changing production behavior.
func CheckSnapshot(ctx context.Context, s Snapshot) []Finding {
	if ctx == nil {
		ctx = context.Background()
	}

	c := checker{}
	c.routes = indexByName(s.Routes, "RouteConfiguration", func(r *envoyroutev3.RouteConfiguration) string {
		return r.GetName()
	}, &c.findings)
	c.clusters = indexByName(s.Clusters, "Cluster", func(c *envoyclusterv3.Cluster) string {
		return c.GetName()
	}, &c.findings)
	c.endpoints = indexByName(s.Endpoints, "ClusterLoadAssignment", func(e *envoyendpointv3.ClusterLoadAssignment) string {
		return e.GetClusterName()
	}, &c.findings)
	indexByName(s.Listeners, "Listener", func(l *envoylistenerv3.Listener) string {
		return l.GetName()
	}, &c.findings)
	c.secrets = indexByName(s.Secrets, "Secret", func(s *envoytlsv3.Secret) string {
		return s.GetName()
	}, &c.findings)

	if c.isCanceled(ctx) {
		return c.findings
	}
	for _, listener := range s.Listeners {
		c.checkListener(ctx, listener)
		if c.isCanceled(ctx) {
			return c.findings
		}
	}
	for _, route := range s.Routes {
		c.checkRouteConfiguration(ctx, route, routeResource(route.GetName()))
		if c.isCanceled(ctx) {
			return c.findings
		}
	}
	for _, cluster := range s.Clusters {
		c.checkEDSCluster(cluster)
		c.checkClusterTransportSockets(cluster)
	}

	return c.findings
}

// ErrorFindings returns only error-severity findings.
func ErrorFindings(findings []Finding) []Finding {
	var out []Finding
	for _, finding := range findings {
		if finding.Severity == SeverityError {
			out = append(out, finding)
		}
	}
	return out
}

type checker struct {
	findings  []Finding
	routes    map[string]*envoyroutev3.RouteConfiguration
	clusters  map[string]*envoyclusterv3.Cluster
	endpoints map[string]*envoyendpointv3.ClusterLoadAssignment
	secrets   map[string]*envoytlsv3.Secret
}

func (c *checker) checkListener(ctx context.Context, listener *envoylistenerv3.Listener) {
	for _, filterChain := range listener.GetFilterChains() {
		filterChainName := filterChain.GetName()
		if filterChainName == "" {
			filterChainName = "<unnamed>"
		}
		c.checkDownstreamTransportSocket(
			filterChain.GetTransportSocket(),
			fmt.Sprintf("%s FilterChain/%s TransportSocket", listenerResource(listener.GetName()), filterChainName),
		)
		for _, filter := range filterChain.GetFilters() {
			if ctx.Err() != nil {
				return
			}
			if filter.GetName() != envoywellknown.HTTPConnectionManager {
				continue
			}

			resource := fmt.Sprintf("%s FilterChain/%s Filter/%s", listenerResource(listener.GetName()), filterChainName, filter.GetName())
			hcm, ok := c.unpackHCM(filter, resource)
			if !ok {
				continue
			}
			c.checkHCMHTTPFilters(hcm, resource)
			c.checkHCMRouteSpecifier(ctx, listener.GetName(), filterChainName, hcm)
		}
	}
}

func (c *checker) checkHCMHTTPFilters(hcm *envoyhcmv3.HttpConnectionManager, hcmResource string) {
	for _, filter := range hcm.GetHttpFilters() {
		resource := fmt.Sprintf("%s HttpFilter/%s", hcmResource, filter.GetName())
		c.checkOAuth2HTTPFilter(filter, resource)
		c.checkJWTAuthnHTTPFilter(filter, resource)
		c.checkExtAuthzHTTPFilter(filter, resource)
	}
}

func (c *checker) checkOAuth2HTTPFilter(filter *envoyhcmv3.HttpFilter, resource string) {
	typedConfig := filter.GetTypedConfig()
	if typedConfig == nil {
		if strings.HasPrefix(filter.GetName(), oauth2HTTPFilterPrefix) {
			c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
				"OAuth2 HTTP filter does not use typed_config; SDS references were not validated")
		}
		return
	}

	if typedConfig.GetTypeUrl() != oauth2HTTPFilterTypeURL {
		if strings.HasPrefix(filter.GetName(), oauth2HTTPFilterPrefix) {
			c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
				fmt.Sprintf("OAuth2 HTTP filter has typed_config %q; SDS references were not validated", typedConfig.GetTypeUrl()))
		}
		return
	}

	oauth2 := &envoyoauth2v3.OAuth2{}
	if err := typedConfig.UnmarshalTo(oauth2); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack OAuth2 HTTP filter typed_config %q; SDS references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	credentials := oauth2.GetConfig().GetCredentials()
	c.requireSecret(credentials.GetTokenSecret(), resource, "config.credentials.token_secret")
	c.requireSecret(credentials.GetHmacSecret(), resource, "config.credentials.hmac_secret")
	c.requireClusterReference(oauth2.GetConfig().GetTokenEndpoint().GetCluster(), resource, "config.token_endpoint.cluster")
}

func (c *checker) checkJWTAuthnHTTPFilter(filter *envoyhcmv3.HttpFilter, resource string) {
	typedConfig := filter.GetTypedConfig()
	if typedConfig == nil || typedConfig.GetTypeUrl() != jwtAuthnHTTPFilterTypeURL {
		return
	}

	jwtAuthn := &envoyjwtauthnv3.JwtAuthentication{}
	if err := typedConfig.UnmarshalTo(jwtAuthn); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack JWT AuthN HTTP filter typed_config %q; remote JWKS cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	for providerName, provider := range jwtAuthn.GetProviders() {
		c.requireClusterReference(
			provider.GetRemoteJwks().GetHttpUri().GetCluster(),
			resource,
			fmt.Sprintf("providers[%s].remote_jwks.http_uri.cluster", providerName),
		)
	}
}

func (c *checker) checkExtAuthzHTTPFilter(filter *envoyhcmv3.HttpFilter, resource string) {
	typedConfig := filter.GetTypedConfig()
	if typedConfig == nil || typedConfig.GetTypeUrl() != extAuthzHTTPFilterTypeURL {
		return
	}

	extAuthz := &envoyextauthzv3.ExtAuthz{}
	if err := typedConfig.UnmarshalTo(extAuthz); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack ExtAuthz HTTP filter typed_config %q; authorization service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		extAuthz.GetHttpService().GetServerUri().GetCluster(),
		resource,
		"http_service.server_uri.cluster",
	)
	c.requireClusterReference(
		extAuthz.GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"grpc_service.envoy_grpc.cluster_name",
	)
}

func (c *checker) checkClusterTransportSockets(cluster *envoyclusterv3.Cluster) {
	c.checkUpstreamTransportSocket(cluster.GetTransportSocket(), fmt.Sprintf("%s TransportSocket", clusterResource(cluster.GetName())))
	for _, match := range cluster.GetTransportSocketMatches() {
		matchName := match.GetName()
		if matchName == "" {
			matchName = "<unnamed>"
		}
		c.checkUpstreamTransportSocket(
			match.GetTransportSocket(),
			fmt.Sprintf("%s TransportSocketMatch/%s TransportSocket", clusterResource(cluster.GetName()), matchName),
		)
	}
}

func (c *checker) checkDownstreamTransportSocket(socket *envoycorev3.TransportSocket, resource string) {
	if socket == nil || socket.GetName() != envoywellknown.TransportSocketTls {
		return
	}
	typedConfig := socket.GetTypedConfig()
	if typedConfig == nil {
		c.add(SeverityWarning, CodeUnsupportedTLSTypedConfig, resource,
			"downstream TLS transport socket does not use typed_config; SDS references were not validated")
		return
	}
	tlsContext := &envoytlsv3.DownstreamTlsContext{}
	if err := typedConfig.UnmarshalTo(tlsContext); err != nil {
		c.add(SeverityWarning, CodeUnsupportedTLSTypedConfig, resource,
			fmt.Sprintf("cannot unpack downstream TLS transport socket typed_config %q; SDS references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}
	c.checkCommonTLSContext(tlsContext.GetCommonTlsContext(), resource)
	c.requireSecret(tlsContext.GetSessionTicketKeysSdsSecretConfig(), resource, "session_ticket_keys_sds_secret_config")
}

func (c *checker) checkUpstreamTransportSocket(socket *envoycorev3.TransportSocket, resource string) {
	if socket == nil {
		return
	}
	switch socket.GetName() {
	case envoywellknown.TransportSocketTls:
		typedConfig := socket.GetTypedConfig()
		if typedConfig == nil {
			c.add(SeverityWarning, CodeUnsupportedTLSTypedConfig, resource,
				"upstream TLS transport socket does not use typed_config; SDS references were not validated")
			return
		}
		tlsContext := &envoytlsv3.UpstreamTlsContext{}
		if err := typedConfig.UnmarshalTo(tlsContext); err != nil {
			c.add(SeverityWarning, CodeUnsupportedTLSTypedConfig, resource,
				fmt.Sprintf("cannot unpack upstream TLS transport socket typed_config %q; SDS references were not validated: %v", typedConfig.GetTypeUrl(), err))
			return
		}
		c.checkCommonTLSContext(tlsContext.GetCommonTlsContext(), resource)
	case "envoy.transport_sockets.upstream_proxy_protocol":
		typedConfig := socket.GetTypedConfig()
		if typedConfig == nil {
			c.add(SeverityWarning, CodeUnsupportedTLSTypedConfig, resource,
				"upstream proxy protocol transport socket does not use typed_config; nested SDS references were not validated")
			return
		}
		proxyProtocol := &proxyprotocolv3.ProxyProtocolUpstreamTransport{}
		if err := typedConfig.UnmarshalTo(proxyProtocol); err != nil {
			c.add(SeverityWarning, CodeUnsupportedTLSTypedConfig, resource,
				fmt.Sprintf("cannot unpack upstream proxy protocol transport socket typed_config %q; nested SDS references were not validated: %v", typedConfig.GetTypeUrl(), err))
			return
		}
		c.checkUpstreamTransportSocket(proxyProtocol.GetTransportSocket(), resource+" InnerTransportSocket")
	}
}

func (c *checker) checkCommonTLSContext(common *envoytlsv3.CommonTlsContext, resource string) {
	if common == nil {
		return
	}
	for i, secretConfig := range common.GetTlsCertificateSdsSecretConfigs() {
		c.requireSecret(secretConfig, resource, fmt.Sprintf("tls_certificate_sds_secret_configs[%d]", i))
	}
	c.requireSecret(common.GetValidationContextSdsSecretConfig(), resource, "validation_context_sds_secret_config")
	c.requireSecret(common.GetCombinedValidationContext().GetValidationContextSdsSecretConfig(), resource, "combined_validation_context.validation_context_sds_secret_config")
}

func (c *checker) requireSecret(secretConfig *envoytlsv3.SdsSecretConfig, resource, field string) {
	if secretConfig == nil || secretConfig.GetName() == "" {
		return
	}
	if _, ok := c.secrets[secretConfig.GetName()]; ok {
		return
	}
	c.add(SeverityError, CodeMissingSecret, resource,
		fmt.Sprintf("%s references missing SDS secret %q", field, secretConfig.GetName()))
}

func (c *checker) unpackHCM(filter *envoylistenerv3.Filter, resource string) (*envoyhcmv3.HttpConnectionManager, bool) {
	typedConfig := filter.GetTypedConfig()
	if typedConfig == nil {
		c.add(SeverityWarning, CodeUnsupportedHCMConfigType, resource,
			"HCM filter does not use typed_config; route references were not validated")
		return nil, false
	}

	hcm := &envoyhcmv3.HttpConnectionManager{}
	if err := typedConfig.UnmarshalTo(hcm); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHCMTypedConfig, resource,
			fmt.Sprintf("cannot unpack HCM typed_config %q; route references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return nil, false
	}
	return hcm, true
}

func (c *checker) checkHCMRouteSpecifier(ctx context.Context, listenerName, filterChainName string, hcm *envoyhcmv3.HttpConnectionManager) {
	resource := fmt.Sprintf("%s FilterChain/%s", listenerResource(listenerName), filterChainName)
	switch routeSpecifier := hcm.GetRouteSpecifier().(type) {
	case *envoyhcmv3.HttpConnectionManager_Rds:
		routeName := routeSpecifier.Rds.GetRouteConfigName()
		if _, ok := c.routes[routeName]; !ok {
			c.add(SeverityError, CodeMissingRouteConfiguration, resource,
				fmt.Sprintf("listener %q filter chain %q references missing RDS route configuration %q", listenerName, filterChainName, routeName))
		}
	case *envoyhcmv3.HttpConnectionManager_RouteConfig:
		c.checkRouteConfiguration(ctx, routeSpecifier.RouteConfig, fmt.Sprintf("%s InlineRouteConfiguration", resource))
	case *envoyhcmv3.HttpConnectionManager_ScopedRoutes:
		c.add(SeverityWarning, CodeUnsupportedHCMRouteSpecifier, resource,
			"scoped_routes route specifier is not validated by xdscheck")
	case nil:
		c.add(SeverityWarning, CodeUnsupportedHCMRouteSpecifier, resource,
			"HCM route specifier is empty; route references were not validated")
	default:
		c.add(SeverityWarning, CodeUnsupportedHCMRouteSpecifier, resource,
			fmt.Sprintf("HCM route specifier %T is not validated by xdscheck", routeSpecifier))
	}
}

func (c *checker) checkRouteConfiguration(ctx context.Context, routeConfig *envoyroutev3.RouteConfiguration, resourcePrefix string) {
	if routeConfig == nil {
		return
	}
	for _, virtualHost := range routeConfig.GetVirtualHosts() {
		if ctx.Err() != nil {
			return
		}
		vhostResource := fmt.Sprintf("%s VirtualHost/%s", resourcePrefix, virtualHost.GetName())
		for _, route := range virtualHost.GetRoutes() {
			routeName := route.GetName()
			if routeName == "" {
				routeName = "<unnamed>"
			}
			c.checkRouteAction(route.GetRoute(), fmt.Sprintf("%s Route/%s", vhostResource, routeName), routeConfig.GetName(), virtualHost.GetName(), routeName)
		}
	}
}

func (c *checker) checkRouteAction(routeAction *envoyroutev3.RouteAction, resource, routeConfigName, virtualHostName, routeName string) {
	if routeAction == nil {
		return
	}

	switch clusterSpecifier := routeAction.GetClusterSpecifier().(type) {
	case *envoyroutev3.RouteAction_Cluster:
		c.requireCluster(clusterSpecifier.Cluster, resource, routeConfigName, virtualHostName, routeName)
	case *envoyroutev3.RouteAction_WeightedClusters:
		for i, clusterWeight := range clusterSpecifier.WeightedClusters.GetClusters() {
			clusterResource := fmt.Sprintf("%s WeightedCluster/%d", resource, i)
			if clusterWeight.GetClusterHeader() != "" {
				c.add(SeverityWarning, CodeUnsupportedWeightedClusterHeader, clusterResource,
					fmt.Sprintf("weighted cluster entry uses cluster_header %q; static cluster existence cannot be verified", clusterWeight.GetClusterHeader()))
				continue
			}
			c.requireCluster(clusterWeight.GetName(), clusterResource, routeConfigName, virtualHostName, routeName)
		}
	case *envoyroutev3.RouteAction_ClusterHeader:
		c.add(SeverityWarning, CodeUnsupportedRouteClusterHeader, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses cluster_header %q; static cluster existence cannot be verified", routeConfigName, virtualHostName, routeName, clusterSpecifier.ClusterHeader))
	case *envoyroutev3.RouteAction_ClusterSpecifierPlugin:
		c.add(SeverityWarning, CodeUnsupportedClusterSpecifierPlugin, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses cluster_specifier_plugin %q; static cluster existence cannot be verified", routeConfigName, virtualHostName, routeName, clusterSpecifier.ClusterSpecifierPlugin))
	case *envoyroutev3.RouteAction_InlineClusterSpecifierPlugin:
		c.add(SeverityWarning, CodeUnsupportedInlineClusterSpecifier, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses inline cluster specifier plugin; static cluster existence cannot be verified", routeConfigName, virtualHostName, routeName))
	case nil:
		return
	default:
		c.add(SeverityWarning, CodeUnsupportedClusterSpecifierPlugin, resource,
			fmt.Sprintf("route configuration %q virtual host %q route %q uses unsupported cluster specifier %T", routeConfigName, virtualHostName, routeName, clusterSpecifier))
	}
}

func (c *checker) requireCluster(name, resource, routeConfigName, virtualHostName, routeName string) {
	if name == "" {
		return
	}
	if _, ok := c.clusters[name]; ok {
		return
	}
	c.add(SeverityError, CodeMissingCluster, resource,
		fmt.Sprintf("route configuration %q virtual host %q route %q references missing cluster %q", routeConfigName, virtualHostName, routeName, name))
}

func (c *checker) requireClusterReference(name, resource, field string) {
	if name == "" {
		return
	}
	if _, ok := c.clusters[name]; ok {
		return
	}
	c.add(SeverityError, CodeMissingCluster, resource,
		fmt.Sprintf("%s references missing cluster %q", field, name))
}

func (c *checker) checkEDSCluster(cluster *envoyclusterv3.Cluster) {
	if cluster.GetType() != envoyclusterv3.Cluster_EDS {
		return
	}

	expectedName := cluster.GetEdsClusterConfig().GetServiceName()
	if expectedName == "" {
		expectedName = cluster.GetName()
	}
	if _, ok := c.endpoints[expectedName]; ok {
		return
	}
	c.add(SeverityError, CodeMissingClusterLoadAssignment, clusterResource(cluster.GetName()),
		fmt.Sprintf("cluster %q uses EDS resource %q but no matching ClusterLoadAssignment was emitted", cluster.GetName(), expectedName))
}

func (c *checker) isCanceled(ctx context.Context) bool {
	err := ctx.Err()
	if err == nil {
		return false
	}
	c.add(SeverityError, CodeCanceled, "Snapshot", fmt.Sprintf("xDS snapshot check canceled: %v", err))
	return true
}

func (c *checker) add(severity, code, resource, message string) {
	c.findings = append(c.findings, Finding{
		Severity: severity,
		Code:     code,
		Resource: resource,
		Message:  message,
	})
}

func indexByName[T any](items []T, typeName string, nameOf func(T) string, findings *[]Finding) map[string]T {
	out := make(map[string]T, len(items))
	firstIndex := make(map[string]int, len(items))
	for i, item := range items {
		name := nameOf(item)
		if first, ok := firstIndex[name]; ok {
			*findings = append(*findings, Finding{
				Severity: SeverityError,
				Code:     CodeDuplicateResourceName,
				Resource: fmt.Sprintf("%s/%s", typeName, name),
				Message:  fmt.Sprintf("duplicate %s resource name %q at indexes %d and %d", typeName, name, first, i),
			})
			continue
		}
		firstIndex[name] = i
		out[name] = item
	}
	return out
}

func listenerResource(name string) string {
	return fmt.Sprintf("Listener/%s", name)
}

func routeResource(name string) string {
	return fmt.Sprintf("RouteConfiguration/%s", name)
}

func clusterResource(name string) string {
	return fmt.Sprintf("Cluster/%s", name)
}
