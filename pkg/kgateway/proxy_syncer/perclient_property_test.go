package proxy_syncer

// Model-based / stateful property test for snapshotPerClient.
//
// Future-work item #1 from the xDS bug-finding plan: instead of hand-writing
// scenarios, drive the real snapshotPerClient KRT transform through long
// randomized sequences of input events (CDS add/remove, EDS
// absent/empty/ready, route retarget across direct and weighted styles) and,
// after every event, assert the spec's safety invariants on whatever the
// transform actually publishes — using xdscheck (the concrete Go invariant
// checker) as the oracle. Issues #13868 and #14184 both lived in event
// interleavings nobody wrote a test for; this explores that space.
//
// Invariants checked on every observed published snapshot:
//   - Snapshot closure + no-orphan-CLA: xdscheck.CheckSnapshot reports no
//     error-severity findings (route->cluster refs resolve, EDS cluster->CLA
//     refs resolve, no CLA for a cluster absent from CDS).
//   - EDS version discipline: when the published EDS resource *content*
//     changes between two snapshots, the EDS version string must change too
//     (the EDSResourceSetChangeChangesVersion invariant; under-versioning is
//     the hazard because Envoy would not refetch).
// Progress (liveness) check: after driving to a fully coherent, stable input
//   (every referenced cluster in CDS with a ready CLA), a coherent snapshot
//   must eventually be published.
//
// Errored clusters are deliberately excluded: the gate intentionally
// publishes a route referencing an errored cluster without that cluster in
// CDS (Envoy 503s that route), which xdscheck flags as a dangling reference
// — a true oracle false-positive. Covering errored clusters needs a refined
// oracle and is left as follow-up.
//
// The generator freely produces both inconsistency directions:
// cluster-without-CLA (an EDS cluster in CDS with no CLA yet) and
// CLA-without-cluster (a stale CLA whose cluster has left CDS).
// filterEndpointResourcesForClusters must reconcile both — dropping stale CLAs
// and synthesizing empty assignments for EDS clusters lacking one — so every
// published snapshot is EDS-consistent. This test is the regression gate for
// that synthesis: before it was added, the cluster-without-CLA case published
// an inconsistent snapshot (xds_consistency_probe_test.go documents the
// go-control-plane tolerance it used to lean on).
//
// Determinism: each iteration runs a fixed seed (base seed overridable via
// XDS_PROP_SEED); iteration/step/cluster counts are overridable via
// XDS_PROP_ITERS / XDS_PROP_STEPS / XDS_PROP_CLUSTERS for deeper local sweeps.
// On any violation the failure prints the seed and the full event journal so
// it reproduces deterministically.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/xdscheck"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

type epState int

const (
	epAbsent epState = iota
	epEmpty
	epReady
)

func (s epState) String() string {
	switch s {
	case epAbsent:
		return "absent"
	case epEmpty:
		return "empty"
	default:
		return "ready"
	}
}

// propWorld is the test's model of the inputs plus the krt collections that
// mirror it. Each logical event mutates exactly one collection so an
// intermediate KRT recompute can never observe a half-applied event.
type propWorld struct {
	ucc      ir.UniquelyConnectedClient
	clusters []string

	inCDS map[string]bool
	ep    map[string]epState
	refs  map[string]bool
	// weighted toggles the route style so both cluster-reference extraction
	// paths (RouteAction_Cluster and RouteAction_WeightedClusters) are driven.
	weighted bool

	clusterCol     krt.StaticCollection[uccWithCluster]
	endpointCol    krt.StaticCollection[UccWithEndpoints]
	gatewaySnaps   krt.StaticCollection[GatewayXdsResources]
	listeners      envoycache.Resources
	namespacedName types.NamespacedName

	ver     uint64
	journal []string
}

func (w *propWorld) nextVer() uint64 { w.ver++; return w.ver }

func (w *propWorld) referencedList() []string {
	out := make([]string, 0, len(w.refs))
	for c, on := range w.refs {
		if on {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

func (w *propWorld) applyRefs() {
	refs := w.referencedList()
	var routes envoycache.Resources
	if w.weighted && len(refs) > 0 {
		routes = weightedRouteResourcesForClusters(refs...)
	} else {
		routes = routeResourcesForClusters(refs...)
	}
	w.gatewaySnaps.UpdateObject(GatewayXdsResources{
		NamespacedName:     w.namespacedName,
		Routes:             routes,
		Listeners:          w.listeners,
		ReferencedClusters: collectReferencedClusters(routes, w.listeners),
	})
	w.journal = append(w.journal, fmt.Sprintf("refs=%v weighted=%v", refs, w.weighted))
}

func (w *propWorld) applyCDS(c string, present bool) {
	w.inCDS[c] = present
	if present {
		w.clusterCol.UpdateObject(edsClusterForClient(w.ucc, c, w.nextVer()))
	} else {
		w.clusterCol.DeleteObject(edsClusterForClient(w.ucc, c, 0).ResourceName())
	}
	w.journal = append(w.journal, fmt.Sprintf("cds[%s]=%v", c, present))
}

func (w *propWorld) applyEP(c string, st epState) {
	w.ep[c] = st
	switch st {
	case epReady:
		w.endpointCol.UpdateObject(endpointsForClient(w.ucc, c, w.nextVer()))
	case epEmpty:
		w.endpointCol.UpdateObject(emptyEndpointsForClient(w.ucc, c, w.nextVer()))
	case epAbsent:
		w.endpointCol.DeleteObject(emptyEndpointsForClient(w.ucc, c, 0).ResourceName())
	}
	w.journal = append(w.journal, fmt.Sprintf("eds[%s]=%s", c, st))
}

// edsContentSignature is a stable digest of the published EDS resource set's
// content (CLA name + proto hash), independent of map ordering. Two snapshots
// with the same signature carry the same endpoints; different signatures mean
// different endpoints and therefore must carry different EDS versions.
func edsContentSignature(snap *envoycache.Snapshot) string {
	items := snap.Resources[envoycachetypes.Endpoint].Items
	parts := make([]string, 0, len(items))
	for name, item := range items {
		cla, ok := item.Resource.(*envoyendpointv3.ClusterLoadAssignment)
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s:%d", name, utils.HashProto(cla)))
	}
	sort.Strings(parts)
	return fmt.Sprintf("%v", parts)
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func TestSnapshotPerClientRandomizedEventSequencesConformToSpec(t *testing.T) {
	if testing.Short() {
		t.Skip("randomized property sweep is slow; skipped under -short")
	}
	// Modest defaults keep the unit suite fast; nightly/local deep sweeps set
	// XDS_PROP_ITERS / XDS_PROP_STEPS / XDS_PROP_CLUSTERS higher (e.g. 100/40/4
	// or 50/80/5, both run clean).
	baseSeed := int64(envInt("XDS_PROP_SEED", 1))
	iters := envInt("XDS_PROP_ITERS", 10)
	steps := envInt("XDS_PROP_STEPS", 20)
	numClusters := envInt("XDS_PROP_CLUSTERS", 4)

	for iter := range iters {
		seed := baseSeed + int64(iter)
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			runPropertySeed(t, seed, steps, numClusters)
		})
	}
}

func runPropertySeed(t *testing.T, seed int64, steps, numClusters int) {
	//nolint:gosec // G404: a deterministic, seeded PRNG is intentional here so failing sequences reproduce; not security-sensitive.
	rng := rand.New(rand.NewSource(seed))

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniquelyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, []ir.UniquelyConnectedClient{ucc})

	clusterNames := make([]string, numClusters)
	for i := range clusterNames {
		clusterNames[i] = fmt.Sprintf("cluster-%d", i)
	}

	w := &propWorld{
		ucc:            ucc,
		clusters:       clusterNames,
		inCDS:          map[string]bool{},
		ep:             map[string]epState{},
		refs:           map[string]bool{},
		listeners:      sliceToResources([]*envoylistenerv3.Listener{httpListenerWithRDS(t, "listener", "route-config")}),
		namespacedName: types.NamespacedName{Namespace: "ns", Name: "gw"},
	}
	for _, c := range clusterNames {
		w.ep[c] = epAbsent
	}

	w.clusterCol = krt.NewStaticCollection[uccWithCluster](nil, nil)
	w.endpointCol = krt.NewStaticCollection[UccWithEndpoints](nil, nil)
	w.gatewaySnaps = krt.NewStaticCollection[GatewayXdsResources](nil, nil)
	w.applyRefs() // start with an empty route config (LDS/RDS only)

	snapshots := snapshotPerClient(
		krtutil.KrtOptions{},
		uccs,
		w.gatewaySnaps,
		PerClientEnvoyEndpoints{
			endpoints: w.endpointCol,
			index: krtpkg.UnnamedIndex(w.endpointCol, func(ep UccWithEndpoints) []string {
				return []string{ep.Client.ResourceName()}
			}),
		},
		PerClientEnvoyClusters{
			clusters: w.clusterCol,
			index: krtpkg.UnnamedIndex(w.clusterCol, func(c uccWithCluster) []string {
				return []string{c.Client.ResourceName()}
			}),
		},
	)

	// Everything the transform emits is resolved per cluster and published
	// through the real syncXds into a real snapshot cache; the oracle then
	// checks what a client would actually be served — including the held-flip
	// and carry-forward compositions.
	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	registerSyncXds(snapshots, NewProxyTranslator(cache))
	nodeID := ucc.ResourceName()

	var (
		haveLast bool
		lastSig  string
		lastVer  string
	)
	// checkPublished samples the currently-served cache snapshot and asserts
	// the safety invariants. Called repeatedly during the settle window so
	// even a transient incoherent publish is caught.
	checkPublished := func(stepDesc string) {
		resourceSnapshot, err := cache.GetSnapshot(nodeID)
		if err != nil {
			return // nothing published yet (cold start withhold)
		}
		snap, ok := resourceSnapshot.(*envoycache.Snapshot)
		if !ok {
			return
		}
		findings := xdscheck.ErrorFindings(xdscheck.CheckSnapshot(context.Background(), xdsCheckSnapshotFromCache(t, snap)))
		if len(findings) > 0 {
			t.Fatalf("seed=%d: served snapshot violates spec after %q\nfindings: %+v\njournal:\n  %s",
				seed, stepDesc, findings, joinJournal(w.journal))
		}
		sig := edsContentSignature(snap)
		ver := snap.Resources[envoycachetypes.Endpoint].Version
		if haveLast && sig != lastSig && ver == lastVer {
			t.Fatalf("seed=%d: EDS content changed but version did not after %q\n  sig %q -> %q, version stayed %q\njournal:\n  %s",
				seed, stepDesc, lastSig, sig, ver, joinJournal(w.journal))
		}
		haveLast, lastSig, lastVer = true, sig, ver
	}

	settleAndCheck := func(stepDesc string) {
		// KRT recompute is fast and synchronous-ish for static collections;
		// sample a few times across a short window to catch transients and the
		// settled state.
		for range 4 {
			checkPublished(stepDesc)
			time.Sleep(15 * time.Millisecond)
		}
	}

	for step := range steps {
		c := clusterNames[rng.Intn(numClusters)]
		// Inputs are toggled freely and independently, so both inconsistency
		// directions arise: toggling CDS can leave an EDS cluster without a CLA
		// (cluster-without-CLA) and cycling EDS can leave a CLA whose cluster is
		// gone (CLA-without-cluster). The published snapshot must be consistent
		// regardless.
		switch rng.Intn(4) {
		case 0: // toggle route reference (and occasionally the route style)
			w.refs[c] = !w.refs[c]
			if rng.Intn(3) == 0 {
				w.weighted = !w.weighted
			}
			w.applyRefs()
		case 1: // toggle cluster presence in CDS
			w.applyCDS(c, !w.inCDS[c])
		case 2: // cycle endpoint state (absent -> empty -> ready -> ...)
			w.applyEP(c, epState((int(w.ep[c])+1+rng.Intn(2))%3))
		case 3: // jump endpoint straight to a random state
			w.applyEP(c, epState(rng.Intn(3)))
		}
		settleAndCheck(fmt.Sprintf("step %d", step))
	}

	// Progress: drive to a fully coherent, stable state and require a
	// coherent snapshot to be published. Referenced clusters are re-applied
	// UNCONDITIONALLY with fresh versions so a terminal KRT recompute is
	// guaranteed to fire — this isolates snapshotPerClient's publish logic
	// from KRT static-collection event quiescence in the test harness (the
	// separate "inputs coherent but no event delivered" concern is KRT-A1,
	// covered by the formal liveness work and the tier2 watchdog, not here).
	driveCoherent := func() {
		for _, c := range clusterNames {
			if w.refs[c] {
				w.inCDS[c] = true
				w.ep[c] = epReady
				w.clusterCol.UpdateObject(edsClusterForClient(w.ucc, c, w.nextVer()))
				w.endpointCol.UpdateObject(endpointsForClient(w.ucc, c, w.nextVer()))
			}
		}
		// Toggle the route style to force a fresh GatewayXdsResources version
		// (a terminal event for the dependent recompute) while keeping the
		// referenced set — and thus coherence — identical.
		w.weighted = !w.weighted
		w.applyRefs()
	}
	driveCoherent()
	// A coherent end state must produce a coherent, non-deferred wrapper AND
	// a served snapshot whose routes are the coherent routes (the held flip,
	// if any, must be released).
	waitServedCoherent := func(d time.Duration) *envoycache.Snapshot {
		deadline := time.Now().Add(d)
		for {
			list := snapshots.List()
			if len(list) == 1 && !list[0].deferred {
				wantRoutes := list[0].snap.Resources[envoycachetypes.Route].Version
				if resourceSnapshot, err := cache.GetSnapshot(nodeID); err == nil {
					if snap, ok := resourceSnapshot.(*envoycache.Snapshot); ok &&
						snap.Resources[envoycachetypes.Route].Version == wantRoutes {
						return snap
					}
				}
			}
			if time.Now().After(deadline) {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Re-drive the coherent state on each round so a terminal KRT recompute is
	// repeatedly forced: static-collection event delivery in the harness can
	// need several nudges to propagate through the derived-collection graph
	// (proven a quiescence artifact for seeds 46/54/77 by direct state dumps).
	// snapshotPerClient's gate is a pure function of the fetched collections,
	// so a genuinely stuck-defer would never publish across any number of
	// fresh events; coherent inputs publish on some round.
	var snap *envoycache.Snapshot
	for range 12 {
		if snap = waitServedCoherent(400 * time.Millisecond); snap != nil {
			break
		}
		driveCoherent()
	}
	if snap == nil {
		t.Fatalf("seed=%d: coherent inputs never produced a served coherent snapshot across repeated nudges (liveness)\nrefs=%v\njournal:\n  %s",
			seed, w.referencedList(), joinJournal(w.journal))
	}
	findings := xdscheck.ErrorFindings(xdscheck.CheckSnapshot(context.Background(), xdsCheckSnapshotFromCache(t, snap)))
	if len(findings) > 0 {
		t.Fatalf("seed=%d: coherent-state served snapshot violates spec\nfindings: %+v\njournal:\n  %s",
			seed, findings, joinJournal(w.journal))
	}
}

func joinJournal(journal []string) string {
	var out strings.Builder
	for i, e := range journal {
		if i > 0 {
			out.WriteString("\n  ")
		}
		out.WriteString(e)
	}
	return out.String()
}
