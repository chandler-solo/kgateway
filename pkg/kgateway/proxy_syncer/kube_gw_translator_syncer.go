package proxy_syncer

import (
	"context"
	"sync"
	"time"

	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

// perClientFirstPublishBudget bounds how long a connected client that has
// never been published a snapshot waits for its per-client inputs to become
// coherent. Within the budget, deferred (incomplete) snapshots are withheld —
// the inputs usually converge within a recompute or two. Once the budget
// expires, the latest deferred snapshot is published anyway: a client with no
// config at all serves nothing (its pod never goes Ready), so a bounded
// degradation — missing clusters absent from CDS, missing endpoints as
// explicit empty CLAs — strictly beats an unbounded outage.
//
// Clients that already hold a published snapshot are NEVER sent a deferred
// one, with no time bound: Envoy keeping its last coherent config is the
// correct degradation for a warm client, and publishing an incomplete
// snapshot would actively remove resources it is using.
//
// Variable rather than const so tests can shorten it.
var perClientFirstPublishBudget = 15 * time.Second

// firstPublishState tracks publication bookkeeping for one client (keyed by
// proxyKey) between syncXds invocations.
type firstPublishState struct {
	// firstDeferredAt is when this never-published client's first deferred
	// snapshot arrived; zero if the client is not awaiting first publish.
	firstDeferredAt time.Time
	// pending is the latest deferred wrapper, published if the budget expires
	// before a coherent snapshot supersedes it.
	pending *XdsSnapWrapper
	// timer fires at firstDeferredAt+budget to publish pending.
	timer *time.Timer
	// deferredSinceLastPublish makes the next coherent publish count as a
	// recovery in metrics.
	deferredSinceLastPublish bool
}

// firstPublishGate serializes publication decisions for deferred snapshots.
// It is shared by reference across ProxyTranslator copies.
type firstPublishGate struct {
	mu      sync.Mutex
	clients map[string]*firstPublishState
}

func newFirstPublishGate() *firstPublishGate {
	return &firstPublishGate{clients: make(map[string]*firstPublishState)}
}

// syncXds applies the publication policy for one wrapper event:
//
//   - coherent snapshot: publish, cancel any pending first-publish timer,
//     count a recovery if this client had deferred since its last publish.
//   - deferred snapshot, client already published: withhold (keep last good).
//   - deferred snapshot, client never published: hold up to
//     perClientFirstPublishBudget, then publish the latest deferred wrapper.
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) {
	proxyKey := snapWrap.proxyKey

	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey, "deferred", snapWrap.deferred)
	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	if !snapWrap.deferred {
		// Publish under the gate lock so a concurrently-firing first-publish
		// timer can never overwrite this coherent snapshot with an older
		// deferred one.
		g := s.firstPublish
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

	if _, err := s.xdsCache.GetSnapshot(proxyKey); err == nil {
		// Warm client: withhold, unbounded by design (see budget comment).
		s.firstPublish.markDeferred(proxyKey)
		return
	}

	s.offerFirstPublish(ctx, proxyKey, snapWrap)
}

// offerFirstPublish records the latest deferred wrapper for a never-published
// client and arms (once) the budget timer that publishes it.
func (s *ProxyTranslator) offerFirstPublish(ctx context.Context, proxyKey string, snapWrap XdsSnapWrapper) {
	g := s.firstPublish
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.clients[proxyKey]
	if st == nil {
		st = &firstPublishState{}
		g.clients[proxyKey] = st
	}
	st.deferredSinceLastPublish = true
	st.pending = &snapWrap
	if st.timer != nil {
		return // timer already armed; it will publish the latest pending
	}
	st.firstDeferredAt = time.Now()
	logger.Info("withholding first publish until per-client inputs converge or budget expires",
		"client", proxyKey, "budget", perClientFirstPublishBudget, "reasons", snapWrap.deferReasons)
	st.timer = time.AfterFunc(perClientFirstPublishBudget, func() {
		s.publishPendingFirstPublish(ctx, proxyKey)
	})
}

// publishPendingFirstPublish runs when the first-publish budget expires:
// publish the latest deferred wrapper unless a coherent snapshot got there
// first. The cache check and mutation happen under the gate lock, mirroring
// the coherent-publish path, so the two can never interleave.
func (s *ProxyTranslator) publishPendingFirstPublish(ctx context.Context, proxyKey string) {
	g := s.firstPublish
	g.mu.Lock()
	defer g.mu.Unlock()

	st := g.clients[proxyKey]
	if st == nil || st.pending == nil {
		return
	}
	wrap := st.pending
	st.pending = nil
	st.timer = nil
	if _, err := s.xdsCache.GetSnapshot(proxyKey); err == nil {
		// A coherent snapshot was published while the timer was in flight.
		return
	}
	logger.Warn("first-publish budget expired; publishing deferred snapshot so the client can start",
		"client", proxyKey, "reasons", wrap.deferReasons)
	s.xdsCache.SetSnapshot(ctx, proxyKey, wrap.snap)
	recordBoundedFirstPublish(proxyKey)
}

// clearPendingLocked cancels any pending first publish for the client and
// reports whether the client had deferred snapshots since its last coherent
// publish. Callers must hold g.mu.
func (g *firstPublishGate) clearPendingLocked(proxyKey string) (recovered bool) {
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

// markDeferred records that a warm client had an update withheld, so the next
// coherent publish counts as a recovery.
func (g *firstPublishGate) markDeferred(proxyKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.clients[proxyKey]
	if st == nil {
		st = &firstPublishState{}
		g.clients[proxyKey] = st
	}
	st.deferredSinceLastPublish = true
}

// clientDeparted drops all first-publish bookkeeping for a client whose
// wrapper row was deleted (client disconnected or its gateway snapshot went
// away). Without this, pending timers could publish to a key after its client
// left, and the bookkeeping map would grow with departed clients.
func (g *firstPublishGate) clientDeparted(proxyKey string) {
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
