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
	// deferred: the snapshot transform deferred for this client (its output
	// was deleted while the client is still connected) — its per-client
	// inputs are not yet coherent, and Envoy keeps the last published
	// snapshot (or has none yet) until they are.
	deferred bool
	// orphanedSince: when the client was first observed absent from the
	// connected set; zero while it is live. Publishing zeroes it (under the
	// same lock as SetSnapshot), so a reconnect-while-reclaiming race cannot
	// clear a snapshot that was just written.
	orphanedSince time.Time
}

// perClientReconciler owns per-client publication state for the per-client xDS
// snapshot path (#14184):
//
//   - Cache reclaim: the snapshot Delete branch intentionally never clears the
//     xDS cache (Envoy keeps the last-good snapshot during a defer), so a
//     departed client's entry is reclaimed here after a grace period instead.
//   - Stuck detection: deferred clients and connected-but-never-published
//     clients (whose role has a snapshot to publish) drive the demand-driven
//     heartbeat, which re-runs the per-client collections so the
//     wait-for-consistency gates in snapshotPerClient reliably terminate.
//   - Publication commit: SetSnapshot and the bookkeeping that depends on it
//     happen atomically under one lock, so reclaim and publish are totally
//     ordered.
//
// It reconciles against the set of keys it has published (tracked here), not
// the cache's internal status map, so its behavior does not depend on
// go-control-plane watch/status semantics and is straightforward to test.
type perClientReconciler struct {
	xdsCache envoycache.SnapshotCache
	uccs     krt.Collection[ir.UniqlyConnectedClient]
	grace    time.Duration
	now      func() time.Time
	// roleHasSnapshot reports whether a per-gateway snapshot exists for the
	// role, i.e. whether a connected-but-never-published client could publish
	// at all. Nil means "assume yes". Without this, an orphaned Envoy whose
	// Gateway was deleted (or one connected with an unknown role) would count
	// as stuck forever and keep the heartbeat recomputing pointlessly.
	roleHasSnapshot func(role string) bool

	mu      sync.Mutex
	clients map[string]*clientState
}

func newPerClientReconciler(
	xdsCache envoycache.SnapshotCache,
	uccs krt.Collection[ir.UniqlyConnectedClient],
	grace time.Duration,
) *perClientReconciler {
	return &perClientReconciler{
		xdsCache: xdsCache,
		uccs:     uccs,
		grace:    grace,
		now:      time.Now,
		clients:  map[string]*clientState{},
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
// records the outcome, reporting whether this publish recovered the client
// from a prior deferred state.
//
// Holding r.mu across SetSnapshot totally orders publication against
// reconcile()'s ClearSnapshot, and zeroing orphanedSince here means a reclaim
// pass can only clear entries whose last publish predates the grace period —
// closing the reconnect-during-reclaim race. ClearSnapshot/SetSnapshot are map
// operations on the snapshot cache, so the added lock hold is small.
func (r *perClientReconciler) commitPublish(ctx context.Context, snapWrap XdsSnapWrapper) (recovered bool) {
	key := snapWrap.proxyKey
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state(key)
	if err := r.xdsCache.SetSnapshot(ctx, key, snapWrap.snap); err != nil {
		logger.Error("failed to set xds snapshot", "proxy_key", key, "error", err)
		st.deferred = true
		return false
	}
	recovered = st.deferred
	st.published = true
	st.deferred = false
	st.orphanedSince = time.Time{}
	return recovered
}

// hasStuckClients reports whether any CONNECTED client is waiting on its
// per-client inputs. The deferred flag covers clients whose snapshot row was
// deleted by a defer; the never-published check covers a brand-new client
// whose very first build deferred — that produces NO event at all (the key
// never existed, so there is nothing to delete), which was exactly the
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
		if st.deferred {
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
// so no signal (deferred marks included) can outlive the client that produced
// it. It returns the keys whose cache entries were reclaimed.
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
