package xdscheck

import (
	"context"
	"fmt"
	"sort"
	"strings"

	xdscorev3 "github.com/cncf/xds/go/xds/core/v3"
	xdsmatcherv3 "github.com/cncf/xds/go/xds/type/matcher/v3"
	envoyaccesslogv3 "github.com/envoyproxy/go-control-plane/envoy/config/accesslog/v3"
	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
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
	envoygenericcredentialv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/http/injected_credentials/generic/v3"
	envoyoauth2credentialv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/http/injected_credentials/oauth2/v3"
	proxyprotocolv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/proxy_protocol/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	kgatewaywellknown "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

const (
	SeverityError   = "error"
	SeverityWarning = "warning"

	CodeDuplicateResourceName             = "duplicate_resource_name"
	CodeMissingRouteConfiguration         = "missing_route_configuration"
	CodeMissingCluster                    = "missing_cluster"
	CodeMissingClusterLoadAssignment      = "missing_cluster_load_assignment"
	CodeOrphanClusterLoadAssignment       = "orphan_cluster_load_assignment"
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
	CodeUnsupportedAccessLogTypedConfig   = "unsupported_access_log_typed_config"
	CodeUnsupportedFormatterTypedConfig   = "unsupported_formatter_typed_config"
	CodeUnsupportedTracingTypedConfig     = "unsupported_tracing_typed_config"
	CodeCanceled                          = "check_canceled"
)

const (
	accessLogFileTypeURL             = "type.googleapis.com/envoy.extensions.access_loggers.file.v3.FileAccessLog"
	accessLogHTTPGRPCTypeURL         = "type.googleapis.com/envoy.extensions.access_loggers.grpc.v3.HttpGrpcAccessLogConfig"
	accessLogOpenTelemetryTypeURL    = "type.googleapis.com/envoy.extensions.access_loggers.open_telemetry.v3.OpenTelemetryAccessLogConfig"
	accessLogTCPGRPCTypeURL          = "type.googleapis.com/envoy.extensions.access_loggers.grpc.v3.TcpGrpcAccessLogConfig"
	compositeHTTPFilterTypeURL       = "type.googleapis.com/envoy.extensions.filters.http.composite.v3.Composite"
	credentialInjectorTypeURL        = "type.googleapis.com/envoy.extensions.filters.http.credential_injector.v3.CredentialInjector"
	executeFilterActionTypeURL       = "type.googleapis.com/envoy.extensions.filters.http.composite.v3.ExecuteFilterAction"
	extensionWithMatcherTypeURL      = "type.googleapis.com/envoy.extensions.common.matching.v3.ExtensionWithMatcher"
	extAuthzHTTPFilterTypeURL        = "type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz"
	extProcPerRouteTypeURL           = "type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute"
	extProcHTTPFilterTypeURL         = "type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor"
	formatterGenericSecretTypeURL    = "type.googleapis.com/envoy.extensions.formatter.generic_secret.v3.GenericSecret"
	formatterMetadataTypeURL         = "type.googleapis.com/envoy.extensions.formatter.metadata.v3.Metadata"
	formatterReqWithoutQueryTypeURL  = "type.googleapis.com/envoy.extensions.formatter.req_without_query.v3.ReqWithoutQuery"
	genericInjectedCredentialTypeURL = "type.googleapis.com/envoy.extensions.http.injected_credentials.generic.v3.Generic"
	oauth2InjectedCredentialTypeURL  = "type.googleapis.com/envoy.extensions.http.injected_credentials.oauth2.v3.OAuth2"
	jwtAuthnHTTPFilterTypeURL        = "type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.JwtAuthentication"
	oauth2HTTPFilterTypeURL          = "type.googleapis.com/envoy.extensions.filters.http.oauth2.v3.OAuth2"
	oauth2HTTPFilterPrefix           = "envoy.filters.http.oauth2"
	rateLimitHTTPFilterTypeURL       = "type.googleapis.com/envoy.extensions.filters.http.ratelimit.v3.RateLimit"
	routeFilterConfigTypeURL         = "type.googleapis.com/envoy.config.route.v3.FilterConfig"
	tracingDatadogTypeURL            = "type.googleapis.com/envoy.config.trace.v3.DatadogConfig"
	tracingLightstepTypeURL          = "type.googleapis.com/envoy.config.trace.v3.LightstepConfig"
	tracingOpenTelemetryTypeURL      = "type.googleapis.com/envoy.config.trace.v3.OpenTelemetryConfig"
	tracingSkyWalkingTypeURL         = "type.googleapis.com/envoy.config.trace.v3.SkyWalkingConfig"
	tracingZipkinTypeURL             = "type.googleapis.com/envoy.config.trace.v3.ZipkinConfig"
	unsupportedFilterChainNameField  = "filter_chain_name"
	//nolint:gosec // G101: this is the well-known SDS resource *name* for the system CA bundle, not a credential.
	systemCASecretName = "SYSTEM_CA_CERT"
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
	c.requiredEndpointNames = requiredEndpointNames(s.Clusters)
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
	for _, endpoint := range s.Endpoints {
		c.checkClusterLoadAssignment(endpoint)
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

	requiredEndpointNames map[string]string
}

type anyTypedConfig interface {
	GetTypeUrl() string
	UnmarshalTo(proto.Message) error
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
			c.checkHCMAccessLogs(hcm, resource)
			c.checkHCMTracing(hcm, resource)
			c.checkHCMHTTPFilters(hcm, resource)
			c.checkHCMRouteSpecifier(ctx, listener.GetName(), filterChainName, hcm)
		}
	}
}

func (c *checker) checkHCMTracing(hcm *envoyhcmv3.HttpConnectionManager, hcmResource string) {
	provider := hcm.GetTracing().GetProvider()
	if provider == nil {
		return
	}

	providerName := provider.GetName()
	if providerName == "" {
		providerName = "<unnamed>"
	}
	c.checkTracingProvider(provider.GetTypedConfig(), fmt.Sprintf("%s Tracing/%s", hcmResource, providerName))
}

func (c *checker) checkTracingProvider(typedConfig anyTypedConfig, resource string) {
	if typedConfig == nil {
		return
	}

	switch typedConfig.GetTypeUrl() {
	case tracingDatadogTypeURL:
		c.checkDatadogTracingTypedConfig(typedConfig, resource)
	case tracingLightstepTypeURL:
		c.checkLightstepTracingTypedConfig(typedConfig, resource)
	case tracingOpenTelemetryTypeURL:
		c.checkOpenTelemetryTracingTypedConfig(typedConfig, resource)
	case tracingSkyWalkingTypeURL:
		c.checkSkyWalkingTracingTypedConfig(typedConfig, resource)
	case tracingZipkinTypeURL:
		c.checkZipkinTracingTypedConfig(typedConfig, resource)
	}
}

func (c *checker) checkDatadogTracingTypedConfig(typedConfig anyTypedConfig, resource string) {
	tracing := &envoytracev3.DatadogConfig{}
	if err := typedConfig.UnmarshalTo(tracing); err != nil {
		c.add(SeverityWarning, CodeUnsupportedTracingTypedConfig, resource,
			fmt.Sprintf("cannot unpack Datadog tracing typed_config %q; tracing collector cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		tracing.GetCollectorCluster(),
		resource,
		"collector_cluster",
	)
}

func (c *checker) checkLightstepTracingTypedConfig(typedConfig anyTypedConfig, resource string) {
	tracing := &envoytracev3.LightstepConfig{}
	if err := typedConfig.UnmarshalTo(tracing); err != nil {
		c.add(SeverityWarning, CodeUnsupportedTracingTypedConfig, resource,
			fmt.Sprintf("cannot unpack Lightstep tracing typed_config %q; tracing collector cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		tracing.GetCollectorCluster(),
		resource,
		"collector_cluster",
	)
}

func (c *checker) checkOpenTelemetryTracingTypedConfig(typedConfig anyTypedConfig, resource string) {
	tracing := &envoytracev3.OpenTelemetryConfig{}
	if err := typedConfig.UnmarshalTo(tracing); err != nil {
		c.add(SeverityWarning, CodeUnsupportedTracingTypedConfig, resource,
			fmt.Sprintf("cannot unpack OpenTelemetry tracing typed_config %q; tracing service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		tracing.GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"grpc_service.envoy_grpc.cluster_name",
	)
	c.requireClusterReference(
		tracing.GetHttpService().GetHttpUri().GetCluster(),
		resource,
		"http_service.http_uri.cluster",
	)
	c.checkHTTPServiceFormatters(tracing.GetHttpService(), resource+" http_service")
}

func (c *checker) checkSkyWalkingTracingTypedConfig(typedConfig anyTypedConfig, resource string) {
	tracing := &envoytracev3.SkyWalkingConfig{}
	if err := typedConfig.UnmarshalTo(tracing); err != nil {
		c.add(SeverityWarning, CodeUnsupportedTracingTypedConfig, resource,
			fmt.Sprintf("cannot unpack SkyWalking tracing typed_config %q; tracing service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		tracing.GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"grpc_service.envoy_grpc.cluster_name",
	)
}

func (c *checker) checkZipkinTracingTypedConfig(typedConfig anyTypedConfig, resource string) {
	tracing := &envoytracev3.ZipkinConfig{}
	if err := typedConfig.UnmarshalTo(tracing); err != nil {
		c.add(SeverityWarning, CodeUnsupportedTracingTypedConfig, resource,
			fmt.Sprintf("cannot unpack Zipkin tracing typed_config %q; tracing collector cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	if tracing.GetCollectorService() != nil {
		c.requireClusterReference(
			tracing.GetCollectorService().GetHttpUri().GetCluster(),
			resource,
			"collector_service.http_uri.cluster",
		)
		c.checkHTTPServiceFormatters(tracing.GetCollectorService(), resource+" collector_service")
		return
	}

	c.requireClusterReference(
		tracing.GetCollectorCluster(),
		resource,
		"collector_cluster",
	)
}

func (c *checker) checkHCMAccessLogs(hcm *envoyhcmv3.HttpConnectionManager, hcmResource string) {
	for i, accessLog := range hcm.GetAccessLog() {
		resourceName := "<nil>"
		if accessLog != nil {
			resourceName = accessLog.GetName()
		}
		if resourceName == "" {
			resourceName = "<unnamed>"
		}
		c.checkAccessLog(accessLog, fmt.Sprintf("%s AccessLog/%d/%s", hcmResource, i, resourceName))
	}
}

func (c *checker) checkAccessLog(accessLog *envoyaccesslogv3.AccessLog, resource string) {
	if accessLog == nil {
		return
	}
	typedConfig := accessLog.GetTypedConfig()
	if typedConfig == nil {
		return
	}

	switch typedConfig.GetTypeUrl() {
	case accessLogFileTypeURL:
		c.checkFileAccessLogTypedConfig(typedConfig, resource)
	case accessLogHTTPGRPCTypeURL:
		c.checkHTTPGRPCAccessLogTypedConfig(typedConfig, resource)
	case accessLogTCPGRPCTypeURL:
		c.checkTCPGRPCAccessLogTypedConfig(typedConfig, resource)
	case accessLogOpenTelemetryTypeURL:
		c.checkOpenTelemetryAccessLogTypedConfig(typedConfig, resource)
	}
}

func (c *checker) checkHTTPGRPCAccessLogTypedConfig(typedConfig anyTypedConfig, resource string) {
	accessLog := &envoygrpcaccesslogv3.HttpGrpcAccessLogConfig{}
	if err := typedConfig.UnmarshalTo(accessLog); err != nil {
		c.add(SeverityWarning, CodeUnsupportedAccessLogTypedConfig, resource,
			fmt.Sprintf("cannot unpack HTTP gRPC access log typed_config %q; access log service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		accessLog.GetCommonConfig().GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"common_config.grpc_service.envoy_grpc.cluster_name",
	)
}

func (c *checker) checkTCPGRPCAccessLogTypedConfig(typedConfig anyTypedConfig, resource string) {
	accessLog := &envoygrpcaccesslogv3.TcpGrpcAccessLogConfig{}
	if err := typedConfig.UnmarshalTo(accessLog); err != nil {
		c.add(SeverityWarning, CodeUnsupportedAccessLogTypedConfig, resource,
			fmt.Sprintf("cannot unpack TCP gRPC access log typed_config %q; access log service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		accessLog.GetCommonConfig().GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"common_config.grpc_service.envoy_grpc.cluster_name",
	)
}

func (c *checker) checkOpenTelemetryAccessLogTypedConfig(typedConfig anyTypedConfig, resource string) {
	accessLog := &envoyotelaccesslogv3.OpenTelemetryAccessLogConfig{}
	if err := typedConfig.UnmarshalTo(accessLog); err != nil {
		c.add(SeverityWarning, CodeUnsupportedAccessLogTypedConfig, resource,
			fmt.Sprintf("cannot unpack OpenTelemetry access log typed_config %q; access log service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		//nolint:staticcheck // SA1019: the deprecated common_config field can still carry the cluster reference; check both forms.
		accessLog.GetCommonConfig().GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"common_config.grpc_service.envoy_grpc.cluster_name",
	)
	c.requireClusterReference(
		accessLog.GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"grpc_service.envoy_grpc.cluster_name",
	)
	c.requireClusterReference(
		accessLog.GetHttpService().GetHttpUri().GetCluster(),
		resource,
		"http_service.http_uri.cluster",
	)
	c.checkFormatterTypedConfigs(accessLog.GetFormatters(), resource)
	c.checkHTTPServiceFormatters(accessLog.GetHttpService(), resource+" http_service")
}

func (c *checker) checkFileAccessLogTypedConfig(typedConfig anyTypedConfig, resource string) {
	accessLog := &envoyfileaccesslogv3.FileAccessLog{}
	if err := typedConfig.UnmarshalTo(accessLog); err != nil {
		c.add(SeverityWarning, CodeUnsupportedAccessLogTypedConfig, resource,
			fmt.Sprintf("cannot unpack File access log typed_config %q; log format formatter references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.checkSubstitutionFormatString(accessLog.GetLogFormat(), resource+" LogFormat")
}

func (c *checker) checkHTTPServiceFormatters(httpService *envoycorev3.HttpService, resource string) {
	if httpService == nil {
		return
	}
	c.checkFormatterTypedConfigs(httpService.GetFormatters(), resource)
}

func (c *checker) checkSubstitutionFormatString(format *envoycorev3.SubstitutionFormatString, resource string) {
	if format == nil {
		return
	}
	c.checkFormatterTypedConfigs(format.GetFormatters(), resource)
}

func (c *checker) checkFormatterTypedConfigs(formatters []*envoycorev3.TypedExtensionConfig, resource string) {
	for i, formatter := range formatters {
		formatterName := "<nil>"
		var typedConfig anyTypedConfig
		if formatter != nil {
			formatterName = formatter.GetName()
			typedConfig = formatter.GetTypedConfig()
		}
		if formatterName == "" {
			formatterName = "<unnamed>"
		}
		c.checkFormatterTypedConfig(typedConfig, fmt.Sprintf("%s Formatter/%d/%s", resource, i, formatterName))
	}
}

func (c *checker) checkFormatterTypedConfig(typedConfig anyTypedConfig, resource string) {
	if typedConfig == nil {
		c.add(SeverityWarning, CodeUnsupportedFormatterTypedConfig, resource,
			"formatter does not use typed_config; formatter references were not validated")
		return
	}

	switch typedConfig.GetTypeUrl() {
	case formatterGenericSecretTypeURL:
		c.checkGenericSecretFormatterTypedConfig(typedConfig, resource)
	case formatterMetadataTypeURL, formatterReqWithoutQueryTypeURL:
		return
	default:
		c.add(SeverityWarning, CodeUnsupportedFormatterTypedConfig, resource,
			fmt.Sprintf("formatter has typed_config %q; formatter references were not validated", typedConfig.GetTypeUrl()))
	}
}

func (c *checker) checkGenericSecretFormatterTypedConfig(typedConfig anyTypedConfig, resource string) {
	formatter := &envoygenericsecretformatterv3.GenericSecret{}
	if err := typedConfig.UnmarshalTo(formatter); err != nil {
		c.add(SeverityWarning, CodeUnsupportedFormatterTypedConfig, resource,
			fmt.Sprintf("cannot unpack GenericSecret formatter typed_config %q; formatter SDS references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	names := make([]string, 0, len(formatter.GetSecretConfigs()))
	for name := range formatter.GetSecretConfigs() {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c.requireSecret(formatter.GetSecretConfigs()[name], resource, fmt.Sprintf("secret_configs[%s]", name))
	}
}

func (c *checker) checkHCMHTTPFilters(hcm *envoyhcmv3.HttpConnectionManager, hcmResource string) {
	for _, filter := range hcm.GetHttpFilters() {
		resource := fmt.Sprintf("%s HttpFilter/%s", hcmResource, filter.GetName())
		c.checkHTTPFilterTypedConfig(filter.GetName(), filter.GetTypedConfig(), resource, nil)
	}
}

func (c *checker) checkHTTPFilterTypedConfig(
	filterName string,
	typedConfig anyTypedConfig,
	resource string,
	namedFilterChains map[string]*envoycompositev3.FilterChainConfiguration,
) {
	if typedConfig == nil {
		if strings.HasPrefix(filterName, oauth2HTTPFilterPrefix) {
			c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
				"OAuth2 HTTP filter does not use typed_config; SDS references were not validated")
		}
		return
	}

	switch typedConfig.GetTypeUrl() {
	case oauth2HTTPFilterTypeURL:
		c.checkOAuth2HTTPFilterTypedConfig(typedConfig, resource)
	case jwtAuthnHTTPFilterTypeURL:
		c.checkJWTAuthnHTTPFilterTypedConfig(typedConfig, resource)
	case extAuthzHTTPFilterTypeURL:
		c.checkExtAuthzHTTPFilterTypedConfig(typedConfig, resource)
	case extProcHTTPFilterTypeURL:
		c.checkExtProcHTTPFilterTypedConfig(typedConfig, resource)
	case rateLimitHTTPFilterTypeURL:
		c.checkRateLimitHTTPFilterTypedConfig(typedConfig, resource)
	case credentialInjectorTypeURL:
		c.checkCredentialInjectorHTTPFilterTypedConfig(typedConfig, resource)
	case extensionWithMatcherTypeURL:
		c.checkExtensionWithMatcherHTTPFilterTypedConfig(typedConfig, resource)
	case compositeHTTPFilterTypeURL:
		c.checkCompositeHTTPFilterTypedConfig(typedConfig, resource)
	case executeFilterActionTypeURL:
		c.checkExecuteFilterActionTypedConfig(typedConfig, resource, namedFilterChains)
	default:
		if strings.HasPrefix(filterName, oauth2HTTPFilterPrefix) {
			c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
				fmt.Sprintf("OAuth2 HTTP filter has typed_config %q; SDS references were not validated", typedConfig.GetTypeUrl()))
		}
	}
}

func (c *checker) checkOAuth2HTTPFilterTypedConfig(typedConfig anyTypedConfig, resource string) {
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

func (c *checker) checkCredentialInjectorHTTPFilterTypedConfig(typedConfig anyTypedConfig, resource string) {
	credentialInjector := &envoycredentialinjectorv3.CredentialInjector{}
	if err := typedConfig.UnmarshalTo(credentialInjector); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack CredentialInjector HTTP filter typed_config %q; injected credential references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	credential := credentialInjector.GetCredential()
	if credential == nil {
		return
	}

	credentialName := credential.GetName()
	if credentialName == "" {
		credentialName = "<unnamed>"
	}
	c.checkInjectedCredentialTypedConfig(credential.GetTypedConfig(), fmt.Sprintf("%s Credential/%s", resource, credentialName))
}

func (c *checker) checkInjectedCredentialTypedConfig(typedConfig anyTypedConfig, resource string) {
	if typedConfig == nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			"injected credential does not use typed_config; injected credential references were not validated")
		return
	}

	switch typedConfig.GetTypeUrl() {
	case genericInjectedCredentialTypeURL:
		c.checkGenericInjectedCredentialTypedConfig(typedConfig, resource)
	case oauth2InjectedCredentialTypeURL:
		c.checkOAuth2InjectedCredentialTypedConfig(typedConfig, resource)
	default:
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("injected credential has typed_config %q; injected credential references were not validated", typedConfig.GetTypeUrl()))
	}
}

func (c *checker) checkGenericInjectedCredentialTypedConfig(typedConfig anyTypedConfig, resource string) {
	credential := &envoygenericcredentialv3.Generic{}
	if err := typedConfig.UnmarshalTo(credential); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack Generic injected credential typed_config %q; injected credential SDS references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireSecret(credential.GetCredential(), resource, "credential")
}

func (c *checker) checkOAuth2InjectedCredentialTypedConfig(typedConfig anyTypedConfig, resource string) {
	credential := &envoyoauth2credentialv3.OAuth2{}
	if err := typedConfig.UnmarshalTo(credential); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack OAuth2 injected credential typed_config %q; injected credential references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireSecret(credential.GetClientCredentials().GetClientSecret(), resource, "client_credentials.client_secret")
	c.requireClusterReference(credential.GetTokenEndpoint().GetCluster(), resource, "token_endpoint.cluster")
}

func (c *checker) checkJWTAuthnHTTPFilterTypedConfig(typedConfig anyTypedConfig, resource string) {
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

func (c *checker) checkExtAuthzHTTPFilterTypedConfig(typedConfig anyTypedConfig, resource string) {
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

func (c *checker) checkExtProcHTTPFilterTypedConfig(typedConfig anyTypedConfig, resource string) {
	extProc := &envoyextprocv3.ExternalProcessor{}
	if err := typedConfig.UnmarshalTo(extProc); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack ExtProc HTTP filter typed_config %q; external processor service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		extProc.GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"grpc_service.envoy_grpc.cluster_name",
	)
}

func (c *checker) checkRateLimitHTTPFilterTypedConfig(typedConfig anyTypedConfig, resource string) {
	rateLimit := &envoyratelimitv3.RateLimit{}
	if err := typedConfig.UnmarshalTo(rateLimit); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack RateLimit HTTP filter typed_config %q; rate limit service cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		rateLimit.GetRateLimitService().GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"rate_limit_service.grpc_service.envoy_grpc.cluster_name",
	)
}

func (c *checker) checkExtensionWithMatcherHTTPFilterTypedConfig(typedConfig anyTypedConfig, resource string) {
	extensionWithMatcher := &envoymatchingv3.ExtensionWithMatcher{}
	if err := typedConfig.UnmarshalTo(extensionWithMatcher); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack ExtensionWithMatcher HTTP filter typed_config %q; matched HTTP filter references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	namedFilterChains := c.checkHTTPFilterTypedExtensionConfig(
		extensionWithMatcher.GetExtensionConfig(),
		resource+" ExtensionConfig",
		nil,
	)
	//nolint:staticcheck // SA1019: emitted configs may still use the deprecated matcher field; detect it to warn instead of silently skipping.
	if extensionWithMatcher.GetMatcher() != nil && extensionWithMatcher.GetXdsMatcher() == nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			"ExtensionWithMatcher uses deprecated matcher; matched HTTP filter references were not validated")
		return
	}
	c.checkXDSMatcher(extensionWithMatcher.GetXdsMatcher(), namedFilterChains, resource+" XdsMatcher")
}

func (c *checker) checkCompositeHTTPFilterTypedConfig(
	typedConfig anyTypedConfig,
	resource string,
) map[string]*envoycompositev3.FilterChainConfiguration {
	composite := &envoycompositev3.Composite{}
	if err := typedConfig.UnmarshalTo(composite); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack Composite HTTP filter typed_config %q; matched HTTP filter references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return nil
	}

	namedFilterChains := composite.GetNamedFilterChains()
	for name, chain := range namedFilterChains {
		c.checkCompositeFilterChain(chain, fmt.Sprintf("%s NamedFilterChain/%s", resource, name), namedFilterChains)
	}
	c.checkXDSMatcher(composite.GetMatcher(), namedFilterChains, resource+" Matcher")
	return namedFilterChains
}

func (c *checker) checkExecuteFilterActionTypedConfig(
	typedConfig anyTypedConfig,
	resource string,
	namedFilterChains map[string]*envoycompositev3.FilterChainConfiguration,
) {
	action := &envoycompositev3.ExecuteFilterAction{}
	if err := typedConfig.UnmarshalTo(action); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack ExecuteFilterAction typed_config %q; delegated HTTP filter references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.checkHTTPFilterTypedExtensionConfig(action.GetTypedConfig(), resource+" TypedConfig", namedFilterChains)
	c.checkCompositeFilterChain(action.GetFilterChain(), resource+" FilterChain", namedFilterChains)
	if action.GetFilterChainName() == "" {
		return
	}
	filterChain, ok := namedFilterChains[action.GetFilterChainName()]
	if !ok {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("%s %q does not match a Composite named filter chain; delegated HTTP filter references were not validated", unsupportedFilterChainNameField, action.GetFilterChainName()))
		return
	}
	c.checkCompositeFilterChain(filterChain, fmt.Sprintf("%s NamedFilterChain/%s", resource, action.GetFilterChainName()), namedFilterChains)
}

func (c *checker) checkHTTPFilterTypedExtensionConfig(
	config *envoycorev3.TypedExtensionConfig,
	resource string,
	namedFilterChains map[string]*envoycompositev3.FilterChainConfiguration,
) map[string]*envoycompositev3.FilterChainConfiguration {
	if config == nil {
		return namedFilterChains
	}
	if config.GetName() != "" {
		resource = fmt.Sprintf("%s/%s", resource, config.GetName())
	}
	c.checkHTTPFilterTypedConfig(config.GetName(), config.GetTypedConfig(), resource, namedFilterChains)
	if config.GetTypedConfig() == nil || config.GetTypedConfig().GetTypeUrl() != compositeHTTPFilterTypeURL {
		return namedFilterChains
	}

	composite := &envoycompositev3.Composite{}
	if err := config.GetTypedConfig().UnmarshalTo(composite); err != nil {
		return namedFilterChains
	}
	return composite.GetNamedFilterChains()
}

func (c *checker) checkXDSHTTPFilterTypedExtensionConfig(
	config *xdscorev3.TypedExtensionConfig,
	resource string,
	namedFilterChains map[string]*envoycompositev3.FilterChainConfiguration,
) {
	if config == nil {
		return
	}
	if config.GetName() != "" {
		resource = fmt.Sprintf("%s/%s", resource, config.GetName())
	}
	c.checkHTTPFilterTypedConfig(config.GetName(), config.GetTypedConfig(), resource, namedFilterChains)
}

func (c *checker) checkCompositeFilterChain(
	chain *envoycompositev3.FilterChainConfiguration,
	resource string,
	namedFilterChains map[string]*envoycompositev3.FilterChainConfiguration,
) {
	if chain == nil {
		return
	}
	for i, typedConfig := range chain.GetTypedConfig() {
		c.checkHTTPFilterTypedExtensionConfig(typedConfig, fmt.Sprintf("%s TypedConfig/%d", resource, i), namedFilterChains)
	}
}

func (c *checker) checkXDSMatcher(
	matcher *xdsmatcherv3.Matcher,
	namedFilterChains map[string]*envoycompositev3.FilterChainConfiguration,
	resource string,
) {
	if matcher == nil {
		return
	}
	for i, fieldMatcher := range matcher.GetMatcherList().GetMatchers() {
		c.checkXDSMatcherOnMatch(
			fieldMatcher.GetOnMatch(),
			namedFilterChains,
			fmt.Sprintf("%s MatcherList/%d", resource, i),
		)
	}
	for key, onMatch := range matcher.GetMatcherTree().GetExactMatchMap().GetMap() {
		c.checkXDSMatcherOnMatch(onMatch, namedFilterChains, fmt.Sprintf("%s ExactMatch/%s", resource, key))
	}
	for key, onMatch := range matcher.GetMatcherTree().GetPrefixMatchMap().GetMap() {
		c.checkXDSMatcherOnMatch(onMatch, namedFilterChains, fmt.Sprintf("%s PrefixMatch/%s", resource, key))
	}
	c.checkXDSMatcherOnMatch(matcher.GetOnNoMatch(), namedFilterChains, resource+" OnNoMatch")
}

func (c *checker) checkXDSMatcherOnMatch(
	onMatch *xdsmatcherv3.Matcher_OnMatch,
	namedFilterChains map[string]*envoycompositev3.FilterChainConfiguration,
	resource string,
) {
	if onMatch == nil {
		return
	}
	if onMatch.GetMatcher() != nil {
		c.checkXDSMatcher(onMatch.GetMatcher(), namedFilterChains, resource+" Matcher")
	}
	if onMatch.GetAction() != nil {
		c.checkXDSHTTPFilterTypedExtensionConfig(onMatch.GetAction(), resource+" Action", namedFilterChains)
	}
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
	name := secretConfig.GetName()
	if name == systemCASecretName {
		return
	}
	if _, ok := c.secrets[name]; ok {
		return
	}
	c.add(SeverityError, CodeMissingSecret, resource,
		fmt.Sprintf("%s references missing SDS secret %q", field, name))
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
		c.checkRouteTypedPerFilterConfigs(virtualHost.GetTypedPerFilterConfig(), vhostResource)
		for _, route := range virtualHost.GetRoutes() {
			routeName := route.GetName()
			if routeName == "" {
				routeName = "<unnamed>"
			}
			routeResource := fmt.Sprintf("%s Route/%s", vhostResource, routeName)
			c.checkRouteTypedPerFilterConfigs(route.GetTypedPerFilterConfig(), routeResource)
			c.checkRouteAction(route.GetRoute(), routeResource, routeConfig.GetName(), virtualHost.GetName(), routeName)
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
			c.checkRouteTypedPerFilterConfigs(clusterWeight.GetTypedPerFilterConfig(), clusterResource)
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

func (c *checker) checkRouteTypedPerFilterConfigs(configs map[string]*anypb.Any, resource string) {
	for name, typedConfig := range configs {
		c.checkRouteTypedPerFilterConfig(typedConfig, fmt.Sprintf("%s TypedPerFilterConfig/%s", resource, name))
	}
}

func (c *checker) checkRouteTypedPerFilterConfig(typedConfig anyTypedConfig, resource string) {
	if typedConfig == nil {
		return
	}

	switch typedConfig.GetTypeUrl() {
	case extProcPerRouteTypeURL:
		c.checkExtProcPerRouteTypedConfig(typedConfig, resource)
	case routeFilterConfigTypeURL:
		filterConfig := &envoyroutev3.FilterConfig{}
		if err := typedConfig.UnmarshalTo(filterConfig); err != nil {
			c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
				fmt.Sprintf("cannot unpack route FilterConfig typed_config %q; nested per-filter references were not validated: %v", typedConfig.GetTypeUrl(), err))
			return
		}
		c.checkRouteTypedPerFilterConfig(filterConfig.GetConfig(), resource+" Config")
	}
}

func (c *checker) checkExtProcPerRouteTypedConfig(typedConfig anyTypedConfig, resource string) {
	perRoute := &envoyextprocv3.ExtProcPerRoute{}
	if err := typedConfig.UnmarshalTo(perRoute); err != nil {
		c.add(SeverityWarning, CodeUnsupportedHTTPFilterTypedConfig, resource,
			fmt.Sprintf("cannot unpack ExtProc per-route typed_config %q; per-route external processor cluster references were not validated: %v", typedConfig.GetTypeUrl(), err))
		return
	}

	c.requireClusterReference(
		perRoute.GetOverrides().GetGrpcService().GetEnvoyGrpc().GetClusterName(),
		resource,
		"overrides.grpc_service.envoy_grpc.cluster_name",
	)
}

func (c *checker) requireCluster(name, resource, routeConfigName, virtualHostName, routeName string) {
	if name == "" {
		return
	}
	if name == kgatewaywellknown.BlackholeClusterName {
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
	if name == kgatewaywellknown.BlackholeClusterName {
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

func (c *checker) checkClusterLoadAssignment(endpoint *envoyendpointv3.ClusterLoadAssignment) {
	if endpoint == nil || endpoint.GetClusterName() == "" {
		return
	}
	if _, ok := c.requiredEndpointNames[endpoint.GetClusterName()]; ok {
		return
	}
	c.add(SeverityError, CodeOrphanClusterLoadAssignment, clusterLoadAssignmentResource(endpoint.GetClusterName()),
		fmt.Sprintf("ClusterLoadAssignment %q has no matching EDS cluster; ADS named EDS snapshots should not include endpoint resources Envoy will not request", endpoint.GetClusterName()))
}

func requiredEndpointNames(clusters []*envoyclusterv3.Cluster) map[string]string {
	out := make(map[string]string)
	for _, cluster := range clusters {
		if cluster == nil || cluster.GetType() != envoyclusterv3.Cluster_EDS {
			continue
		}
		endpointName := cluster.GetEdsClusterConfig().GetServiceName()
		if endpointName == "" {
			endpointName = cluster.GetName()
		}
		out[endpointName] = cluster.GetName()
	}
	return out
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

func clusterLoadAssignmentResource(name string) string {
	return fmt.Sprintf("ClusterLoadAssignment/%s", name)
}
