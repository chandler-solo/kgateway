package proxy_syncer

import (
	"context"
	"slices"
	"sync"
	"time"

	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

// perClientFirstPublishBudget bounds how long a client that has NEVER been
// published a snapshot and has not reported a prior xDS version waits for its
// per-client inputs to become coherent. Within the budget a deferred snapshot
// is withheld (inputs usually converge within a recompute or two). Once it
// expires the latest deferred snapshot is published anyway: an Envoy that has
// not reported any accepted config is likely still starting, and keeping it at
// no listeners can leave the pod unable to report Ready and crash-looping.
//
// Clients that either already hold a published snapshot in this controller or
// report a prior accepted xDS version are never sent a deferred one (no time
// bound): keeping the last coherent config is the correct degradation for a
// warm proxy, and publishing an incomplete snapshot would remove resources it
// is using. This is the #13868 make-before-break guarantee, unchanged.
//
// Unexported var (not a setting) so tests can shorten it; there is
// deliberately no operator-facing knob in this minimal bound.
var perClientFirstPublishBudget = 15 * time.Second

const perClientWarmWithheldLogInterval = 5 * time.Minute

// firstPublishGate bounds the first publish for never-published clients. It is
// shared by reference across ProxyTranslator copies.
type firstPublishGate struct {
	mu      sync.Mutex
	pending map[string]*pendingFirstPublish
	// deferredSince marks clients that have had an update withheld since their
	// last coherent publish, so the next coherent publish counts as a recovery.
	deferredSince map[string]struct{}
	// warmWithheld tracks deferred snapshots withheld from clients that may
	// already be serving traffic. It is intentionally in-memory and per-client;
	// Prometheus metrics stay aggregate to avoid high-cardinality UCC labels.
	warmWithheld map[string]*warmWithheldDeferred
}

type pendingFirstPublish struct {
	snap  *envoycache.Snapshot
	timer *time.Timer
}

type warmWithheldDeferred struct {
	since      time.Time
	lastLog    time.Time
	reasons    []string
	warmReason string
}

func newFirstPublishGate() *firstPublishGate {
	return &firstPublishGate{
		pending:       make(map[string]*pendingFirstPublish),
		deferredSince: make(map[string]struct{}),
		warmWithheld:  make(map[string]*warmWithheldDeferred),
	}
}

// syncXds applies the publication policy for one wrapper event.
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) {
	proxyKey := snapWrap.proxyKey
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey, "deferred", snapWrap.deferred)
	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	if !snapWrap.deferred {
		// Coherent: publish, and cancel any pending first-publish under the
		// gate lock so a concurrently-firing timer cannot overwrite it.
		if s.firstPublish.publishCoherent(ctx, s.xdsCache, proxyKey, snapWrap.snap) {
			recordSnapshotRecovery(proxyKey)
		}
		return
	}

	recordSnapshotDefer(proxyKey, snapWrap.deferReasons)
	s.firstPublish.markDeferred(proxyKey)

	// Safety/liveness split: without a real KRT quiescence signal, publishing a
	// deferred snapshot to a client that may already be serving can regress
	// #13868. Only clients with no evidence of prior config get the bounded
	// first-publish liveness escape hatch below.
	if _, err := s.xdsCache.GetSnapshot(proxyKey); err == nil {
		// Warm client: withhold, unbounded (keep last-good).
		s.firstPublish.withholdWarm(proxyKey, snapWrap.deferReasons, warmReasonLocalCache)
		return
	}
	if s.hasPriorXDSVersion(proxyKey) {
		// Warm reconnect to this controller: the local cache has no snapshot,
		// but Envoy's initial xDS request carried a prior accepted version.
		// Treat it as already serving and preserve #13868's make-before-break
		// behavior by withholding deferred snapshots indefinitely.
		s.firstPublish.withholdWarm(proxyKey, snapWrap.deferReasons, warmReasonPriorXDSVersion)
		return
	}

	// Never-published, no-prior-version client: hold up to the budget, then publish.
	s.firstPublish.offer(ctx, s.xdsCache, proxyKey, snapWrap.snap)
}

func (s *ProxyTranslator) hasPriorXDSVersion(proxyKey string) bool {
	return s.xdsClientState != nil && s.xdsClientState.HasPriorXDSVersion(proxyKey)
}

// publishCoherent publishes a coherent snapshot, cancels any pending
// first-publish, and reports whether the client had deferred since its last
// coherent publish (so the caller can count a recovery).
func (g *firstPublishGate) publishCoherent(ctx context.Context, cache envoycache.SnapshotCache, proxyKey string, snap *envoycache.Snapshot) (recovered bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.pending[proxyKey]; st != nil {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(g.pending, proxyKey)
	}
	if _, ok := g.deferredSince[proxyKey]; ok {
		recovered = true
		delete(g.deferredSince, proxyKey)
	}
	delete(g.warmWithheld, proxyKey)
	cache.SetSnapshot(ctx, proxyKey, snap)
	return recovered
}

// markDeferred records that a client had an update withheld, so its next
// coherent publish counts as a recovery.
func (g *firstPublishGate) markDeferred(proxyKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deferredSince[proxyKey] = struct{}{}
}

func (g *firstPublishGate) withholdWarm(proxyKey string, reasons []string, warmReason string) {
	recordDeferredSnapshotWithheld(proxyKey, reasons, warmReason)

	now := time.Now()
	g.mu.Lock()
	st := g.warmWithheld[proxyKey]
	if st == nil {
		st = &warmWithheldDeferred{since: now}
		g.warmWithheld[proxyKey] = st
	}
	reasonsChanged := st.warmReason != warmReason || !slices.Equal(st.reasons, reasons)
	shouldLog := st.lastLog.IsZero() || reasonsChanged || now.Sub(st.lastLog) >= perClientWarmWithheldLogInterval
	st.warmReason = warmReason
	st.reasons = slices.Clone(reasons)
	if shouldLog {
		st.lastLog = now
	}
	since := st.since
	g.mu.Unlock()

	if shouldLog {
		logger.Warn("withholding deferred snapshot for warm xDS client; keeping last coherent config",
			"client", proxyKey,
			"warm_reason", warmReason,
			"defer_reasons", reasons,
			"deferred_since", since,
			"deferred_for", now.Sub(since),
		)
	}
}

// offer records the latest deferred snapshot for a never-published client and
// arms the budget timer once; the timer publishes whatever is latest.
func (g *firstPublishGate) offer(ctx context.Context, cache envoycache.SnapshotCache, proxyKey string, snap *envoycache.Snapshot) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.pending[proxyKey]
	if st == nil {
		st = &pendingFirstPublish{}
		g.pending[proxyKey] = st
		logger.Info("withholding first publish until per-client inputs converge or the budget expires",
			"client", proxyKey, "budget", perClientFirstPublishBudget)
	}
	st.snap = snap
	if st.timer != nil {
		return // already armed; it will publish the latest snap
	}
	st.timer = time.AfterFunc(perClientFirstPublishBudget, func() {
		g.firePending(ctx, cache, proxyKey)
	})
}

// firePending runs at budget expiry: publish the latest deferred snapshot
// unless a coherent snapshot won the race. The check and publish happen under
// the lock, so they cannot interleave with a coherent publish.
func (g *firstPublishGate) firePending(ctx context.Context, cache envoycache.SnapshotCache, proxyKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.pending[proxyKey]
	if st == nil || st.snap == nil {
		return
	}
	snap := st.snap
	delete(g.pending, proxyKey)
	if _, err := cache.GetSnapshot(proxyKey); err == nil {
		return // a coherent snapshot was published while the timer was in flight
	}
	logger.Warn("first-publish budget expired; publishing deferred snapshot so the client can start",
		"client", proxyKey)
	cache.SetSnapshot(ctx, proxyKey, snap)
	recordBoundedPublish(proxyKey)
}

// clientDeparted cancels any pending first publish for a client whose wrapper
// row was deleted (disconnected, or its gateway snapshot went away), so a
// timer cannot publish to a key after its client left.
func (g *firstPublishGate) clientDeparted(proxyKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.pending[proxyKey]; st != nil {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(g.pending, proxyKey)
	}
	delete(g.deferredSince, proxyKey)
	delete(g.warmWithheld, proxyKey)
}
