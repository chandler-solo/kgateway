package irtranslator

import (
	"context"
	"errors"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycommondnsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/clusters/common/dns/v3"
	envoydnsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/clusters/dns/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoy_upstreams_v3 "github.com/envoyproxy/go-control-plane/envoy/extensions/upstreams/http/v3"
	envoywellknown "github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"

	apisettings "github.com/kgateway-dev/kgateway/v2/api/settings"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/endpoints"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/extensions2/pluginutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	sdk "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/collections"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/validator"
	"github.com/kgateway-dev/kgateway/v2/pkg/xds/bootstrap"
)

const (
	clusterConnectionTimeout = time.Second * 5
	dnsClusterExtensionName  = "envoy.clusters.dns"
)

type BackendTranslator struct {
	ContributedBackends map[schema.GroupKind]ir.BackendInit
	ContributedPolicies map[schema.GroupKind]sdk.PolicyPlugin
	EndpointPlugins     []sdk.EndpointPlugin
	CommonCols          *collections.CommonCollections
	Validator           validator.Validator
	Mode                apisettings.ValidationMode
}

// BackendBaseCluster is the client-independent ("base") translation of a Backend into an
// Envoy Cluster. It is produced once per Backend and shared across all uniquely-connected
// clients; the per-client overlay (destination rules, locality priorities) is applied later
// by ApplyPerClientOverlay. Splitting translation this way avoids re-running the full backend
// plugin chain once per (backend, client) pair.
type BackendBaseCluster struct {
	// Name is the source Backend's ResourceName; it keys this element in the KRT collection.
	Name string
	// Cluster is the base proto. When Err != nil this is a blackhole cluster and the overlay
	// is skipped (all clients share the blackhole + error).
	// +krtEqualsTodo include full cluster diff in equality
	Cluster *envoyclusterv3.Cluster
	// Version is HashProto(Cluster); it stands in for a deep proto diff in Equals.
	Version uint64
	// InlineEps are the inline endpoints captured from InitEnvoyBackend, needed by the
	// per-client endpoint plugins / inline-CLA path. Nil for EDS clusters.
	InlineEps *ir.EndpointsForBackend
	// Err is the base translation error, if any. Compared by message in Equals because all
	// errored clusters share one blackhole proto, so Version can't tell error states apart.
	Err error
}

func (b BackendBaseCluster) ResourceName() string {
	return b.Name
}

func (b BackendBaseCluster) Equals(in BackendBaseCluster) bool {
	return b.Name == in.Name &&
		b.Version == in.Version &&
		errMsg(b.Err) == errMsg(in.Err) &&
		inlineEpsEqual(b.InlineEps, in.InlineEps)
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func inlineEpsEqual(a, b *ir.EndpointsForBackend) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equals(*b)
}

// TranslateBackend translates a BackendObjectIR to an Envoy Cluster for a single client. It is a
// thin wrapper over the base translation (TranslateBackendBase) followed by the per-client overlay
// (ApplyPerClientOverlay). If we encounter any errors during translation, a blackhole cluster is
// returned along with the error. The error return value is what matters as consumers
// (pkg/kgateway/proxy_syncer/perclient.go) will drop errored clusters from the xDS snapshot and
// track them separately for status reporting. The blackhole cluster itself is not sent to Envoy
// but provides a consistent return structure.
func (t *BackendTranslator) TranslateBackend(
	ctx context.Context,
	kctx krt.HandlerContext,
	ucc ir.UniquelyConnectedClient,
	backend *ir.BackendObjectIR,
) (*envoyclusterv3.Cluster, error) {
	// defensive checks that the backend is supported and has a plugin that can translate it.
	gk := backend.GetGroupKind()
	process, ok := t.ContributedBackends[gk]
	if !ok {
		return nil, errors.New("no backend translator found for " + gk.String())
	}
	if process.InitEnvoyBackend == nil {
		return nil, errors.New("no backend plugin found for " + gk.String())
	}

	base := t.TranslateBackendBase(ctx, backend)
	if base == nil {
		// Unreachable given the checks above, but keep a defined return.
		return nil, errors.New("no base cluster translated for " + gk.String())
	}
	if base.Err != nil {
		return base.Cluster, base.Err
	}
	return t.ApplyPerClientOverlay(ctx, kctx, ucc, backend, base)
}

// TranslateBackendBase performs the client-independent ("base") translation of a Backend into an
// Envoy Cluster: initialization, the backend plugin, DNS lookup family, client-independent backend
// policies (ProcessBackend), default locality config, and the gateway backend client certificate.
// Per-client concerns (destination rules, endpoint locality priorities, the inline CLA) are NOT
// applied here; they are layered on by ApplyPerClientOverlay. Returns nil only when the Backend is
// unsupported (no translator / no plugin), mirroring the old nil-cluster contract.
func (t *BackendTranslator) TranslateBackendBase(
	ctx context.Context,
	backend *ir.BackendObjectIR,
) *BackendBaseCluster {
	gk := backend.GetGroupKind()
	process, ok := t.ContributedBackends[gk]
	if !ok {
		return nil
	}
	if process.InitEnvoyBackend == nil {
		return nil
	}

	// Check for pre-existing errors in the Backend IR before starting translation.
	// Exit translation early if we have errors
	if backend.Errors != nil {
		logger.Error("backend has pre-existing errors", "backend", backend.GetName(), "errors", backend.Errors)
		return newBlackholeBase(backend, errors.Join(backend.Errors...))
	}

	// Initialize the cluster with minimal configuration
	out := initializeCluster(backend)
	inlineEps := process.InitEnvoyBackend(ctx, *backend, out)
	processDnsLookupFamily(out, t.CommonCols)

	// Apply client-independent backend policies to the computed cluster.
	if err := t.runBaseBackendPolicies(ctx, backend, out); err != nil {
		logger.Error("failed to apply policies to cluster", "cluster", out.GetName(), "error", err)
		return newBlackholeBase(backend, err)
	}
	defaultLocalityConfig(out)
	if err := applyGatewayBackendClientCertificate(out, backend); err != nil {
		logger.Error("failed to apply gateway backend client certificate", "cluster", out.GetName(), "error", err)
		return newBlackholeBase(backend, err)
	}

	return &BackendBaseCluster{
		Name:      backend.ResourceName(),
		Cluster:   out,
		Version:   utils.HashProto(out),
		InlineEps: inlineEps,
	}
}

// ApplyPerClientOverlay layers the per-client concerns onto a copy of the base cluster: the
// PerClientProcessBackend hooks (e.g. Istio destination rules, waypoint), endpoint plugins, and the
// inline ClusterLoadAssignment built from the client's locality. The base proto is never mutated.
// In strict mode the final per-client cluster is validated (the caching validator deduplicates
// identical content across clients).
func (t *BackendTranslator) ApplyPerClientOverlay(
	ctx context.Context,
	kctx krt.HandlerContext,
	ucc ir.UniquelyConnectedClient,
	backend *ir.BackendObjectIR,
	base *BackendBaseCluster,
) (*envoyclusterv3.Cluster, error) {
	// Fast path: when nothing varies per client for this cluster and we are not validating,
	// reuse the shared base proto directly (it is immutable downstream). This is the common case
	// for EDS clusters when no per-client plugin (destination rule, waypoint) matches this pair --
	// including every cluster when Istio integration is disabled.
	if t.Mode != apisettings.ValidationStrict && base.InlineEps == nil && !t.anyPerClientBackendApplies(kctx, ctx, ucc, backend) {
		return base.Cluster, nil
	}

	out, ok := proto.Clone(base.Cluster).(*envoyclusterv3.Cluster)
	if !ok {
		return buildBlackholeCluster(backend), errors.New("failed to clone base cluster")
	}
	t.runPerClientPolicies(kctx, ctx, ucc, backend, base.InlineEps, out)

	// In strict mode, validate the final per-client cluster configuration using Envoy.
	if t.Mode == apisettings.ValidationStrict && t.Validator != nil {
		if err := t.validateClusterConfig(ctx, out); err != nil {
			logger.Error("cluster failed xDS validation in strict mode", "cluster", out.GetName(), "error", err)
			return buildBlackholeCluster(backend), err
		}
	}

	return out, nil
}

func newBlackholeBase(backend *ir.BackendObjectIR, err error) *BackendBaseCluster {
	bh := buildBlackholeCluster(backend)
	return &BackendBaseCluster{
		Name:    backend.ResourceName(),
		Cluster: bh,
		Version: utils.HashProto(bh),
		Err:     err,
	}
}

// anyPerClientBackendApplies reports whether any contributed policy plugin would modify the cluster
// for this (backend, client) pair. A plugin that performs per-client backend processing but does not
// provide a PerClientProcessBackendApplies predicate is conservatively assumed to always apply. When
// this returns false (and there are no inline endpoints), the per-client overlay is a no-op and the
// base cluster can be reused as-is, avoiding a per-pair clone.
func (t *BackendTranslator) anyPerClientBackendApplies(
	kctx krt.HandlerContext,
	ctx context.Context,
	ucc ir.UniquelyConnectedClient,
	backend *ir.BackendObjectIR,
) bool {
	for _, p := range t.ContributedPolicies {
		if p.PerClientProcessBackend == nil {
			continue
		}
		if p.PerClientProcessBackendApplies == nil {
			// Plugin may modify the cluster; we cannot safely skip the overlay.
			return true
		}
		if p.PerClientProcessBackendApplies(kctx, ctx, ucc, *backend) {
			return true
		}
	}
	return false
}

// defaultLocalityConfig keeps traffic evenly distributed across zones for clusters
// that did not opt into a locality-aware LB mode. The proxy bootstrap always sets
// cluster_manager.local_cluster_name, and once the gateway fleet spans multiple
// zones Envoy's implicit zone-aware defaults (routing_enabled 100%,
// min_cluster_size 6) would otherwise engage with no policy configured.
func defaultLocalityConfig(c *envoyclusterv3.Cluster) {
	if c.GetLoadBalancingPolicy() != nil {
		// Typed load balancing policies carry their own locality_lb_config and
		// ignore common_lb_config.locality_config_specifier (see the
		// backendconfigpolicy plugin's buildTypedLocalityLbConfig).
		return
	}
	if c.GetCommonLbConfig().GetLocalityConfigSpecifier() != nil {
		// A policy plugin already chose a locality mode.
		return
	}
	if c.GetEdsClusterConfig() == nil {
		// Only kgateway-managed EDS clusters are guaranteed to carry locality
		// load-balancing weights on their CLAs; leave plugin-provided inline
		// clusters untouched.
		return
	}
	if c.CommonLbConfig == nil {
		c.CommonLbConfig = &envoyclusterv3.Cluster_CommonLbConfig{}
	}
	c.CommonLbConfig.LocalityConfigSpecifier = &envoyclusterv3.Cluster_CommonLbConfig_LocalityWeightedLbConfig_{
		LocalityWeightedLbConfig: &envoyclusterv3.Cluster_CommonLbConfig_LocalityWeightedLbConfig{},
	}
}

// runBaseBackendPolicies applies the client-independent backend policies (ProcessBackend) to the
// cluster. It is run once per Backend as part of base translation.
func (t *BackendTranslator) runBaseBackendPolicies(
	ctx context.Context,
	backend *ir.BackendObjectIR,
	out *envoyclusterv3.Cluster,
) error {
	var errs []error
	for gk, policyPlugin := range t.ContributedPolicies {
		// if the policy plugin has no ProcessBackend function, skip it
		if policyPlugin.ProcessBackend == nil {
			continue
		}
		policies := backend.AttachedPolicies.Policies[gk]
		if policyPlugin.MergePolicies != nil && len(policies) > 0 {
			policies = []ir.PolicyAtt{policyPlugin.MergePolicies(policies)}
		}
		// apply plugins to the backend. we want to skip applying the plugin if the
		// attached IR encountered any errors during construction.
		for _, polAttachment := range policies {
			if len(polAttachment.Errors) > 0 {
				logger.Error("policy has errors", "gk", gk, "errors", polAttachment.Errors, "policyRef", polAttachment.PolicyRef)
				errs = append(errs, polAttachment.Errors...)
				continue
			}
			policyPlugin.ProcessBackend(ctx, polAttachment.PolicyIr, *backend, out)
		}
	}
	return errors.Join(errs...)
}

// runPerClientPolicies applies the client-specific overlay to a (cloned) cluster: the
// PerClientProcessBackend hooks, endpoint plugins, and the inline ClusterLoadAssignment derived from
// the client's locality. None of the per-client hooks return errors, so this does not.
func (t *BackendTranslator) runPerClientPolicies(
	kctx krt.HandlerContext,
	ctx context.Context,
	ucc ir.UniquelyConnectedClient,
	backend *ir.BackendObjectIR,
	inlineEps *ir.EndpointsForBackend,
	out *envoyclusterv3.Cluster,
) {
	// if the backend was initialized with inlineEps then we
	// need an EndpointsInputs to run plugins against
	var endpointInputs *endpoints.EndpointsInputs
	if inlineEps != nil {
		endpointInputs = &endpoints.EndpointsInputs{
			EndpointsForBackend: *inlineEps,
		}
		endpointInputs.EndpointsForBackend.AttachedPolicies = backend.AttachedPolicies
	}

	for _, policyPlugin := range t.ContributedPolicies {
		if policyPlugin.PerClientProcessBackend != nil {
			policyPlugin.PerClientProcessBackend(kctx, ctx, ucc, *backend, out)
		}
	}

	if endpointInputs != nil {
		for _, processEndpoints := range t.orderedEndpointPlugins() {
			processEndpoints(kctx, ctx, ucc, endpointInputs)
		}
	}

	// for clusters that want a CLA _and_ initialized with inlineEps, build the CLA.
	// never overwrite the CLA that was already initialized (potentially within a plugin).
	if out.GetLoadAssignment() == nil && endpointInputs != nil && clusterSupportsInlineCLA(out) {
		out.LoadAssignment = endpoints.PrioritizeEndpoints(
			logger,
			ucc,
			*endpointInputs,
		)
	}
}

func (t *BackendTranslator) orderedEndpointPlugins() []sdk.EndpointPlugin {
	if t.EndpointPlugins != nil {
		return t.EndpointPlugins
	}
	return OrderedEndpointPlugins(t.ContributedPolicies)
}

// validateClusterConfig validates an individual cluster configuration using Envoy's
// validation. This catches configuration errors that would cause Envoy data plane NACKs,
// such as invalid cipher suites, invalid TLS parameters, etc.
func (t *BackendTranslator) validateClusterConfig(ctx context.Context, cluster *envoyclusterv3.Cluster) error {
	builder := bootstrap.New()
	builder.AddCluster(cluster)
	bootstrap, err := builder.Build()
	if err != nil {
		return err
	}
	if err := t.Validator.Validate(validator.WithValidationCaller(ctx, validator.CallerBackend), bootstrap); err != nil {
		return err
	}
	return nil
}

var inlineCLAClusterTypes = sets.New(
	envoyclusterv3.Cluster_STATIC,
	envoyclusterv3.Cluster_STRICT_DNS,
	envoyclusterv3.Cluster_LOGICAL_DNS,
)

func clusterSupportsInlineCLA(cluster *envoyclusterv3.Cluster) bool {
	switch cdt := cluster.GetClusterDiscoveryType().(type) {
	case *envoyclusterv3.Cluster_ClusterType:
		return cdt.ClusterType.GetName() == dnsClusterExtensionName
	case *envoyclusterv3.Cluster_Type:
		return inlineCLAClusterTypes.Has(cdt.Type)
	default:
		return false
	}
}

var h2Options = func() *anypb.Any {
	http2ProtocolOptions := &envoy_upstreams_v3.HttpProtocolOptions{
		UpstreamProtocolOptions: &envoy_upstreams_v3.HttpProtocolOptions_ExplicitHttpConfig_{
			ExplicitHttpConfig: &envoy_upstreams_v3.HttpProtocolOptions_ExplicitHttpConfig{
				ProtocolConfig: &envoy_upstreams_v3.HttpProtocolOptions_ExplicitHttpConfig_Http2ProtocolOptions{
					Http2ProtocolOptions: &envoycorev3.Http2ProtocolOptions{},
				},
			},
		},
	}

	a, err := utils.MessageToAny(http2ProtocolOptions)
	if err != nil {
		// should never happen - all values are known ahead of time.
		panic(err)
	}
	return a
}()

// processDnsLookupFamily modifies clusters that use DNS-based discovery in the following way:
// 1. explicitly default to 'V4_PREFERRED' (as opposed to the envoy default of effectively V6_PREFERRED)
// 2. override to value defined in kgateway global setting if present
func processDnsLookupFamily(out *envoyclusterv3.Cluster, cc *collections.CommonCollections) {
	lookupFamily := envoyclusterv3.Cluster_V4_PREFERRED
	if cc != nil {
		switch cc.Settings.DnsLookupFamily {
		case apisettings.DnsLookupFamilyV4Preferred:
			lookupFamily = envoyclusterv3.Cluster_V4_PREFERRED
		case apisettings.DnsLookupFamilyV4Only:
			lookupFamily = envoyclusterv3.Cluster_V4_ONLY
		case apisettings.DnsLookupFamilyV6Only:
			lookupFamily = envoyclusterv3.Cluster_V6_ONLY
		case apisettings.DnsLookupFamilyAuto:
			lookupFamily = envoyclusterv3.Cluster_AUTO
		case apisettings.DnsLookupFamilyAll:
			lookupFamily = envoyclusterv3.Cluster_ALL
		}
	}

	switch cdt := out.GetClusterDiscoveryType().(type) {
	case *envoyclusterv3.Cluster_ClusterType:
		if cdt.ClusterType.GetName() != dnsClusterExtensionName || cdt.ClusterType.GetTypedConfig() == nil {
			return
		}
		dnsCluster := &envoydnsv3.DnsCluster{}
		err := cdt.ClusterType.GetTypedConfig().UnmarshalTo(dnsCluster)
		if err != nil {
			logger.Error("failed to unpack dns cluster config", "cluster", out.GetName(), "error", err)
			return
		}
		dnsCluster.DnsLookupFamily = toExtensionDnsLookupFamily(lookupFamily)
		typedConfig, err := utils.MessageToAny(dnsCluster)
		if err != nil {
			logger.Error("failed to pack dns cluster config", "cluster", out.GetName(), "error", err)
			return
		}
		cdt.ClusterType.TypedConfig = typedConfig
	default:
		return
	}
}

func toExtensionDnsLookupFamily(family envoyclusterv3.Cluster_DnsLookupFamily) envoycommondnsv3.DnsLookupFamily {
	switch family {
	case envoyclusterv3.Cluster_AUTO:
		return envoycommondnsv3.DnsLookupFamily_AUTO
	case envoyclusterv3.Cluster_V6_ONLY:
		return envoycommondnsv3.DnsLookupFamily_V6_ONLY
	case envoyclusterv3.Cluster_V4_ONLY:
		return envoycommondnsv3.DnsLookupFamily_V4_ONLY
	case envoyclusterv3.Cluster_V4_PREFERRED:
		return envoycommondnsv3.DnsLookupFamily_V4_PREFERRED
	case envoyclusterv3.Cluster_ALL:
		return envoycommondnsv3.DnsLookupFamily_ALL
	default:
		return envoycommondnsv3.DnsLookupFamily_AUTO
	}
}

func translateAppProtocol(appProtocol ir.AppProtocol) map[string]*anypb.Any {
	// Avoid allocating an empty map for the common HTTP/1 case. Downstream
	// callers (utils/cluster.go, extensions2/pluginutils) lazily allocate the
	// map when they need to set a key.
	if appProtocol != ir.HTTP2AppProtocol {
		return nil
	}
	return map[string]*anypb.Any{
		"envoy.extensions.upstreams.http.v3.HttpProtocolOptions": cloneAny(h2Options),
	}
}

func cloneAny(msg *anypb.Any) *anypb.Any {
	if msg == nil {
		return nil
	}
	return &anypb.Any{
		TypeUrl: msg.TypeUrl,
		Value:   append([]byte(nil), msg.Value...),
	}
}

// initializeCluster creates a default envoy cluster with minimal configuration,
// that will then be augmented by various backend plugins
func initializeCluster(b *ir.BackendObjectIR) *envoyclusterv3.Cluster {
	out := &envoyclusterv3.Cluster{
		Name:                          b.ClusterName(),
		ConnectTimeout:                durationpb.New(clusterConnectionTimeout),
		TypedExtensionProtocolOptions: translateAppProtocol(b.AppProtocol),
		CommonLbConfig:                createCommonLbConfig(b),
	}
	return out
}

func buildBlackholeCluster(b *ir.BackendObjectIR) *envoyclusterv3.Cluster {
	out := &envoyclusterv3.Cluster{
		Name:     b.ClusterName(),
		Metadata: new(envoycorev3.Metadata),
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
			Type: envoyclusterv3.Cluster_STATIC,
		},
		LoadAssignment: &envoyendpointv3.ClusterLoadAssignment{
			ClusterName: b.ClusterName(),
			Endpoints:   []*envoyendpointv3.LocalityLbEndpoints{},
		},
	}
	return out
}

func createCommonLbConfig(b *ir.BackendObjectIR) *envoyclusterv3.Cluster_CommonLbConfig {
	if b.TrafficDistribution != wellknown.TrafficDistributionAny {
		return &envoyclusterv3.Cluster_CommonLbConfig{
			LocalityConfigSpecifier: &envoyclusterv3.Cluster_CommonLbConfig_LocalityWeightedLbConfig_{
				LocalityWeightedLbConfig: &envoyclusterv3.Cluster_CommonLbConfig_LocalityWeightedLbConfig{},
			},
		}
	}
	return nil
}

func applyGatewayBackendClientCertificate(out *envoyclusterv3.Cluster, backend *ir.BackendObjectIR) error {
	if backend == nil || backend.GatewayBackendClientCertificate == nil {
		return nil
	}

	certificate := backend.GatewayBackendClientCertificate.Certificate
	if ts, err := injectGatewayBackendClientCertificate(out.GetTransportSocket(), certificate); err != nil {
		return err
	} else if ts != nil {
		out.TransportSocket = ts
	}
	for _, match := range out.GetTransportSocketMatches() {
		ts, err := injectGatewayBackendClientCertificate(match.GetTransportSocket(), certificate)
		if err != nil {
			return err
		}
		if ts != nil {
			match.TransportSocket = ts
		}
	}
	return nil
}

// injectGatewayBackendClientCertificate returns a clone of transportSocket with the
// Gateway-scoped client cert/key set on its UpstreamTlsContext. Returns (nil, nil) when
// transportSocket is not a TLS socket so the caller can leave it untouched. The clone
// avoids aliasing transport-socket protos shared with other clusters by upstream plugins.
func injectGatewayBackendClientCertificate(
	transportSocket *envoycorev3.TransportSocket,
	certificate ir.TLSCertificate,
) (*envoycorev3.TransportSocket, error) {
	if transportSocket == nil || transportSocket.GetName() != envoywellknown.TransportSocketTls {
		return nil, nil
	}
	typedConfig := transportSocket.GetTypedConfig()
	if typedConfig == nil {
		return nil, nil
	}

	tlsContext := &envoytlsv3.UpstreamTlsContext{}
	if err := typedConfig.UnmarshalTo(tlsContext); err != nil {
		return nil, err
	}
	if tlsContext.CommonTlsContext == nil {
		tlsContext.CommonTlsContext = &envoytlsv3.CommonTlsContext{}
	}
	tlsContext.CommonTlsContext.TlsCertificates = []*envoytlsv3.TlsCertificate{{
		CertificateChain: pluginutils.InlineStringDataSource(string(certificate.CertChain)),
		PrivateKey:       pluginutils.InlineStringDataSource(string(certificate.PrivateKey)),
	}}
	tlsContext.CommonTlsContext.TlsCertificateSdsSecretConfigs = nil

	updatedTypedConfig, err := utils.MessageToAny(tlsContext)
	if err != nil {
		return nil, err
	}
	clone, ok := proto.Clone(transportSocket).(*envoycorev3.TransportSocket)
	if !ok {
		return nil, errors.New("failed to clone transport socket")
	}
	clone.ConfigType = &envoycorev3.TransportSocket_TypedConfig{TypedConfig: updatedTypedConfig}
	return clone, nil
}
