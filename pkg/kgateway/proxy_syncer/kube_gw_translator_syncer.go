package proxy_syncer

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"sync"
	"time"

	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

// perClientPublishBudget bounds how long ANY client waits for its per-client
// inputs to become coherent. Within the budget, deferred (incomplete)
// snapshots are withheld — the inputs usually converge within a recompute or
// two, and a withheld update is invisible (the client keeps its last
// published config, or for a brand-new client, keeps waiting). Once the
// budget expires the latest deferred snapshot is published:
//
//   - to a never-published client, as-is. A client with no config at all
//     serves nothing (its pod never goes Ready), so a bounded degradation
//     strictly beats an unbounded outage.
//   - to an already-published client, merged with carry-forward: any
//     referenced cluster or CLA the deferred snapshot is missing is retained
//     from the currently-published snapshot, so an incomplete publish can
//     never remove a resource the client is actively using. Config keeps
//     flowing under sustained churn, and staleness shrinks from "the whole
//     gateway is frozen" to "only the still-incoherent references are stale."
//
// Variable rather than const so tests can shorten it.
var perClientPublishBudget = 15 * time.Second

// clientPublishState tracks publication bookkeeping for one client (keyed by
// proxyKey) between syncXds invocations.
type clientPublishState struct {
	// pending is the latest deferred wrapper, published (possibly merged) if
	// the budget expires before a coherent snapshot supersedes it.
	pending *XdsSnapWrapper
	// timer fires when the budget expires to publish pending.
	timer *time.Timer
	// deferredSinceLastPublish makes the next coherent publish count as a
	// recovery in metrics.
	deferredSinceLastPublish bool
}

// publishBudgetGate serializes publication decisions for deferred snapshots.
// It is shared by reference across ProxyTranslator copies.
type publishBudgetGate struct {
	mu      sync.Mutex
	clients map[string]*clientPublishState
}

func newPublishBudgetGate() *publishBudgetGate {
	return &publishBudgetGate{clients: make(map[string]*clientPublishState)}
}

// syncXds applies the publication policy for one wrapper event:
//
//   - coherent snapshot: publish, cancel any pending budget timer, count a
//     recovery if this client had deferred since its last coherent publish.
//   - deferred snapshot: record it as pending and (re-)arm the budget timer;
//     publication happens when the budget expires (see publishPendingBudgeted)
//     unless a coherent snapshot arrives first.
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) {
	proxyKey := snapWrap.proxyKey

	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey, "deferred", snapWrap.deferred)
	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	if !snapWrap.deferred {
		// Publish under the gate lock so a concurrently-firing budget timer
		// can never overwrite this coherent snapshot with an older deferred
		// one.
		g := s.publishBudget
		g.mu.Lock()
		recovered := g.clearPendingLocked(proxyKey)
		s.xdsCache.SetSnapshot(ctx, proxyKey, snapWrap.snap)
		g.mu.Unlock()
		if recovered {
			recordSnapshotRecovery(proxyKey)
		}
		return
	}

	recordSnapshotDefer(proxyKey, snapWrap.deferReasons)
	s.offerBudgetedPublish(ctx, proxyKey, snapWrap)
}

// offerBudgetedPublish records the latest deferred wrapper for a client and
// arms the budget timer if one is not already running for this episode.
func (s *ProxyTranslator) offerBudgetedPublish(ctx context.Context, proxyKey string, snapWrap XdsSnapWrapper) {
	g := s.publishBudget
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.clients[proxyKey]
	if st == nil {
		st = &clientPublishState{}
		g.clients[proxyKey] = st
	}
	st.deferredSinceLastPublish = true
	st.pending = &snapWrap
	if st.timer != nil {
		return // timer already armed; it will publish the latest pending
	}
	logger.Info("withholding publish until per-client inputs converge or the budget expires",
		"client", proxyKey, "budget", perClientPublishBudget, "reasons", snapWrap.deferReasons)
	st.timer = time.AfterFunc(perClientPublishBudget, func() {
		s.publishPendingBudgeted(ctx, proxyKey)
	})
}

// publishPendingBudgeted runs when the publish budget expires: publish the
// latest deferred wrapper — as-is for a never-published client, merged with
// carry-forward for an already-published one — unless a coherent snapshot got
// there first. The cache check and mutation happen under the gate lock,
// mirroring the coherent-publish path, so the two can never interleave.
func (s *ProxyTranslator) publishPendingBudgeted(ctx context.Context, proxyKey string) {
	g := s.publishBudget
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.clients[proxyKey]
	if st == nil || st.pending == nil {
		return
	}
	wrap := st.pending
	// Keep the state (and deferredSinceLastPublish) so the episode continues:
	// a later deferred wrapper re-arms the timer, and the eventual coherent
	// publish still counts as a recovery.
	st.pending = nil
	st.timer = nil

	published, err := s.xdsCache.GetSnapshot(proxyKey)
	if err != nil {
		// Never-published client: an incomplete snapshot beats no listeners.
		logger.Warn("publish budget expired; publishing deferred snapshot so the client can start",
			"client", proxyKey, "reasons", wrap.deferReasons)
		s.xdsCache.SetSnapshot(ctx, proxyKey, wrap.snap)
		recordBoundedPublish(proxyKey, boundedPublishFirstPublish)
		return
	}

	merged, carried := mergeCarryForward(wrap, published)
	logger.Warn("publish budget expired; publishing deferred snapshot with carry-forward so config keeps flowing",
		"client", proxyKey, "reasons", wrap.deferReasons, "carried_forward", carried)
	s.xdsCache.SetSnapshot(ctx, proxyKey, merged)
	recordBoundedPublish(proxyKey, boundedPublishCarryForward)
}

// mergeCarryForward returns the deferred wrapper's snapshot with any
// referenced cluster or CLA it is missing retained from the
// currently-published snapshot, so a budget-expiry publish to a warm client
// can only add or update resources the client uses — never remove them.
// Preference order for a referenced EDS cluster's CLA: the deferred
// snapshot's real CLA > the previously-published CLA > the synthesized empty.
// Returns the carried resource names for logging (empty if nothing needed
// carrying, in which case the wrapper's snapshot is returned unmodified).
func mergeCarryForward(wrap *XdsSnapWrapper, published envoycache.ResourceSnapshot) (*envoycache.Snapshot, []string) {
	newClusters := cloneOrInit(wrap.snap.Resources[envoycachetypes.Cluster].Items)
	newEndpoints := cloneOrInit(wrap.snap.Resources[envoycachetypes.Endpoint].Items)
	oldClusters := published.GetResourcesAndTTL(resourcev3.ClusterType)
	oldEndpoints := published.GetResourcesAndTTL(resourcev3.EndpointType)
	synthesized := stringSet(wrap.synthesizedClas)

	var carried []string

	// Carry forward referenced clusters missing from the deferred CDS.
	for name := range wrap.referencedClusters {
		if name == wellknown.BlackholeClusterName {
			continue
		}
		if _, ok := newClusters[name]; ok {
			continue
		}
		old, ok := oldClusters[name]
		if !ok {
			continue // neither snapshot has it; nothing to preserve
		}
		newClusters[name] = old
		carried = append(carried, "cluster/"+name)
	}

	// Ensure every referenced EDS cluster in the merged CDS has the best
	// available CLA: a real one from the deferred snapshot, else the
	// previously-published one (covers carried clusters and replaces
	// synthesized empties), else whatever is already there.
	for name := range wrap.referencedClusters {
		clusterResource, ok := newClusters[name]
		if !ok {
			continue
		}
		claName, requiresEndpointResource := endpointResourceNameForCluster(clusterResource)
		if !requiresEndpointResource {
			continue
		}
		_, isSynthesized := synthesized[claName]
		if _, hasReal := newEndpoints[claName]; hasReal && !isSynthesized {
			continue
		}
		old, ok := oldEndpoints[claName]
		if !ok {
			continue
		}
		newEndpoints[claName] = old
		carried = append(carried, "endpoints/"+claName)
	}

	if len(carried) == 0 {
		return wrap.snap, nil
	}
	sort.Strings(carried)
	var carryHash uint64
	for _, c := range carried {
		carryHash ^= utils.HashString(c)
	}

	merged := *wrap.snap
	merged.Resources[envoycachetypes.Cluster] = envoycache.Resources{
		Version: fmt.Sprintf("%s-carry-%d", wrap.snap.Resources[envoycachetypes.Cluster].Version, carryHash),
		Items:   newClusters,
	}
	merged.Resources[envoycachetypes.Endpoint] = envoycache.Resources{
		Version: fmt.Sprintf("%s-carry-%d", wrap.snap.Resources[envoycachetypes.Endpoint].Version, carryHash),
		Items:   newEndpoints,
	}
	return &merged, carried
}

func cloneOrInit(items map[string]envoycachetypes.ResourceWithTTL) map[string]envoycachetypes.ResourceWithTTL {
	if items == nil {
		return make(map[string]envoycachetypes.ResourceWithTTL)
	}
	return maps.Clone(items)
}

// clearPendingLocked cancels any pending budgeted publish for the client and
// reports whether the client had deferred snapshots since its last coherent
// publish. Callers must hold g.mu.
func (g *publishBudgetGate) clearPendingLocked(proxyKey string) (recovered bool) {
	st := g.clients[proxyKey]
	if st == nil {
		return false
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	recovered = st.deferredSinceLastPublish
	delete(g.clients, proxyKey)
	return recovered
}

// clientDeparted drops all publish-budget bookkeeping for a client whose
// wrapper row was deleted (client disconnected or its gateway snapshot went
// away). Without this, pending timers could publish to a key after its client
// left, and the bookkeeping map would grow with departed clients.
func (g *publishBudgetGate) clientDeparted(proxyKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.clients[proxyKey]
	if st == nil {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	delete(g.clients, proxyKey)
}
