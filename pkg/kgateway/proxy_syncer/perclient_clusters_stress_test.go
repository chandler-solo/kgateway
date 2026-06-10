package proxy_syncer

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// stressUccSource mirrors the production connected-client collection
// (krtcollections.callbacksCollection): an in-memory map mutated from outside KRT
// (there: xDS stream callbacks) and surfaced via NewManyFromNothing + a
// RecomputeTrigger. The per-client collections depend on this collection shape, so
// reproducing it faithfully here exercises the real propagation path rather than a
// StaticCollection (which serializes events). This is the path along which #14184
// (a connected client stranded with empty per-client config) would arise.
type stressUccSource struct {
	mu      sync.RWMutex
	clients map[string]ir.UniqlyConnectedClient
	trigger *krt.RecomputeTrigger
	// postReadDelay, when nonzero, is slept AFTER the transform has snapshotted
	// the client map and BEFORE it returns. This widens the window in which a
	// mutation + TriggerRecomputation land while a recompute holding a stale
	// read is still in flight — the suspected stranding window for #14184: if
	// the trigger event that arrived mid-run were coalesced into the in-flight
	// recompute instead of producing a follow-up one, the stale output would
	// stick (and, being hash-identical to the previous output when the read
	// predates the mutation, leave no trace).
	postReadDelay time.Duration
}

func newStressUccSource(krtopts krtutil.KrtOptions, postReadDelay time.Duration, initial []ir.UniqlyConnectedClient) (*stressUccSource, krt.Collection[ir.UniqlyConnectedClient]) {
	s := &stressUccSource{
		clients:       make(map[string]ir.UniqlyConnectedClient),
		trigger:       krt.NewRecomputeTrigger(true),
		postReadDelay: postReadDelay,
	}
	for _, c := range initial {
		s.clients[c.ResourceName()] = c
	}
	col := krt.NewManyFromNothing(func(ctx krt.HandlerContext) []ir.UniqlyConnectedClient {
		s.trigger.MarkDependant(ctx)
		s.mu.RLock()
		out := make([]ir.UniqlyConnectedClient, 0, len(s.clients))
		for _, c := range s.clients {
			out = append(out, c)
		}
		s.mu.RUnlock()
		if s.postReadDelay > 0 {
			time.Sleep(s.postReadDelay)
		}
		return out
	}, krtopts.ToOptions("StressUniqueClients")...)
	return s, col
}

func (s *stressUccSource) add(c ir.UniqlyConnectedClient) {
	s.mu.Lock()
	s.clients[c.ResourceName()] = c
	s.mu.Unlock()
	s.trigger.TriggerRecomputation()
}

func (s *stressUccSource) del(rn string) {
	s.mu.Lock()
	delete(s.clients, rn)
	s.mu.Unlock()
	s.trigger.TriggerRecomputation()
}

// A stable client whose Envoy "blips" (delete + re-add of the SAME client)
// concurrently with other-client churn and backend churn, all driven through the
// real trigger collection. After churn settles and the stable client is present, it
// must have a cluster for every backend. Run with -race.
func TestPerClientClusters_TriggerDrivenChurnNeverStrands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	stable := clustersTestClient("role-stable")
	src, uccs := newStressUccSource(krtopts, 0, []ir.UniqlyConnectedClient{stable})

	backendNames := []string{"b1", "b2", "b3", "b4", "b5"}
	backends := make([]*ir.BackendObjectIR, 0, len(backendNames))
	for _, n := range backendNames {
		backends = append(backends, clustersTestBackend(n))
	}
	finalBackends := krt.NewStaticCollection(nil, backends, krtopts.ToOptions("FinalBackends")...)

	clusters := NewPerClientEnvoyClusters(ctx, krtopts, clustersTestTranslator(), finalBackends, uccs, nil)
	eventuallyClusterCount(t, clusters, stable, len(backendNames))

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Blip the stable client: rapid delete + re-add of the identical client.
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			src.del(stable.ResourceName())
			src.add(stable)
		}
	})

	// Churn other clients.
	for g := range 6 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			c := clustersTestClient(fmt.Sprintf("role-churn-%d", g))
			for {
				select {
				case <-stop:
					return
				default:
				}
				src.add(c)
				src.del(c.ResourceName())
			}
		}(g)
	}

	// Churn a backend so per-client rows recompute during client blips.
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			finalBackends.UpdateObject(clustersTestBackend("b5"))
		}
	})

	time.Sleep(2 * time.Second)
	close(stop)
	wg.Wait()

	// Ensure the stable client is present as the final state, then require recovery.
	src.add(stable)
	eventuallyClusterCount(t, clusters, stable, len(backendNames))
}

// slowClustersTestTranslator is clustersTestTranslator with an artificial
// per-(backend, client) delay, so a fan-out burst (every backend transform
// re-running against the client set) stays in flight long enough for client
// mutations + triggers to land mid-burst — the window in which the production
// holes (#14184) must have formed.
func slowClustersTestTranslator(perPair time.Duration) *irtranslator.BackendTranslator {
	return &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			{Group: "", Kind: "Service"}: {
				InitEnvoyBackend: func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					time.Sleep(perPair)
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
		},
	}
}

// clustersFingerprint summarizes the collection's full output (row name ->
// version) so quiescence can be detected without issuing any event.
func clustersFingerprint(c PerClientEnvoyClusters) string {
	rows := c.clusters.List()
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s=%d", r.ResourceName(), r.ClusterVersion))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// waitQuiescent waits until the collection output has been stable for
// stableFor (no event issued by the wait itself — a healing event would mask
// exactly the holes these tests hunt). Fails the test on timeout.
func waitQuiescent(t *testing.T, c PerClientEnvoyClusters, stableFor, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := clustersFingerprint(c)
	stableSince := time.Now()
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		cur := clustersFingerprint(c)
		if cur != last {
			last = cur
			stableSince = time.Now()
			continue
		}
		if time.Since(stableSince) >= stableFor {
			return
		}
	}
	t.Fatalf("collection never quiesced within %v", timeout)
}

func stressIterations(t *testing.T, def int) int {
	if v := os.Getenv("KGW_STRESS_ITERATIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			t.Fatalf("invalid KGW_STRESS_ITERATIONS %q: %v", v, err)
		}
		return n
	}
	return def
}

// Reproducer attempt for the #14184 stranding (cohort of clients permanently
// missing rows for stable backends; old clients' rows intact). Targeted
// window: a client connects (map mutation + TriggerRecomputation) while a
// source recompute holding a PRE-mutation read of the map is still in flight
// (widened here by postReadDelay). If the trigger event arriving mid-run were
// coalesced into the in-flight recompute instead of producing a follow-up
// one, the source output would remain the stale list — hash-identical to the
// previous output, so suppressed and traceless — and the new client would
// never appear downstream. The probe add is the FINAL event of each
// iteration: nothing afterwards can heal a hole, so a single coalescing loss
// fails the assertion.
func TestPerClientClusters_Repro_TriggerDuringInflightStaleRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	stable := clustersTestClient("role-stable")
	src, uccs := newStressUccSource(krtopts, 10*time.Millisecond, []ir.UniqlyConnectedClient{stable})

	backendNames := []string{"b1", "b2", "b3", "b4", "b5"}
	backends := make([]*ir.BackendObjectIR, 0, len(backendNames))
	for _, n := range backendNames {
		backends = append(backends, clustersTestBackend(n))
	}
	finalBackends := krt.NewStaticCollection(nil, backends, krtopts.ToOptions("FinalBackends")...)
	clusters := NewPerClientEnvoyClusters(ctx, krtopts, clustersTestTranslator(), finalBackends, uccs, nil)
	eventuallyClusterCount(t, clusters, stable, len(backendNames))

	iterations := stressIterations(t, 40)
	for i := range iterations {
		// Start a recompute that will hold a pre-probe read of the map for
		// postReadDelay.
		decoy := clustersTestClient(fmt.Sprintf("role-decoy-%d", i))
		src.add(decoy)
		// Let the decoy-triggered recompute begin and take its read...
		time.Sleep(2 * time.Millisecond)
		// ...then connect the probe while that recompute is (likely) still
		// sleeping. This is the last event of the iteration.
		probe := clustersTestClient(fmt.Sprintf("role-probe-%d", i))
		src.add(probe)

		waitQuiescent(t, clusters, 50*time.Millisecond, 5*time.Second)
		require.NotNilf(t, uccs.GetKey(probe.ResourceName()),
			"iteration %d: probe client missing from source collection after quiescence — trigger coalesced away (reproduced #14184 shape)", i)
		require.Lenf(t, clusterNamesForClient(clusters, probe), len(backendNames),
			"iteration %d: probe client missing per-client cluster rows after quiescence (reproduced #14184 shape); got %v", i, clusterNamesForClient(clusters, probe))

		// Reset for the next iteration (these events may heal; the assertion
		// above already ran against the quiescent pre-heal state).
		src.del(decoy.ResourceName())
		src.del(probe.ResourceName())
	}
}

// Reproducer attempt for the downstream half of the #14184 shape: clients
// connecting while SLOW per-backend fan-out bursts are still in flight. The
// per-pair translation delay keeps many backend-keyed transform runs holding
// stale Fetch(uccs) results while probes connect; the probes' adds are the
// FINAL events before the quiescent assertion, so a lost or partially
// dispatched fan-out (some backend keys never re-running against the new
// client list) leaves exactly the production wreckage: a probe with rows for
// SOME backends only, while pre-existing clients keep full rows.
func TestPerClientClusters_Repro_ConnectDuringSlowFanout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	stable := clustersTestClient("role-stable")
	src, uccs := newStressUccSource(krtopts, 2*time.Millisecond, []ir.UniqlyConnectedClient{stable})

	const backendCount = 12
	backends := make([]*ir.BackendObjectIR, 0, backendCount)
	for i := range backendCount {
		backends = append(backends, clustersTestBackend(fmt.Sprintf("b%d", i)))
	}
	finalBackends := krt.NewStaticCollection(nil, backends, krtopts.ToOptions("FinalBackends")...)
	clusters := NewPerClientEnvoyClusters(ctx, krtopts,
		slowClustersTestTranslator(300*time.Microsecond), finalBackends, uccs, nil)
	eventuallyClusterCount(t, clusters, stable, backendCount)

	iterations := stressIterations(t, 6)
	for i := range iterations {
		stop := make(chan struct{})
		var wg sync.WaitGroup

		// Keep slow fan-out bursts continuously in flight: backend churn
		// re-runs single backend keys; client churn re-runs ALL backend keys.
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				finalBackends.UpdateObject(clustersTestBackend("b0"))
				time.Sleep(time.Millisecond)
			}
		})
		for g := range 4 {
			churner := clustersTestClient(fmt.Sprintf("role-churn-%d-%d", i, g))
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					src.add(churner)
					src.del(churner.ResourceName())
				}
			})
		}

		time.Sleep(500 * time.Millisecond)

		// Connect the probe cohort while churn fan-outs are still in flight,
		// then stop churn. The probe adds are the last client-set mutations.
		probes := make([]ir.UniqlyConnectedClient, 0, 4)
		for p := range 4 {
			probe := clustersTestClient(fmt.Sprintf("role-probe-%d-%d", i, p))
			probes = append(probes, probe)
			src.add(probe)
			time.Sleep(3 * time.Millisecond)
		}
		close(stop)
		wg.Wait()

		waitQuiescent(t, clusters, 100*time.Millisecond, 10*time.Second)
		// Permanence check, not a drain race: after the last event, NOTHING can
		// heal a hole (krt has no timers), so polling for 30s with zero further
		// events cleanly separates "queue still draining" (converges, passes)
		// from "permanently stranded" (the #14184 shape; fails).
		for _, probe := range probes {
			require.Eventuallyf(t, func() bool {
				return len(clusterNamesForClient(clusters, probe)) == backendCount
			}, 30*time.Second, 20*time.Millisecond,
				"iteration %d: probe %s PERMANENTLY stranded with partial rows (reproduced #14184); in source collection: %v; rows: %v",
				i, probe.ResourceName(), uccs.GetKey(probe.ResourceName()) != nil, clusterNamesForClient(clusters, probe))
		}
		eventuallyClusterCount(t, clusters, stable, backendCount)

		for _, probe := range probes {
			src.del(probe.ResourceName())
		}
	}
}
