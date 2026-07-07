package proxy_syncer

import (
	"context"
	"sync"
	"time"

	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
)

// firstPublishGate bounds how long a client that has NEVER been published a
// snapshot waits for its referenced clusters to become ready.
//
// The per-cluster resolution in syncXds withholds a deferred snapshot from a
// never-published client: there is no last-good config to hold or carry, and
// publishing routes to unready clusters would 503 them. That is correct for
// the brief converging case, but with no bound a permanently-unready
// reference (a backend that is down, a misconfiguration) leaves the pod with
// no listeners at all — it never reports Ready and crash-loops, and each
// restart reconnects as a new client that re-triggers the per-client fan-out.
//
// The gate arms one timer per never-published client. If the client's inputs
// cohere within the budget, the coherent publish wins and the timer is
// discarded. At expiry the latest deferred snapshot is published as-is — it
// is always internally consistent (missing EDS references carry synthesized
// empty assignments), so the pod binds its listeners and becomes Ready while
// routes to still-unready clusters return 503 until they cohere.
//
// One exception: a client that reported a prior accepted xDS version on
// connect is warm — an Envoy that reconnected (or outlived a controller
// restart) and is still serving config from its previous stream. Publishing
// a snapshot whose routes name clusters absent from its CDS would replace
// that working config with one that NCs those routes, so warm clients stay
// withheld while any referenced cluster is MISSING (the #13868
// make-before-break property). A controller-local cache miss alone cannot
// prove a client is cold; the client-reported version can.
//
// The warm withhold deliberately does NOT extend to gaps that are only
// endpoint-less clusters (unusableReferenced). Incoherence can be steady
// state, not just transient (#14352): an ExternalName Service never has
// EndpointSlices, and a scale-to-zero backend's zero endpoints IS its truth.
// syncXds events only fire after the informer chain has synced (krt
// registration semantics), so a CLA still empty a full budget later is the
// backend's steady state, not propagation lag. Withholding on it would
// freeze the warm client forever after a controller restart — no route
// change, cert rotation, or endpoint update would ever reach it — and would
// starve any cold pod that shares its UCC key (the prior-version mark is
// key-level). So at expiry, if every referenced cluster is present in CDS,
// the snapshot publishes as the current truth; in the rare case the
// emptiness was lag, the very next build repairs it through per-cluster
// resolution (the cache is populated from here on).
//
// All snapshot-cache mutations for gated clients go through the gate's lock,
// so an expiring timer can never overwrite a newer coherent publish.
type firstPublishGate struct {
	// budget is how long a never-published client's deferred snapshot is
	// withheld before the bounded publish; 0 disables the bound (withhold
	// until coherent, with no deadline). Written once at construction.
	budget time.Duration

	mu      sync.Mutex
	pending map[string]*pendingFirstPublish
}

type pendingFirstPublish struct {
	// snap is the latest deferred snapshot; the timer publishes whatever is
	// latest at expiry.
	snap *envoycache.Snapshot
	// missing and unusable are the latest recorded gaps, for the expiry log.
	missing  []string
	unusable []string
	timer    *time.Timer
}

func newFirstPublishGate(budget time.Duration) *firstPublishGate {
	return &firstPublishGate{
		budget:  budget,
		pending: make(map[string]*pendingFirstPublish),
	}
}

// publish publishes a coherent (or per-cluster-resolved) snapshot and cancels
// any pending bounded first publish, under one lock so a concurrently-expiring
// timer cannot overwrite the newer snapshot.
func (g *firstPublishGate) publish(ctx context.Context, cache envoycache.SnapshotCache, proxyKey string, snap *envoycache.Snapshot) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.pending[proxyKey]; st != nil {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(g.pending, proxyKey)
	}
	return cache.SetSnapshot(ctx, proxyKey, snap)
}

// offerCold records the latest deferred snapshot for a never-published client
// and arms the budget timer once per episode; the timer publishes whatever is
// latest unless the client turns out to be warm (prior xDS version) or a
// coherent publish got there first.
func (g *firstPublishGate) offerCold(
	ctx context.Context,
	cache envoycache.SnapshotCache,
	snapWrap XdsSnapWrapper,
	hasPriorXDSVersion func(string) bool,
) {
	if g.budget <= 0 {
		return // bound disabled: withhold until coherent
	}
	proxyKey := snapWrap.proxyKey
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.pending[proxyKey]
	if st == nil {
		st = &pendingFirstPublish{}
		g.pending[proxyKey] = st
		logger.Info("withholding first publish until referenced clusters are ready or the budget expires",
			"proxy_key", proxyKey, "budget", g.budget,
			"missing_clusters", snapWrap.missingReferenced,
			"unusable_clusters", snapWrap.unusableReferenced,
		)
	}
	st.snap = snapWrap.snap
	st.missing = snapWrap.missingReferenced
	st.unusable = snapWrap.unusableReferenced
	if st.timer != nil {
		return // already armed; it will publish the latest snap
	}
	st.timer = time.AfterFunc(g.budget, func() {
		g.firePending(ctx, cache, proxyKey, hasPriorXDSVersion)
	})
}

// firePending runs at budget expiry: publish the latest deferred snapshot,
// unless a coherent snapshot won the race, or the client reported a prior xDS
// version (warm reconnect) AND some referenced cluster is missing from CDS —
// publishing that would NC routes the proxy is still serving. A warm client
// whose only gaps are endpoint-less clusters publishes anyway: by expiry that
// emptiness is the backends' steady state (#14352), and withholding would
// freeze the client indefinitely (see the type comment). The check and publish
// happen under the gate lock, so they cannot interleave with publish().
func (g *firstPublishGate) firePending(
	ctx context.Context,
	cache envoycache.SnapshotCache,
	proxyKey string,
	hasPriorXDSVersion func(string) bool,
) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.pending[proxyKey]
	if st == nil || st.snap == nil {
		return
	}
	// Keep the entry so a later deferred wrapper of the same episode re-arms
	// the timer instead of restarting the accounting.
	snap := st.snap
	st.snap = nil
	st.timer = nil
	if _, err := cache.GetSnapshot(proxyKey); err == nil {
		return // a coherent snapshot was published while the timer was in flight
	}
	mode := boundedPublishFirstPublish
	if hasPriorXDSVersion != nil && hasPriorXDSVersion(proxyKey) {
		if len(st.missing) > 0 {
			logger.Warn("first-publish budget expired, but client reported a prior xDS version and referenced clusters are missing; withholding deferred snapshot",
				"proxy_key", proxyKey, "missing_clusters", st.missing, "unusable_clusters", st.unusable)
			recordDeferredWithheld(proxyKey)
			return
		}
		// Warm client, but every referenced cluster is present in CDS and the
		// only gaps are endpoint-less clusters: by now that is the backends'
		// steady state (ExternalName, scale-to-zero — #14352), not lag, and
		// withholding would freeze this client's config indefinitely.
		mode = boundedPublishWarmTruth
		logger.Warn("first-publish budget expired for warm client; remaining gaps are endpoint-less clusters, publishing their current truth",
			"proxy_key", proxyKey, "unusable_clusters", st.unusable)
	} else {
		logger.Warn("first-publish budget expired; publishing deferred snapshot so the client can start",
			"proxy_key", proxyKey, "missing_clusters", st.missing, "unusable_clusters", st.unusable)
	}
	if err := cache.SetSnapshot(ctx, proxyKey, snap); err != nil {
		logger.Error("failed to set xds snapshot", "proxy_key", proxyKey, "error", err)
		return
	}
	recordBoundedPublish(proxyKey, mode)
}

// clientDeparted cancels any pending bounded first publish for a client whose
// wrapper row was deleted, so a timer cannot publish to a key after its client
// left. A still-connected client re-arms the gate with its next deferred
// wrapper.
func (g *firstPublishGate) clientDeparted(proxyKey string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st := g.pending[proxyKey]; st != nil {
		if st.timer != nil {
			st.timer.Stop()
		}
		delete(g.pending, proxyKey)
	}
}
