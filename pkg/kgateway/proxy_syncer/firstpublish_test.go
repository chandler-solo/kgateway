package proxy_syncer

import (
	"context"
	"testing"
	"time"

	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const firstPublishTestClient = "c1"

// stubPriorXDS models the client-state reader: has=false is a cold client,
// has=true a warm reconnect that reported a prior accepted version.
type stubPriorXDS struct{ has bool }

func (s stubPriorXDS) HasPriorXDSVersion(string) bool { return s.has }

func newFirstPublishTestTranslator(prior bool, budget time.Duration) *ProxyTranslator {
	pt := NewProxyTranslator(
		envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil),
		stubPriorXDS{has: prior},
		budget,
	)
	return &pt
}

// deferredWrapperV builds a deferred wrapper whose listener version identifies
// it in assertions; the missing reference is what keeps it deferred.
func deferredWrapperV(version string) XdsSnapWrapper {
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Listener] = envoycache.NewResources(version, nil)
	return XdsSnapWrapper{
		snap:              snap,
		proxyKey:          firstPublishTestClient,
		deferred:          true,
		missingReferenced: []string{"cluster-missing"},
	}
}

func coherentWrapperV(version string) XdsSnapWrapper {
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Listener] = envoycache.NewResources(version, nil)
	return XdsSnapWrapper{snap: snap, proxyKey: firstPublishTestClient}
}

func publishedListenerVersion(t *testing.T, pt *ProxyTranslator) (string, bool) {
	t.Helper()
	snap, err := pt.xdsCache.GetSnapshot(firstPublishTestClient)
	if err != nil {
		return "", false
	}
	return snap.GetVersion(resourcev3.ListenerType), true
}

// A cold, never-published client is withheld within the budget and published
// at expiry so the pod can start.
func TestFirstPublish_ColdClientPublishesAtBudget(t *testing.T) {
	pt := newFirstPublishTestTranslator(false, 50*time.Millisecond)

	pt.syncXds(context.Background(), deferredWrapperV("v1"))

	_, ok := publishedListenerVersion(t, pt)
	assert.False(t, ok, "deferred snapshot must not publish before the budget expires")

	require.Eventually(t, func() bool {
		_, ok := publishedListenerVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond, "deferred snapshot must publish after the budget expires")
	v, _ := publishedListenerVersion(t, pt)
	assert.Equal(t, "v1", v)
}

// The latest deferred snapshot is the one published at budget expiry.
func TestFirstPublish_LatestDeferredWins(t *testing.T) {
	pt := newFirstPublishTestTranslator(false, 80*time.Millisecond)

	pt.syncXds(context.Background(), deferredWrapperV("v1"))
	pt.syncXds(context.Background(), deferredWrapperV("v2"))

	require.Eventually(t, func() bool {
		_, ok := publishedListenerVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond)
	v, _ := publishedListenerVersion(t, pt)
	assert.Equal(t, "v2", v)
}

// A client that reported a prior accepted xDS version is warm: it must stay
// withheld at budget expiry (an incomplete SotW publish would replace config
// it is already serving), but a coherent snapshot still publishes immediately.
func TestFirstPublish_PriorXDSVersionClientStaysWithheld(t *testing.T) {
	pt := newFirstPublishTestTranslator(true, 40*time.Millisecond)

	pt.syncXds(context.Background(), deferredWrapperV("v1"))

	require.Never(t, func() bool {
		_, ok := publishedListenerVersion(t, pt)
		return ok
	}, 250*time.Millisecond, 20*time.Millisecond,
		"a prior-xDS-version (warm reconnect) client must not receive a deferred snapshot at budget expiry")

	pt.syncXds(context.Background(), coherentWrapperV("coherent"))
	v, ok := publishedListenerVersion(t, pt)
	require.True(t, ok, "a coherent snapshot publishes immediately, warm or not")
	assert.Equal(t, "coherent", v)
}

// A coherent snapshot supersedes a pending bounded publish, and the canceled
// timer must never overwrite it.
func TestFirstPublish_CoherentSupersedesPending(t *testing.T) {
	pt := newFirstPublishTestTranslator(false, 100*time.Millisecond)

	pt.syncXds(context.Background(), deferredWrapperV("deferred"))
	pt.syncXds(context.Background(), coherentWrapperV("coherent"))

	v, ok := publishedListenerVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent", v)

	time.Sleep(250 * time.Millisecond) // well past the budget
	v, _ = publishedListenerVersion(t, pt)
	assert.Equal(t, "coherent", v, "a canceled first-publish timer must not overwrite a coherent snapshot")
}

// A departed client's pending bounded publish must not fire.
func TestFirstPublish_DepartureCancelsPending(t *testing.T) {
	pt := newFirstPublishTestTranslator(false, 50*time.Millisecond)

	pt.syncXds(context.Background(), deferredWrapperV("v1"))
	pt.firstPublish.clientDeparted(firstPublishTestClient)

	time.Sleep(200 * time.Millisecond)
	_, ok := publishedListenerVersion(t, pt)
	assert.False(t, ok, "a departed client's pending first publish must not fire")
}

// KGW_PER_CLIENT_PUBLISH_BUDGET=0 is the conservative opt-out: never-published
// clients are withheld with no deadline.
func TestFirstPublish_BudgetZeroDisablesBound(t *testing.T) {
	pt := newFirstPublishTestTranslator(false, 0)

	pt.syncXds(context.Background(), deferredWrapperV("v1"))

	require.Never(t, func() bool {
		_, ok := publishedListenerVersion(t, pt)
		return ok
	}, 250*time.Millisecond, 20*time.Millisecond,
		"with the bound disabled, a deferred snapshot must never publish to a never-published client")
}
