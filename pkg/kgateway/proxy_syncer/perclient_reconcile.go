package proxy_syncer

import (
	"context"
	"sync"
	"time"

	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

// clientState is everything the reconciler tracks about one connected (or
// recently departed) client, keyed by the UCC resource name. Keeping it in one
// struct (rather than parallel maps) makes the lifecycle single-point: a
// departed client is removed by deleting one entry, so no signal can leak.
type clientState struct {
	// published: we have SetSnapshot for this client and have not cleared it.
	published bool
	// deferred: the most recent attempt for this client withheld publication
	// (publish-time policy) or the snapshot transform deferred (its output was
	// deleted while the client is still connected).
	deferred bool
	// degraded: the last publish succeeded but shipped known-incomplete data
	// (missing referenced clusters, or synthesized empty CLAs). A degraded
	// client counts as stuck so the heartbeat keeps re-running the per-client
	// collections until a clean publish clears it.
	degraded bool
	// incompleteSince: when the current incomplete-inputs deferral episode
	// started; zero when there is none. An episode begins the first time a
	// publish attempt finds the snapshot incomplete, bounds how long updates
	// may be withheld (Envoy keeps its last coherent config meanwhile), and
	// ends only on a CLEAN publish — so once the budget forces a degraded
	// publish, later incomplete updates flow immediately instead of
	// re-deferring.
	incompleteSince time.Time
	// orphanedSince: when the client was first observed absent from the
	// connected set; zero while it is live. Publishing zeroes it (under the
	// same lock as SetSnapshot), so a reconnect-while-reclaiming race cannot
	// clear a snapshot that was just written.
	orphanedSince time.Time
	// pending: the most recent withheld snapshot, retained so the heartbeat
	// loop can re-attempt publication directly — a withheld snapshot produces
	// no KRT event, and an unchanged recompute is hash-suppressed, so without
	// this retained wrapper nothing would ever re-evaluate the warm-up
	// deadline on a quiet cluster.
	pending *XdsSnapWrapper
	// pendingSeq guards retries: a retry may only commit if its sequence still
	// matches, so a newer event-driven publish can never be overwritten by a
	// stale retry.
	pendingSeq uint64
}

// pendingRetry is a snapshot of a withheld publication handed to the retry loop.
type pendingRetry struct {
	wrap XdsSnapWrapper
	seq  uint64
}

// perClientReconciler owns per-client publication state for the per-client xDS
// snapshot path (#14184):
//
//   - Cache reclaim: the snapshot Delete branch intentionally never clears the
//     xDS cache (Envoy keeps the last-good snapshot during a defer), so a
//     departed client's entry is reclaimed here after a grace period instead.
//   - Stuck detection: deferred, degraded, and pending-withheld clients (plus
//     connected-but-never-published clients whose role has a snapshot to
//     publish) drive the demand-driven heartbeat.
//   - Incomplete-inputs gating: a publish attempt whose snapshot is missing
//     referenced clusters or had CLAs synthesized may be deferred, bounded by
//     incompleteBudget per episode. This covers both the reconnect race
//     #13868 addressed (a cold client's first snapshot) and transiently
//     incomplete rebuilds for already-published clients, where publishing
//     would regress Envoy's state-of-the-world config.
//   - Publication commit: SetSnapshot and the bookkeeping that depends on it
//     happen atomically under one lock, so reclaim and publish are totally
//     ordered.
//
// It reconciles against the set of keys it has published (tracked here), not
// the cache's internal status map, so its behavior does not depend on
// go-control-plane watch/status semantics and is straightforward to test.
type perClientReconciler struct {
	xdsCache         envoycache.SnapshotCache
	uccs             krt.Collection[ir.UniqlyConnectedClient]
	grace            time.Duration
	incompleteBudget time.Duration
	now              func() time.Time
	// roleHasSnapshot reports whether a per-gateway snapshot exists for the
	// role, i.e. whether a connected-but-never-published client could publish
	// at all. Nil means "assume yes". Without this, an orphaned Envoy whose
	// Gateway was deleted (or one connected with an unknown role) would count
	// as stuck forever and keep the heartbeat recomputing pointlessly.
	roleHasSnapshot func(role string) bool

	mu      sync.Mutex
	clients map[string]*clientState
	seq     uint64
}

func newPerClientReconciler(
	xdsCache envoycache.SnapshotCache,
	uccs krt.Collection[ir.UniqlyConnectedClient],
	grace time.Duration,
	incompleteBudget time.Duration,
) *perClientReconciler {
	return &perClientReconciler{
		xdsCache:         xdsCache,
		uccs:             uccs,
		grace:            grace,
		incompleteBudget: incompleteBudget,
		now:              time.Now,
		clients:          map[string]*clientState{},
	}
}

// state returns the tracked state for key, creating it if absent. Callers must
// hold r.mu.
func (r *perClientReconciler) state(key string) *clientState {
	st, ok := r.clients[key]
	if !ok {
		st = &clientState{}
		r.clients[key] = st
	}
	return st
}

// shouldDeferIncomplete reports whether a publish attempt with incomplete
// inputs (missing referenced clusters, synthesized CLAs) should be withheld.
// The first call of an episode starts the clock; within the budget Envoy
// keeps its last coherent config (or, for a cold client, none yet — the
// reconnect race #13868 addressed) while events or heartbeat recomputes heal
// the inputs. Past the budget the caller publishes the best available
// snapshot, marked degraded; the episode ends only on a clean publish, so
// post-budget incomplete updates flow without re-deferring.
//
// The deadline bounds deferral only together with the pending-retry loop:
// expiry is observed by re-attempting publication, and with no input changes
// there is no KRT event to do that, so the heartbeat loop retries the retained
// pending snapshot each tick (see pendingRetries). The effective bound is
// therefore incompleteBudget rounded up to the next heartbeat tick.
func (r *perClientReconciler) shouldDeferIncomplete(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state(key)
	if st.incompleteSince.IsZero() {
		st.incompleteSince = r.now()
		return true
	}
	return r.now().Sub(st.incompleteSince) < r.incompleteBudget
}

// observeWithheld records that publication for key was withheld. pending, when
// non-nil, is retained for direct retry by the heartbeat loop; pass nil for
// withholds that retrying the same data cannot heal (an invalid snapshot needs
// a new build, which arrives as a regular event).
func (r *perClientReconciler) observeWithheld(key string, pending *XdsSnapWrapper) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state(key)
	st.deferred = true
	if pending != nil {
		r.seq++
		st.pending = pending
		st.pendingSeq = r.seq
	}
}

// observeSnapshotDelete handles a Delete event from the per-client snapshot
// collection, which means either the transform deferred (client still
// connected) or the client departed. Only the former marks the client stuck;
// a departure just starts the orphan clock so routine pod churn does not make
// the heartbeat fire unconditionally for the whole reclaim grace window.
func (r *perClientReconciler) observeSnapshotDelete(key string) {
	live := r.uccs != nil && r.uccs.GetKey(key) != nil
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state(key)
	if live {
		st.deferred = true
		return
	}
	if st.orphanedSince.IsZero() {
		st.orphanedSince = r.now()
	}
}

// commitPublish atomically publishes snapWrap's snapshot to the xDS cache and
// records the outcome. It returns whether the snapshot was published and
// whether this publish recovered the client from a prior deferred or degraded
// state (a degraded publish never counts as a recovery).
//
// expectSeq non-nil marks a retry of a previously withheld snapshot: the
// commit is aborted unless the retained pending entry still matches, so a
// retry can never overwrite a newer event-driven publish.
//
// Holding r.mu across SetSnapshot totally orders publication against
// reconcile()'s ClearSnapshot, and zeroing orphanedSince here means a reclaim
// pass can only clear entries whose last publish predates the grace period —
// closing the reconnect-during-reclaim race. ClearSnapshot/SetSnapshot are map
// operations on the snapshot cache, so the added lock hold is small.
func (r *perClientReconciler) commitPublish(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
	degraded bool,
	expectSeq *uint64,
) (published bool, recovered bool) {
	key := snapWrap.proxyKey
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state(key)
	if expectSeq != nil && (st.pending == nil || st.pendingSeq != *expectSeq) {
		// A newer event superseded this retry; its own publish (or withhold)
		// already recorded the current state.
		return false, false
	}
	if err := r.xdsCache.SetSnapshot(ctx, key, snapWrap.snap); err != nil {
		logger.Error("failed to set xds snapshot", "proxy_key", key, "error", err)
		st.deferred = true
		if expectSeq == nil {
			r.seq++
			st.pending = &snapWrap
			st.pendingSeq = r.seq
		}
		return false, false
	}
	recovered = (st.deferred || st.degraded) && !degraded
	st.published = true
	st.deferred = false
	st.degraded = degraded
	st.pending = nil
	if !degraded {
		// Only a clean publish ends the incomplete-inputs episode; a degraded
		// (post-budget) publish keeps it open so further incomplete updates
		// flow immediately instead of starting a fresh deferral.
		st.incompleteSince = time.Time{}
	}
	st.orphanedSince = time.Time{}
	return true, recovered
}

// pendingRetries returns the withheld snapshots eligible for a direct publish
// re-attempt: those whose client is still connected. The caller re-attempts
// each via the publish path; the seq guard in commitPublish makes a stale
// retry a no-op.
func (r *perClientReconciler) pendingRetries() []pendingRetry {
	// A nil connected-client collection (unit tests) means "treat everything
	// as live"; production always wires one.
	var live map[string]struct{}
	if r.uccs != nil {
		live = map[string]struct{}{}
		for _, ucc := range r.uccs.List() {
			live[ucc.ResourceName()] = struct{}{}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []pendingRetry
	for key, st := range r.clients {
		if st.pending == nil {
			continue
		}
		if live != nil {
			if _, ok := live[key]; !ok {
				continue
			}
		}
		out = append(out, pendingRetry{wrap: *st.pending, seq: st.pendingSeq})
	}
	return out
}

// hasStuckClients reports whether any CONNECTED client is missing a current,
// clean snapshot. Deferred/degraded/pending states cover clients we have seen
// events or publish attempts for; the never-published check covers a brand-new
// client whose very first build defers — that produces NO event at all (the
// key never existed, so there is nothing to delete), which was exactly the
// stranded-cohort shape seen in production (#14184). Clients whose role has no
// per-gateway snapshot (an orphaned Envoy after Gateway deletion, an unknown
// role) are excluded: no amount of per-client recomputation can publish for
// them, so counting them would keep the heartbeat firing forever.
func (r *perClientReconciler) hasStuckClients() bool {
	var live []ir.UniqlyConnectedClient
	if r.uccs != nil {
		live = r.uccs.List()
	}
	liveKeys := make(map[string]struct{}, len(live))
	for _, ucc := range live {
		liveKeys[ucc.ResourceName()] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, st := range r.clients {
		if _, ok := liveKeys[key]; !ok {
			continue
		}
		if st.deferred || st.degraded || st.pending != nil {
			return true
		}
	}
	for _, ucc := range live {
		st, ok := r.clients[ucc.ResourceName()]
		if ok && st.published {
			continue
		}
		if r.roleHasSnapshot != nil && !r.roleHasSnapshot(ucc.Role) {
			continue
		}
		return true
	}
	return false
}

// reconcile sweeps the tracked state against the connected set: live clients
// get their orphan clock reset; departed clients past the grace period have
// their cache entry cleared (if published) and their state dropped — ALL state,
// so no signal (deferred marks, warm-up clocks, pending retries) can outlive
// the client that produced it. It returns the keys whose cache entries were
// reclaimed.
func (r *perClientReconciler) reconcile() []string {
	live := make(map[string]struct{})
	if r.uccs != nil {
		for _, ucc := range r.uccs.List() {
			live[ucc.ResourceName()] = struct{}{}
		}
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	var reclaimed []string
	for key, st := range r.clients {
		if _, ok := live[key]; ok {
			// Still connected: not a candidate, and reset any prior orphan timer
			// (covers a disconnect/reconnect that stayed within grace).
			st.orphanedSince = time.Time{}
			continue
		}
		if st.orphanedSince.IsZero() {
			st.orphanedSince = now
			continue
		}
		if now.Sub(st.orphanedSince) >= r.grace {
			if st.published {
				r.xdsCache.ClearSnapshot(key)
				reclaimed = append(reclaimed, key)
			}
			delete(r.clients, key)
		}
	}
	return reclaimed
}
