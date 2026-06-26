package irtranslator

// White-box benchmark (internal package) comparing the per-client cluster fan-out before and after
// the base+overlay refactor (Stage 1a).
//
//   Old: full translation per (backend, client) pair, in place — no base cache, no clone, no fast
//        path. This faithfully reconstructs the pre-refactor TranslateBackend body.
//   New: base translated once per backend, then a cheap per-client overlay applied per pair.
//
// Swept across Istio off/on (whether a per-client backend plugin is registered) and light/heavy
// backends (how expensive the client-independent base translation is). The delta scales linearly
// with the number of (backend, client) pairs.

import (
	"context"
	"fmt"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	sdk "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

const (
	benchNumBackends = 200
	benchNumClients  = 20
)

var benchGK = schema.GroupKind{Group: "bench", Kind: "Backend"}

// benchSink prevents the compiler from optimizing away the translated clusters.
var benchSink *envoyclusterv3.Cluster

func benchInit(heavy bool) func(context.Context, ir.BackendObjectIR, *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
	return func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
		out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
		out.EdsClusterConfig = &envoyclusterv3.Cluster_EdsClusterConfig{
			EdsConfig: &envoycorev3.ConfigSource{
				ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{Ads: &envoycorev3.AggregatedConfigSource{}},
			},
		}
		if heavy {
			// Simulate a TLS backend: a proto marshal as part of base translation.
			if tlsAny, err := utils.MessageToAny(&envoytlsv3.UpstreamTlsContext{Sni: out.GetName()}); err == nil {
				out.TransportSocket = &envoycorev3.TransportSocket{
					Name:       "envoy.transport_sockets.tls",
					ConfigType: &envoycorev3.TransportSocket_TypedConfig{TypedConfig: tlsAny},
				}
			}
		}
		return nil
	}
}

func benchTranslator(istioOn, heavy bool) *BackendTranslator {
	t := &BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			benchGK: {InitEnvoyBackend: benchInit(heavy)},
		},
		ContributedPolicies: map[schema.GroupKind]sdk.PolicyPlugin{},
		// Mode left as zero value (non-strict): validation is skipped, matching the default deployment.
	}
	if istioOn {
		// A destination-rule-like per-client plugin: it only modifies clients whose label matches
		// (a "destination rule" is present), and advertises that via the applies predicate so the
		// translator can skip the per-client clone for non-matching pairs.
		t.ContributedPolicies[benchGK] = sdk.PolicyPlugin{
			PerClientProcessBackend: func(_ krt.HandlerContext, _ context.Context, ucc ir.UniquelyConnectedClient, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) {
				if ucc.Labels["bench-mutate"] == "yes" {
					out.OutlierDetection = &envoyclusterv3.OutlierDetection{}
				}
			},
			PerClientProcessBackendApplies: func(_ krt.HandlerContext, _ context.Context, ucc ir.UniquelyConnectedClient, _ ir.BackendObjectIR) bool {
				return ucc.Labels["bench-mutate"] == "yes"
			},
		}
	}
	return t
}

func benchBackends(n int) []*ir.BackendObjectIR {
	out := make([]*ir.BackendObjectIR, n)
	for i := range n {
		b := ir.NewBackendObjectIR(ir.ObjectSource{
			Group:     "bench",
			Kind:      "Backend",
			Namespace: "default",
			Name:      fmt.Sprintf("b%d", i),
		}, 8080, "", "")
		out[i] = &b
	}
	return out
}

func benchUCCs(m int) []ir.UniquelyConnectedClient {
	out := make([]ir.UniquelyConnectedClient, m)
	for i := range m {
		// Model sparse destination-rule matching: only ~1 in 10 clients is targeted by a DR.
		mutate := "no"
		if i%10 == 0 {
			mutate = "yes"
		}
		out[i] = ir.NewUniquelyConnectedClient(
			fmt.Sprintf("role%d", i),
			"default",
			map[string]string{"bench-mutate": mutate, "idx": fmt.Sprintf("%d", i)},
			ir.PodLocality{Region: "r", Zone: fmt.Sprintf("z%d", i%3)},
		)
	}
	return out
}

// legacyTranslate reconstructs the pre-refactor per-pair translation: a full, in-place translation
// for every (backend, client), with no base caching, no clone, and no fast path.
func (t *BackendTranslator) legacyTranslate(ctx context.Context, kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient, backend *ir.BackendObjectIR) *envoyclusterv3.Cluster {
	process := t.ContributedBackends[backend.GetGroupKind()]
	out := initializeCluster(backend)
	inlineEps := process.InitEnvoyBackend(ctx, *backend, out)
	processDnsLookupFamily(out, t.CommonCols)
	_ = t.runBaseBackendPolicies(ctx, backend, out)
	t.runPerClientPolicies(kctx, ctx, ucc, backend, inlineEps, out)
	defaultLocalityConfig(out)
	_ = applyGatewayBackendClientCertificate(out, backend)
	return out
}

func benchmarkPerClientClusters(b *testing.B, istioOn, heavy bool) {
	t := benchTranslator(istioOn, heavy)
	backends := benchBackends(benchNumBackends)
	uccs := benchUCCs(benchNumClients)
	ctx := context.Background()
	var kctx krt.TestingDummyContext

	b.Run("Old", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			for _, backend := range backends {
				for _, ucc := range uccs {
					benchSink = t.legacyTranslate(ctx, kctx, ucc, backend)
				}
			}
		}
	})

	b.Run("New", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			bases := make([]*BackendBaseCluster, len(backends))
			for j, backend := range backends {
				bases[j] = t.TranslateBackendBase(ctx, backend)
			}
			for j, backend := range backends {
				for _, ucc := range uccs {
					benchSink, _ = t.ApplyPerClientOverlay(ctx, kctx, ucc, backend, bases[j])
				}
			}
		}
	})
}

func BenchmarkPerClientClusters(b *testing.B) {
	for _, istioOn := range []bool{false, true} {
		for _, heavy := range []bool{false, true} {
			b.Run(fmt.Sprintf("istio=%v/heavy=%v", istioOn, heavy), func(b *testing.B) {
				benchmarkPerClientClusters(b, istioOn, heavy)
			})
		}
	}
}
