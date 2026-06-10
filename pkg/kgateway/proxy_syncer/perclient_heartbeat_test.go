package proxy_syncer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// These tests cover the per-client recompute heartbeat (#14184): the safety valve
// that bounds how long a deferred snapshot can stay deferred. They verify the two
// properties the design relies on:
//   1. firing the heartbeat actually re-runs the per-client translation (so it can
//      heal a client whose rows were left stale/empty by a missed recompute edge);
//   2. when inputs are unchanged, the re-run is deterministic, so KRT suppresses the
//      output and a healthy fleet sees no xDS churn from the heartbeat.

func heartbeatTestTranslator(calls *atomic.Int64) *irtranslator.BackendTranslator {
	return &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			{Group: "", Kind: "Service"}: {
				InitEnvoyBackend: func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					if calls != nil {
						calls.Add(1)
					}
					return nil
				},
			},
		},
	}
}

func heartbeatTestBackend(name string) *ir.BackendObjectIR {
	b := ir.NewBackendObjectIR(ir.ObjectSource{
		Group:     "",
		Kind:      "Service",
		Namespace: "default",
		Name:      name,
	}, 443, "")
	return &b
}

func heartbeatTestClient(role string) ir.UniqlyConnectedClient {
	return ir.NewUniqlyConnectedClient(role, "", nil, ir.PodLocality{})
}

func clusterVersionsForClient(c PerClientEnvoyClusters, ucc ir.UniqlyConnectedClient) map[string]uint64 {
	out := map[string]uint64{}
	for _, fc := range c.FetchClustersForClient(krt.TestingDummyContext{}, ucc) {
		out[fc.Name] = fc.ClusterVersion
	}
	return out
}

func newHeartbeatFixture(t *testing.T, calls *atomic.Int64) (
	*krt.RecomputeTrigger,
	ir.UniqlyConnectedClient,
	PerClientEnvoyClusters,
) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	ucc := heartbeatTestClient("role-a")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{ucc}, krtopts.ToOptions("UniqueClients")...)
	finalBackends := krt.NewStaticCollection(nil,
		[]*ir.BackendObjectIR{heartbeatTestBackend("b1"), heartbeatTestBackend("b2")},
		krtopts.ToOptions("FinalBackends")...)
	heartbeat := krt.NewRecomputeTrigger(true, krtopts.ToOptions("PerClientHeartbeat")...)

	clusters := NewPerClientEnvoyClusters(ctx, krtopts, heartbeatTestTranslator(calls), finalBackends, uccs, heartbeat)
	return heartbeat, ucc, clusters
}

// Firing the heartbeat must reach the per-client manycollection and re-run
// translation -- that is the mechanism by which it heals a stranded client.
func TestPerClientHeartbeat_RerunsPerClientTranslation(t *testing.T) {
	var calls atomic.Int64
	heartbeat, ucc, clusters := newHeartbeatFixture(t, &calls)

	require.Eventually(t, func() bool {
		return len(clusters.FetchClustersForClient(krt.TestingDummyContext{}, ucc)) == 2
	}, 5*time.Second, 10*time.Millisecond, "initial per-client clusters never materialized")

	before := calls.Load()
	require.NotZero(t, before, "translation should have run during initial build")

	heartbeat.TriggerRecomputation()

	require.Eventually(t, func() bool {
		return calls.Load() > before
	}, 5*time.Second, 10*time.Millisecond, "heartbeat did not re-run per-client translation")

	// Recompute is non-destructive: the client still has all its clusters.
	require.Len(t, clusters.FetchClustersForClient(krt.TestingDummyContext{}, ucc), 2)
}

// When inputs are unchanged, repeated heartbeats must produce identical cluster
// versions. Identical versions hash-equal under uccWithCluster.Equals, so KRT
// suppresses the output and the heartbeat causes no xDS churn. A failure here means
// translation is nondeterministic and the heartbeat would rewrite snapshots every
// interval -- the determinism risk called out in the design.
func TestPerClientHeartbeat_NoChurnWhenStable(t *testing.T) {
	var calls atomic.Int64
	heartbeat, ucc, clusters := newHeartbeatFixture(t, &calls)

	require.Eventually(t, func() bool {
		return len(clusters.FetchClustersForClient(krt.TestingDummyContext{}, ucc)) == 2
	}, 5*time.Second, 10*time.Millisecond, "initial per-client clusters never materialized")

	want := clusterVersionsForClient(clusters, ucc)
	require.Len(t, want, 2)

	for range 5 {
		// Wait until the triggered recompute has actually re-run translation
		// before asserting; a sleep alone could pass vacuously against the
		// pre-recompute cached rows on a slow machine.
		before := calls.Load()
		heartbeat.TriggerRecomputation()
		require.Eventually(t, func() bool {
			return calls.Load() > before
		}, 5*time.Second, 10*time.Millisecond, "heartbeat recompute never ran")
		require.Equal(t, want, clusterVersionsForClient(clusters, ucc),
			"heartbeat changed per-client cluster versions with unchanged inputs; "+
				"nondeterministic translation would churn xDS every heartbeat")
	}
}
