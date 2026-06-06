package proxy_syncer

import (
	"sync"
	"time"

	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

// perClientReconciler bounds two pieces of state the per-client snapshot defer logic
// would otherwise leave unbounded (#14184):
//
//   - The xDS cache leak: snapshotPerClient's Delete branch intentionally never
//     clears the cache (it retains the last-good snapshot during a defer), so a
//     departed client's snapshot is never reclaimed. reconcile() clears entries for
//     clients that have been gone from uccCol longer than grace.
//   - Recovery accounting: it remembers which clients are currently deferred so a
//     later publish can be counted as a recovery -- the signal that the heartbeat
//     (or a late event) healed an otherwise-stranded client.
//
// It reconciles against the set of keys we have actually published (tracked here),
// not the cache's internal status map, so its behavior does not depend on
// go-control-plane watch/status semantics and is straightforward to test.
type perClientReconciler struct {
	xdsCache envoycache.SnapshotCache
	uccs     krt.Collection[ir.UniqlyConnectedClient]
	grace    time.Duration
	now      func() time.Time

	mu            sync.Mutex
	published     map[string]struct{}  // clients we have SetSnapshot for and not cleared
	deferred      map[string]struct{}  // clients whose most recent snapshot event was a defer
	orphanedSince map[string]time.Time // published clients absent from uccCol, first-seen-absent time
}

func newPerClientReconciler(
	xdsCache envoycache.SnapshotCache,
	uccs krt.Collection[ir.UniqlyConnectedClient],
	grace time.Duration,
) *perClientReconciler {
	return &perClientReconciler{
		xdsCache:      xdsCache,
		uccs:          uccs,
		grace:         grace,
		now:           time.Now,
		published:     map[string]struct{}{},
		deferred:      map[string]struct{}{},
		orphanedSince: map[string]time.Time{},
	}
}

// observeDeferred records that client key's most recent snapshot event was a defer.
func (r *perClientReconciler) observeDeferred(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deferred[key] = struct{}{}
}

// observePublished records a publish for client key (we have a live cache entry for
// it) and reports whether it recovered from a prior deferred state.
func (r *perClientReconciler) observePublished(key string) (recovered bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.published[key] = struct{}{}
	if _, wasDeferred := r.deferred[key]; wasDeferred {
		delete(r.deferred, key)
		return true
	}
	return false
}

// hasStuckClients reports whether any connected client is missing a current
// snapshot. Two signals are needed: the deferred map covers clients that
// published once and then deferred (their nil result surfaces as a KRT Delete,
// observed by the syncer), but a brand-new client whose very first build defers
// produces NO event at all — the key never existed, so there is nothing to
// delete. Those clients are visible only as connected-but-never-published, which
// was exactly the stranded-cohort shape seen in production (#14184).
//
// A deferred entry for a client that has since departed can briefly hold this
// true; reconcile() removes such entries once the departure grace elapses, so the
// signal is self-cleaning.
func (r *perClientReconciler) hasStuckClients() bool {
	var live []ir.UniqlyConnectedClient
	if r.uccs != nil {
		live = r.uccs.List()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.deferred) > 0 {
		return true
	}
	for _, ucc := range live {
		if _, ok := r.published[ucc.ResourceName()]; !ok {
			return true
		}
	}
	return false
}

// reconcile clears cache entries for published clients that have been absent from
// uccCol for longer than grace, and returns the keys it reclaimed.
func (r *perClientReconciler) reconcile() []string {
	live := make(map[string]struct{})
	for _, ucc := range r.uccs.List() {
		live[ucc.ResourceName()] = struct{}{}
	}
	now := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	var reclaimed []string
	for key := range r.published {
		if _, ok := live[key]; ok {
			// Still connected: not a candidate, and reset any prior orphan timer
			// (covers a disconnect/reconnect that stayed within grace).
			delete(r.orphanedSince, key)
			continue
		}
		since, seen := r.orphanedSince[key]
		if !seen {
			r.orphanedSince[key] = now
			continue
		}
		if now.Sub(since) >= r.grace {
			r.xdsCache.ClearSnapshot(key)
			delete(r.published, key)
			delete(r.orphanedSince, key)
			delete(r.deferred, key)
			reclaimed = append(reclaimed, key)
		}
	}
	return reclaimed
}
