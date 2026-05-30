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
	CommonCols          *collections.CommonCollections
	Validator           validator.Validator
	Mode                apisettings.ValidationMode
}

// BaseCluster is the UCC-invariant result of translating a backend into an Envoy
// cluster. The Cluster field is shared across all UCCs that target this backend —
// callers MUST NOT mutate it. Per-client mutations layer on top via ApplyPerClient,
// which clones before mutating.
//
// When Error is non-nil, Cluster is a blackhole cluster and all UCCs targeting this
// backend should treat it as errored. There is no per-client variation when the base
// is errored, so ApplyPerClient is a no-op in that case.
type BaseCluster struct {
	Cluster *envoyclusterv3.Cluster
	// EndpointInputs carries inline endpoints from InitEnvoyBackend, if the backend
	// produced any. Used by per-client overlay to build the inline CLA and to drive
	// PerClientProcessEndpoints hooks.
	EndpointInputs *endpoints.EndpointsInputs
	// SupportsInlineCLA is true when the cluster type accepts an inline
	// ClusterLoadAssignment (STATIC, STRICT_DNS, LOGICAL_DNS, or the DNS extension).
	// When this is true AND EndpointInputs is non-nil AND Cluster.LoadAssignment is
	// nil, the per-client overlay must always build a CLA — the CLA varies per UCC
	// via PrioritizeEndpoints and so cannot live on the shared base.
	SupportsInlineCLA bool
	Error             error
}

// TranslateBackend translates a BackendObjectIR to an Envoy Cluster. If we encounter any
// errors during translation, a blackhole cluster is returned along with the error. The error
// return value is what matters as consumers (pkg/kgateway/proxy_syncer/perclient.go) will
// drop errored clusters from the xDS snapshot and track them separately for status reporting.
// The blackhole cluster itself is not sent to Envoy but provides a consistent return structure.
//
// This is a convenience wrapper that combines TranslateBackendBase and ApplyPerClient.
// Hot-path callers should invoke them separately so the base cluster can be shared
// across UCCs.
func (t *BackendTranslator) TranslateBackend(
	ctx context.Context,
	kctx krt.HandlerContext,
	ucc ir.UniquelyConnectedClient,
	backend *ir.BackendObjectIR,
) (*envoyclusterv3.Cluster, error) {
	base := t.TranslateBackendBase(ctx, backend)
	if base == nil {
		return nil, errors.New("no backend translator found for " + backend.GetGroupKind().String())
	}
	if base.Error != nil {
		return base.Cluster, base.Error
	}
	perClient, err := t.ApplyPerClient(kctx, ctx, ucc, backend, base)
	if err != nil {
		if perClient != nil {
			return perClient, err
		}
		return buildBlackholeCluster(backend), err
	}
	if perClient != nil {
		return perClient, nil
	}
	return base.Cluster, nil
}

// TranslateBackendBase performs the UCC-invariant phase of cluster translation. The
// returned BaseCluster can be shared across all UCCs targeting this backend.
//
// Returns nil only when the backend GK has no contributed translator at all — a
// configuration error that prevents producing even a blackhole cluster.
func (t *BackendTranslator) TranslateBackendBase(
	ctx context.Context,
	backend *ir.BackendObjectIR,
) *BaseCluster {
	gk := backend.GetGroupKind()
	process, ok := t.ContributedBackends[gk]
	if !ok || process.InitEnvoyBackend == nil {
		return nil
	}

	if backend.Errors != nil {
		logger.Error("backend has pre-existing errors", "backend", backend.GetName(), "errors", backend.Errors)
		return &BaseCluster{
			Cluster: buildBlackholeCluster(backend),
			Error:   errors.Join(backend.Errors...),
		}
	}

	out := initializeCluster(backend)
	inlineEps := process.InitEnvoyBackend(ctx, *backend, out)
	processDnsLookupFamily(out, t.CommonCols)

	// Apply non-per-client policies. Plugins with PerClientClusterOverlay run
	// later in ApplyPerClient; plugins with ProcessBackend are UCC-invariant
	// and run here once.
	if err := t.applyBasePolicies(ctx, backend, out); err != nil {
		logger.Error("failed to apply policies to cluster", "cluster", out.GetName(), "error", err)
		return &BaseCluster{Cluster: buildBlackholeCluster(backend), Error: err}
	}
	if err := applyGatewayBackendClientCertificate(out, backend); err != nil {
		logger.Error("failed to apply gateway backend client certificate", "cluster", out.GetName(), "error", err)
		return &BaseCluster{Cluster: buildBlackholeCluster(backend), Error: err}
	}

	if t.Mode == apisettings.ValidationStrict && t.Validator != nil {
		if err := t.validateClusterConfig(ctx, out); err != nil {
			logger.Error("cluster failed xDS validation in strict mode", "cluster", out.GetName(), "error", err)
			return &BaseCluster{Cluster: buildBlackholeCluster(backend), Error: err}
		}
	}

	var endpointInputs *endpoints.EndpointsInputs
	if inlineEps != nil {
		endpointInputs = &endpoints.EndpointsInputs{EndpointsForBackend: *inlineEps}
	}

	return &BaseCluster{
		Cluster:           out,
		EndpointInputs:    endpointInputs,
		SupportsInlineCLA: clusterSupportsInlineCLA(out),
	}
}

// ApplyPerClient computes per-client cluster mutations on top of base. Returns nil
// when the (ucc, backend) pair needs no per-client processing — callers must then
// reference base.Cluster directly. When non-nil, the returned cluster is a freshly
// allocated proto that callers may retain independently of base.Cluster.
//
// When base.Error is non-nil, this is a no-op (returns nil, nil).
func (t *BackendTranslator) ApplyPerClient(
	kctx krt.HandlerContext,
	ctx context.Context,
	ucc ir.UniquelyConnectedClient,
	backend *ir.BackendObjectIR,
	base *BaseCluster,
) (*envoyclusterv3.Cluster, error) {
	if base == nil || base.Error != nil {
		return nil, nil
	}

	// Gather overlays. Each plugin must self-determine applicability and return
	// nil in the common case; this keeps the per-client cluster collection sparse.
	var overlays []*sdk.ClusterOverlay
	for _, policyPlugin := range t.ContributedPolicies {
		if policyPlugin.PerClientClusterOverlay == nil {
			continue
		}
		if ov := policyPlugin.PerClientClusterOverlay(kctx, ctx, ucc, *backend); ov != nil {
			overlays = append(overlays, ov)
		}
	}

	// Determine whether we need to build an inline CLA. Even with no overlays,
	// inline-CLA clusters need a per-client CLA because PrioritizeEndpoints is
	// UCC-dependent (locality, labels). This depends only on the base, not the UCC.
	needsInlineCLA := base.SupportsInlineCLA && base.EndpointInputs != nil && base.Cluster.GetLoadAssignment() == nil

	if len(overlays) == 0 && !needsInlineCLA {
		return nil, nil
	}

	// Materialize a per-client cluster. Clone is required because the base proto
	// is shared across UCCs and must remain unmodified.
	out, ok := proto.Clone(base.Cluster).(*envoyclusterv3.Cluster)
	if !ok {
		return nil, errors.New("failed to clone base cluster")
	}

	for _, ov := range overlays {
		if ov.Mutate != nil {
			ov.Mutate(out)
		}
	}

	if needsInlineCLA {
		// Gather endpoint plugins lazily — only inline-CLA clusters consume them,
		// so the common EDS path (which returns early above) never pays for this.
		// PerClientProcessEndpoints may modify EndpointInputs (e.g. destrule
		// PriorityInfo). Work on a copy so we don't mutate the shared
		// EndpointInputs held by base.
		epIn := *base.EndpointInputs
		for _, policyPlugin := range t.ContributedPolicies {
			if policyPlugin.PerClientProcessEndpoints != nil {
				policyPlugin.PerClientProcessEndpoints(kctx, ctx, ucc, &epIn)
			}
		}
		// Re-check LoadAssignment: an overlay may have set it.
		if out.GetLoadAssignment() == nil {
			out.LoadAssignment = endpoints.PrioritizeEndpoints(logger, ucc, epIn)
		}
	}

	// Strict-mode validation on the post-overlay cluster. The base was already
	// validated in TranslateBackendBase, but overlays (destrule, waypoint, …)
	// run here and can produce invalid configs that Envoy would NACK at runtime.
	// We must reject them at translation time when the user opted into strict
	// validation. Returning the blackhole keeps the same shape as base errors,
	// so the snapshot consumer's erroredClusters tracking still works.
	if t.Mode == apisettings.ValidationStrict && t.Validator != nil {
		if err := t.validateClusterConfig(ctx, out); err != nil {
			logger.Error("per-client cluster failed xDS validation in strict mode",
				"cluster", out.GetName(), "ucc", ucc.ResourceName(), "error", err)
			return buildBlackholeCluster(backend), err
		}
	}

	return out, nil
}

// applyBasePolicies runs only the UCC-invariant ProcessBackend hooks. Per-client
// hooks (PerClientClusterOverlay, PerClientProcessEndpoints) are handled by
// ApplyPerClient.
func (t *BackendTranslator) applyBasePolicies(
	ctx context.Context,
	backend *ir.BackendObjectIR,
	out *envoyclusterv3.Cluster,
) error {
	var errs []error
	for gk, policyPlugin := range t.ContributedPolicies {
		if policyPlugin.ProcessBackend == nil {
			continue
		}
		policies := backend.AttachedPolicies.Policies[gk]
		if policyPlugin.MergePolicies != nil && len(policies) > 0 {
			policies = []ir.PolicyAtt{policyPlugin.MergePolicies(policies)}
		}
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
	if err := t.Validator.Validate(ctx, bootstrap); err != nil {
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
