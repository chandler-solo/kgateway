package proxy_syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	envoybootstrapv3 "github.com/envoyproxy/go-control-plane/envoy/config/bootstrap/v3"
	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	apisettings "github.com/kgateway-dev/kgateway/v2/api/settings"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	"github.com/kgateway-dev/kgateway/v2/pkg/validator"
)

// Connect-event drain benchmark for the per-client clusters collection — the
// mechanism behind the field outages (#14184):
// one client connecting re-runs every backend's transform, each translating
// for every client, each translation paying an external Envoy validation in
// strict mode, all serialized on the collection's single queue goroutine.
//
// The strict validation cost is stubbed (benchValidationLatency per uncached
// call; the real envoy fork costs 200-500ms, so measured drains scale up
// accordingly). Two scenarios bracket the fix:
//
//   - distinct_classes/binary_validator: every client in its own translation
//     class with fork-per-call validation — the pre-fix worst case.
//   - shared_class/cached_validator: clients share (Namespace, Labels) — the
//     normal Deployment-replica shape — with the content-hash verdict cache
//     (the new default). Per-invocation dedup translates once per class and
//     the cache absorbs repeats across backends and recomputes.
//
// The shared/cached drain is also the cost of one demand-driven heartbeat
// tick: a tick re-runs exactly the same per-backend transforms a connect
// event does.

const (
	benchBackends          = 200
	benchClients           = 8
	benchValidationLatency = 2 * time.Millisecond
)

type benchLatencyValidator struct{ latency time.Duration }

func (f *benchLatencyValidator) Validate(_ context.Context, _ *envoybootstrapv3.Bootstrap) error {
	time.Sleep(f.latency)
	return nil
}

func benchTranslator(v validator.Validator) *irtranslator.BackendTranslator {
	return &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			{Group: "", Kind: "Service"}: {
				InitEnvoyBackend: func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
		},
		Validator: v,
		Mode:      apisettings.ValidationStrict,
	}
}

func benchClientsSet(shareClass bool) []ir.UniqlyConnectedClient {
	out := make([]ir.UniqlyConnectedClient, 0, benchClients)
	for i := range benchClients {
		labels := map[string]string{"app": "gw"}
		if !shareClass {
			labels["pod"] = fmt.Sprintf("p%d", i)
		}
		out = append(out, ir.NewUniqlyConnectedClient(fmt.Sprintf("role-%d", i), "ns", labels, ir.PodLocality{}))
	}
	return out
}

func benchProbe(shareClass bool) ir.UniqlyConnectedClient {
	labels := map[string]string{"app": "gw"}
	if !shareClass {
		labels["pod"] = "probe"
	}
	return ir.NewUniqlyConnectedClient("role-probe", "ns", labels, ir.PodLocality{})
}

func benchDrainScenario(b *testing.B, shareClass bool, v validator.Validator) {
	b.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	clients := benchClientsSet(shareClass)
	uccs := krt.NewStaticCollection(nil, clients, krtopts.ToOptions("UniqueClients")...)
	backends := make([]*ir.BackendObjectIR, 0, benchBackends)
	for i := range benchBackends {
		backends = append(backends, dedupTestBackend(fmt.Sprintf("b%d", i)))
	}
	finalBackends := krt.NewStaticCollection(nil, backends, krtopts.ToOptions("FinalBackends")...)
	clusters := NewPerClientEnvoyClusters(ctx, krtopts, benchTranslator(v), finalBackends, uccs)

	// Wait for the initial build to drain before timing connect events.
	waitDrained := func(ucc ir.UniqlyConnectedClient) {
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			if len(clusters.FetchClustersForClient(krt.TestingDummyContext{}, ucc)) == benchBackends {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		b.Fatalf("client %s never drained", ucc.ResourceName())
	}
	waitDrained(clients[len(clients)-1])

	probe := benchProbe(shareClass)
	b.ResetTimer()
	for b.Loop() {
		// One connect event: the probe joins, every backend transform re-runs.
		uccs.UpdateObject(probe)
		waitDrained(probe)
		b.StopTimer()
		uccs.DeleteObject(probe.ResourceName())
		// Wait for the delete fan-out to drain so iterations don't overlap.
		deadline := time.Now().Add(10 * time.Minute)
		for time.Now().Before(deadline) {
			if len(clusters.FetchClustersForClient(krt.TestingDummyContext{}, probe)) == 0 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		b.StartTimer()
	}
}

// Pre-fix worst case: distinct classes (no dedup possible) and fork-per-call
// validation. Expected per connect event: backends x clients x latency.
func BenchmarkPerClientConnectDrain_DistinctClasses_BinaryValidator(b *testing.B) {
	benchDrainScenario(b, false, &benchLatencyValidator{latency: benchValidationLatency})
}

// Post-fix common case: replicas share a class (dedup translates once per
// backend) and the content-hash cache absorbs repeated verdicts. Expected per
// connect event: backends x (1 translation + ~0 validation).
func BenchmarkPerClientConnectDrain_SharedClass_CachedValidator(b *testing.B) {
	benchDrainScenario(b, true, validator.NewCaching(&benchLatencyValidator{latency: benchValidationLatency}, 0))
}

// Isolation: dedup alone (shared class, uncached fork-per-call validation).
func BenchmarkPerClientConnectDrain_SharedClass_BinaryValidator(b *testing.B) {
	benchDrainScenario(b, true, &benchLatencyValidator{latency: benchValidationLatency})
}

// Isolation: cache alone. This is the FIELD-REPRESENTATIVE case: augmented
// client labels include per-node keys (kubernetes.io/hostname, subzone), so
// real fleets have ~one translation class per node and the dedup rarely
// collapses anything — but the validator cache keys on the OUTPUT cluster
// content, which is identical across classes whenever no DestinationRule
// differentiates them, so the expensive envoy invocations still collapse.
func BenchmarkPerClientConnectDrain_DistinctClasses_CachedValidator(b *testing.B) {
	benchDrainScenario(b, false, validator.NewCaching(&benchLatencyValidator{latency: benchValidationLatency}, 0))
}
