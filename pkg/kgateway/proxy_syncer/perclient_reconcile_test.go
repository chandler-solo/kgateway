package proxy_syncer

import (
	"context"
	"testing"
	"time"

	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func reconcileTestWrapper(key string) XdsSnapWrapper {
	return XdsSnapWrapper{proxyKey: key, snap: &envoycache.Snapshot{}}
}

func newReconcileTestCache() envoycache.SnapshotCache {
	return envoycache.NewSnapshotCache(false, envoycache.IDHash{}, nil)
}

func commitClean(t *testing.T, r *perClientReconciler, key string) bool {
	t.Helper()
	published, recovered := r.commitPublish(context.Background(), reconcileTestWrapper(key), false, nil)
	require.True(t, published)
	return recovered
}

func commitDegraded(t *testing.T, r *perClientReconciler, key string) bool {
	t.Helper()
	published, recovered := r.commitPublish(context.Background(), reconcileTestWrapper(key), true, nil)
	require.True(t, published)
	return recovered
}

// Recovery accounting: only a CLEAN publish after a withhold or a degraded
// publish counts as a recovery.
func TestPerClientReconciler_RecoveryTracking(t *testing.T) {
	r := newPerClientReconciler(newReconcileTestCache(), nil, time.Minute, 10*time.Second)
	const key = "client-a"

	require.False(t, commitClean(t, r, key), "first publish is not a recovery")

	r.observeWithheld(key, nil)
	require.True(t, commitClean(t, r, key), "clean publish after a withhold is a recovery")
	require.False(t, commitClean(t, r, key), "clean publish without an intervening defer is not a recovery")

	r.observeWithheld(key, nil)
	require.False(t, commitDegraded(t, r, key), "a degraded publish never counts as a recovery")
	require.True(t, commitClean(t, r, key), "clean publish after a degraded publish is a recovery")
}

// hasStuckClients must catch every stuck shape for CONNECTED clients —
// withheld, degraded, never-published — and must NOT fire for departed clients
// or clients whose role has nothing to publish.
func TestPerClientReconciler_HasStuckClients(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	a := heartbeatTestClient("role-a")
	b := heartbeatTestClient("role-b")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{a}, krtopts.ToOptions("UniqueClients")...)
	r := newPerClientReconciler(newReconcileTestCache(), uccs, time.Minute, 10*time.Second)

	require.True(t, r.hasStuckClients(), "connected-but-never-published client must count as stuck")

	commitClean(t, r, a.ResourceName())
	require.False(t, r.hasStuckClients(), "cleanly published client is not stuck")

	r.observeWithheld(a.ResourceName(), nil)
	require.True(t, r.hasStuckClients(), "withheld client is stuck")

	commitDegraded(t, r, a.ResourceName())
	require.True(t, r.hasStuckClients(), "degraded client stays stuck until a clean publish")

	commitClean(t, r, a.ResourceName())
	require.False(t, r.hasStuckClients(), "recovered client is not stuck")

	// A new client connecting is stuck until its first publish.
	uccs.UpdateObject(b)
	requireListLen(t, uccs, 2)
	require.True(t, r.hasStuckClients(), "newly connected client must count as stuck until first publish")
	commitClean(t, r, b.ResourceName())
	require.False(t, r.hasStuckClients())
}

// A connected client whose role has no per-gateway snapshot (orphaned Envoy,
// unknown role) must NOT count as stuck: no recompute can publish for it, so
// counting it would keep the heartbeat firing forever.
func TestPerClientReconciler_UnpublishableRoleNotStuck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	orphan := heartbeatTestClient("role-orphan")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{orphan}, krtopts.ToOptions("UniqueClients")...)
	r := newPerClientReconciler(newReconcileTestCache(), uccs, time.Minute, 10*time.Second)
	r.roleHasSnapshot = func(role string) bool { return role != "role-orphan" }

	require.False(t, r.hasStuckClients(), "client with no publishable role must not count as stuck")

	r.roleHasSnapshot = func(string) bool { return true }
	require.True(t, r.hasStuckClients(), "same client counts as stuck once its role has a snapshot")
}

// A snapshot Delete for a still-connected client is a defer (stuck, heal); for
// a departed client it only starts the reclaim clock — routine pod churn must
// not keep the heartbeat firing for the grace window.
func TestPerClientReconciler_DeleteClassifiesDeferVsDeparture(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	a := heartbeatTestClient("role-a")
	b := heartbeatTestClient("role-b")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{a, b}, krtopts.ToOptions("UniqueClients")...)
	r := newPerClientReconciler(newReconcileTestCache(), uccs, time.Minute, 10*time.Second)
	commitClean(t, r, a.ResourceName())
	commitClean(t, r, b.ResourceName())
	require.False(t, r.hasStuckClients())

	// a's snapshot row deleted while a is still connected: a transform defer.
	r.observeSnapshotDelete(a.ResourceName())
	require.True(t, r.hasStuckClients(), "transform defer for a live client must mark it stuck")
	commitClean(t, r, a.ResourceName())

	// b departs, then its snapshot row is deleted: NOT stuck.
	uccs.DeleteObject(b.ResourceName())
	requireListLen(t, uccs, 1)
	r.observeSnapshotDelete(b.ResourceName())
	require.False(t, r.hasStuckClients(), "a departed client must not mark the fleet stuck")
}

func newReclaimTestCache(t *testing.T, ctx context.Context, keys ...string) envoycache.SnapshotCache {
	t.Helper()
	cache := envoycache.NewSnapshotCache(false, envoycache.IDHash{}, nil)
	for _, k := range keys {
		require.NoError(t, cache.SetSnapshot(ctx, k, &envoycache.Snapshot{}))
	}
	return cache
}

// A client absent from uccCol is reclaimed only after the grace period; a connected
// client is never touched.
func TestPerClientReconciler_ReclaimsDepartedAfterGrace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	live := heartbeatTestClient("role-live")
	gone := heartbeatTestClient("role-gone")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{live}, krtopts.ToOptions("UniqueClients")...)
	cache := newReclaimTestCache(t, ctx, live.ResourceName(), gone.ResourceName())

	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := newPerClientReconciler(cache, uccs, time.Minute, 10*time.Second)
	r.now = clk.now
	commitClean(t, r, live.ResourceName())
	commitClean(t, r, gone.ResourceName())

	// First pass only records the orphan timer for the departed client.
	require.Empty(t, r.reconcile())
	requireSnapshotPresent(t, cache, gone.ResourceName())

	// Still within grace.
	clk.advance(45 * time.Second)
	require.Empty(t, r.reconcile())
	requireSnapshotPresent(t, cache, gone.ResourceName())

	// Past grace: the departed client is reclaimed, the connected one is not.
	clk.advance(30 * time.Second)
	require.Equal(t, []string{gone.ResourceName()}, r.reconcile())
	requireSnapshotAbsent(t, cache, gone.ResourceName())
	requireSnapshotPresent(t, cache, live.ResourceName())

	// Idempotent: nothing left to reclaim.
	require.Empty(t, r.reconcile())
}

// Regression (review finding): state for a departed client that was never
// published — e.g. its only attempts failed hard validation, so it never
// entered warm-up — must still be swept, or hasStuckClients stays true forever
// and the heartbeat recomputes unconditionally for the life of the process.
func TestPerClientReconciler_SweepsNeverPublishedDeparted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	a := heartbeatTestClient("role-a")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{a}, krtopts.ToOptions("UniqueClients")...)
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := newPerClientReconciler(newReconcileTestCache(), uccs, time.Minute, 10*time.Second)
	r.now = clk.now

	// Hard-validation withhold: no pending wrapper, no warm-up clock, never published.
	r.observeWithheld(a.ResourceName(), nil)
	require.True(t, r.hasStuckClients())

	// The client departs.
	uccs.DeleteObject(a.ResourceName())
	requireListLen(t, uccs, 0)
	require.False(t, r.hasStuckClients(), "departed client must not count as stuck even before the sweep")

	// The sweep drops ALL of its state once grace elapses.
	require.Empty(t, r.reconcile()) // starts the orphan clock
	clk.advance(2 * time.Minute)
	require.Empty(t, r.reconcile(), "never-published client has no cache entry to reclaim")
	r.mu.Lock()
	_, tracked := r.clients[a.ResourceName()]
	r.mu.Unlock()
	require.False(t, tracked, "departed never-published client state must be swept, not leaked")
}

// A client that disconnects and reconnects within the grace window is never cleared.
func TestPerClientReconciler_ReconnectWithinGraceNotReclaimed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	a := heartbeatTestClient("role-a")
	b := heartbeatTestClient("role-b")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{a, b}, krtopts.ToOptions("UniqueClients")...)
	cache := newReclaimTestCache(t, ctx, a.ResourceName(), b.ResourceName())

	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := newPerClientReconciler(cache, uccs, time.Minute, 10*time.Second)
	r.now = clk.now
	commitClean(t, r, a.ResourceName())
	commitClean(t, r, b.ResourceName())

	// b disconnects.
	uccs.DeleteObject(b.ResourceName())
	requireListLen(t, uccs, 1)
	clk.advance(45 * time.Second)
	require.Empty(t, r.reconcile()) // orphan timer started for b

	// b reconnects within grace; the orphan timer must reset.
	uccs.UpdateObject(b)
	requireListLen(t, uccs, 2)
	clk.advance(45 * time.Second)
	require.Empty(t, r.reconcile())
	requireSnapshotPresent(t, cache, b.ResourceName())

	// Even well past the original orphan time, b is safe because it is connected.
	clk.advance(5 * time.Minute)
	require.Empty(t, r.reconcile())
	requireSnapshotPresent(t, cache, b.ResourceName())
}

// Regression (review finding): publishing must reset the orphan clock under the
// same lock as SetSnapshot, so a reclaim pass that raced a reconnect cannot
// clear the snapshot it just wrote.
func TestPerClientReconciler_PublishResetsOrphanClock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	a := heartbeatTestClient("role-a")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{}, krtopts.ToOptions("UniqueClients")...)
	cache := newReconcileTestCache()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	r := newPerClientReconciler(cache, uccs, time.Minute, 10*time.Second)
	r.now = clk.now

	// Published, then departed long past grace — eligible for reclaim.
	commitClean(t, r, a.ResourceName())
	require.Empty(t, r.reconcile()) // starts the orphan clock
	clk.advance(2 * time.Minute)

	// The client reconnects and republishes just before the reclaim pass runs
	// against a connected-set view that does not include it yet.
	commitClean(t, r, a.ResourceName())
	require.Empty(t, r.reconcile(), "a fresh publish must restart the orphan grace period")
	requireSnapshotPresent(t, cache, a.ResourceName())
}

// Pending retries: a withheld snapshot is retained for direct re-attempt while
// the client is connected; the sequence guard makes a stale retry a no-op.
func TestPerClientReconciler_PendingRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	a := heartbeatTestClient("role-a")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{a}, krtopts.ToOptions("UniqueClients")...)
	cache := newReconcileTestCache()
	r := newPerClientReconciler(cache, uccs, time.Minute, 10*time.Second)
	key := a.ResourceName()

	require.Empty(t, r.pendingRetries(), "nothing pending initially")

	wrap := reconcileTestWrapper(key)
	r.observeWithheld(key, &wrap)
	pending := r.pendingRetries()
	require.Len(t, pending, 1)
	staleSeq := pending[0].seq

	// A newer event-driven withhold supersedes the retained wrapper; the old
	// retry's commit must abort.
	r.observeWithheld(key, &wrap)
	published, _ := r.commitPublish(context.Background(), wrap, false, &staleSeq)
	require.False(t, published, "a stale retry must not commit")
	requireSnapshotAbsent(t, cache, key)

	// The current retry commits and clears the pending entry.
	current := r.pendingRetries()
	require.Len(t, current, 1)
	published, recovered := r.commitPublish(context.Background(), current[0].wrap, false, &current[0].seq)
	require.True(t, published)
	require.True(t, recovered, "publishing a previously withheld snapshot is a recovery")
	requireSnapshotPresent(t, cache, key)
	require.Empty(t, r.pendingRetries(), "a committed retry must clear the pending entry")

	// An event-driven publish also clears any pending entry, so a retry that
	// lost the race aborts at the seq guard.
	r.observeWithheld(key, &wrap)
	stale := r.pendingRetries()
	require.Len(t, stale, 1)
	commitClean(t, r, key)
	published, _ = r.commitPublish(context.Background(), stale[0].wrap, false, &stale[0].seq)
	require.False(t, published, "a retry must not overwrite a newer event-driven publish")

	// Pending entries for departed clients are not retried.
	r.observeWithheld(key, &wrap)
	uccs.DeleteObject(key)
	requireListLen(t, uccs, 0)
	require.Empty(t, r.pendingRetries(), "departed clients must not be retried")
}

func requireSnapshotPresent(t *testing.T, cache envoycache.SnapshotCache, key string) {
	t.Helper()
	_, err := cache.GetSnapshot(key)
	require.NoErrorf(t, err, "expected snapshot for %q to be present", key)
}

func requireSnapshotAbsent(t *testing.T, cache envoycache.SnapshotCache, key string) {
	t.Helper()
	_, err := cache.GetSnapshot(key)
	require.Errorf(t, err, "expected snapshot for %q to be absent", key)
}

func requireListLen(t *testing.T, c krt.Collection[ir.UniqlyConnectedClient], want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(c.List()) == want
	}, time.Second, 10*time.Millisecond, "connected-client collection never reached %d members", want)
}
