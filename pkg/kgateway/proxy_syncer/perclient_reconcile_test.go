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

// Recovery accounting: a publish counts as a recovery only when it follows a defer.
func TestPerClientReconciler_RecoveryTracking(t *testing.T) {
	r := newPerClientReconciler(nil, nil, time.Minute)

	require.False(t, r.observePublished("client-a"), "first publish is not a recovery")
	r.observeDeferred("client-a")
	require.True(t, r.observePublished("client-a"), "publish after defer is a recovery")
	require.False(t, r.observePublished("client-a"), "publish without an intervening defer is not a recovery")
}

// hasStuckClients must catch both stuck shapes: a client whose latest event was a
// defer, and a connected client that has never published (whose first deferred
// build emits no KRT event at all, so only uccCol membership reveals it).
func TestPerClientReconciler_HasStuckClients(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	a := heartbeatTestClient("role-a")
	b := heartbeatTestClient("role-b")
	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{a}, krtopts.ToOptions("UniqueClients")...)
	r := newPerClientReconciler(nil, uccs, time.Minute)

	require.True(t, r.hasStuckClients(), "connected-but-never-published client must count as stuck")

	r.observePublished(a.ResourceName())
	require.False(t, r.hasStuckClients(), "published client is not stuck")

	r.observeDeferred(a.ResourceName())
	require.True(t, r.hasStuckClients(), "deferred client is stuck")

	r.observePublished(a.ResourceName())
	require.False(t, r.hasStuckClients(), "recovered client is not stuck")

	// A new client connecting is stuck until its first publish.
	uccs.UpdateObject(b)
	requireListLen(t, uccs, 2)
	require.True(t, r.hasStuckClients(), "newly connected client must count as stuck until first publish")
	r.observePublished(b.ResourceName())
	require.False(t, r.hasStuckClients())
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
	r := newPerClientReconciler(cache, uccs, time.Minute)
	r.now = clk.now
	r.observePublished(live.ResourceName())
	r.observePublished(gone.ResourceName())

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
	r := newPerClientReconciler(cache, uccs, time.Minute)
	r.now = clk.now
	r.observePublished(a.ResourceName())
	r.observePublished(b.ResourceName())

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

func requireSnapshotPresent(t *testing.T, cache envoycache.SnapshotCache, key string) {
	t.Helper()
	_, err := cache.GetSnapshot(key)
	require.NoErrorf(t, err, "expected snapshot for %q to be present", key)
}

func requireSnapshotAbsent(t *testing.T, cache envoycache.SnapshotCache, key string) {
	t.Helper()
	_, err := cache.GetSnapshot(key)
	require.Errorf(t, err, "expected snapshot for %q to have been reclaimed", key)
}

func requireListLen(t *testing.T, c krt.Collection[ir.UniqlyConnectedClient], want int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return len(c.List()) == want
	}, time.Second, 10*time.Millisecond, "connected-client collection never reached %d members", want)
}
