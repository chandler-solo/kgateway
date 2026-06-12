package proxy_syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shortenFirstPublishBudget shrinks the budget for the duration of a test.
// Tests using it must not run in parallel.
func shortenFirstPublishBudget(t *testing.T, d time.Duration) {
	t.Helper()
	orig := perClientFirstPublishBudget
	perClientFirstPublishBudget = d
	t.Cleanup(func() { perClientFirstPublishBudget = orig })
}

func newTestTranslator() *ProxyTranslator {
	pt := NewProxyTranslator(envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil))
	return &pt
}

// wrapperWithVersion builds a wrapper whose listener version identifies it,
// so tests can tell which wrapper a published snapshot came from.
const testClientKey = "c1"

func wrapperWithVersion(version string, deferred bool, reasons ...string) XdsSnapWrapper {
	snapshot := &envoycache.Snapshot{}
	snapshot.Resources[envoycachetypes.Listener] = envoycache.NewResources(version, nil)
	return XdsSnapWrapper{
		snap:         snapshot,
		proxyKey:     testClientKey,
		deferred:     deferred,
		deferReasons: reasons,
	}
}

func publishedVersion(t *testing.T, pt *ProxyTranslator) (string, bool) {
	t.Helper()
	snap, err := pt.xdsCache.GetSnapshot(testClientKey)
	if err != nil {
		return "", false
	}
	return snap.GetVersion(resourcev3.ListenerType), true
}

func TestSyncXds_CoherentPublishesImmediately(t *testing.T) {
	pt := newTestTranslator()
	pt.syncXds(context.Background(), wrapperWithVersion("v1", false))

	v, ok := publishedVersion(t, pt)
	require.True(t, ok, "coherent snapshot should publish immediately")
	assert.Equal(t, "v1", v)
}

func TestSyncXds_DeferredToNeverPublishedWaitsThenPublishes(t *testing.T) {
	shortenFirstPublishBudget(t, 50*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapperWithVersion("v1", true, deferReasonMissingClusters))

	_, ok := publishedVersion(t, pt)
	assert.False(t, ok, "deferred snapshot must not publish before the budget expires")

	require.Eventually(t, func() bool {
		_, ok := publishedVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond, "deferred snapshot must publish after the budget expires")

	v, _ := publishedVersion(t, pt)
	assert.Equal(t, "v1", v)
}

func TestSyncXds_LatestDeferredWinsAtBudgetExpiry(t *testing.T) {
	shortenFirstPublishBudget(t, 80*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapperWithVersion("v1", true, deferReasonMissingClusters))
	pt.syncXds(context.Background(), wrapperWithVersion("v2", true, deferReasonMissingClusters))

	require.Eventually(t, func() bool {
		_, ok := publishedVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond)

	v, _ := publishedVersion(t, pt)
	assert.Equal(t, "v2", v, "the most recent deferred wrapper must be the one published")
}

func TestSyncXds_CoherentSupersedesPendingDeferred(t *testing.T) {
	shortenFirstPublishBudget(t, 100*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapperWithVersion("deferred", true, deferReasonMissingEndpoints))
	pt.syncXds(context.Background(), wrapperWithVersion("coherent", false))

	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent", v)

	// Past the original budget, the canceled timer must not have replaced the
	// coherent snapshot with the stale deferred one.
	time.Sleep(200 * time.Millisecond)
	v, _ = publishedVersion(t, pt)
	assert.Equal(t, "coherent", v, "a canceled first-publish timer must never overwrite a coherent snapshot")
}

func TestSyncXds_WarmClientNeverReceivesDeferred(t *testing.T) {
	shortenFirstPublishBudget(t, 50*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapperWithVersion("coherent", false))
	pt.syncXds(context.Background(), wrapperWithVersion("deferred", true, deferReasonMissingClusters))

	// Far past the budget: the warm client must still hold the coherent
	// snapshot — the bound applies only to never-published clients.
	time.Sleep(150 * time.Millisecond)
	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent", v, "warm clients keep their last coherent snapshot, with no time bound")
}

// The bounded publish must never be a dead end: once the budget has expired
// and a deferred snapshot (with synthesized empty CLAs) was published, a later
// coherent snapshot — e.g. the real ClusterLoadAssignments arriving — must
// overwrite it. This is the guard against the classic failure mode where a
// client receives an empty endpoints set and then never observes the real
// endpoints.
func TestSyncXds_CoherentAfterBoundedPublishOverwrites(t *testing.T) {
	shortenFirstPublishBudget(t, 30*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapperWithVersion("deferred-synth", true, deferReasonMissingEndpoints))

	require.Eventually(t, func() bool {
		v, ok := publishedVersion(t, pt)
		return ok && v == "deferred-synth"
	}, 2*time.Second, 5*time.Millisecond, "budget expiry must publish the deferred snapshot")

	// The real inputs arrive: the coherent snapshot must supersede the
	// bounded one.
	pt.syncXds(context.Background(), wrapperWithVersion("coherent-real", false))
	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent-real", v,
		"a coherent snapshot after a bounded publish must overwrite the synthesized one")
}

func TestSyncXds_ClientDepartureCancelsPendingFirstPublish(t *testing.T) {
	shortenFirstPublishBudget(t, 50*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapperWithVersion("v1", true, deferReasonMissingClusters))
	pt.firstPublish.clientDeparted(testClientKey)

	time.Sleep(150 * time.Millisecond)
	_, ok := publishedVersion(t, pt)
	assert.False(t, ok, "a departed client's pending first publish must not fire")
}

func edsClusterResource(name string) envoycachetypes.ResourceWithTTL {
	return envoycachetypes.ResourceWithTTL{
		Resource: &envoyclusterv3.Cluster{
			Name:                 name,
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
		},
	}
}

func TestSynthesizeEmptyEndpointResources(t *testing.T) {
	clusters := map[string]envoycachetypes.ResourceWithTTL{
		"eds-a": edsClusterResource("eds-a"),
		"eds-b": edsClusterResource("eds-b"),
		"static-c": {
			Resource: &envoyclusterv3.Cluster{
				Name:                 "static-c",
				ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC},
			},
		},
	}
	existing := envoycache.Resources{
		Version: "base",
		Items: map[string]envoycachetypes.ResourceWithTTL{
			"eds-a": {Resource: &envoyendpointv3.ClusterLoadAssignment{
				ClusterName: "eds-a",
				Endpoints:   []*envoyendpointv3.LocalityLbEndpoints{{}},
			}},
		},
	}

	out := synthesizeEmptyEndpointResources([]string{"eds-b", "static-c", "not-a-cluster"}, clusters, existing)

	require.Contains(t, out.Items, "eds-b", "missing EDS cluster gets a synthesized CLA")
	synth := out.Items["eds-b"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	assert.Equal(t, "eds-b", synth.GetClusterName())
	assert.Empty(t, synth.GetEndpoints(), "synthesized CLA must be explicitly empty")

	assert.NotContains(t, out.Items, "static-c", "non-EDS clusters need no CLA")
	assert.Len(t, out.Items, 2, "existing CLAs are preserved, unknown names skipped")
	got := out.Items["eds-a"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	assert.NotEmpty(t, got.GetEndpoints(), "existing real CLA must be untouched")

	assert.NotEqual(t, existing.Version, out.Version, "synthesis must change the resources version")
	assert.Equal(t, fmt.Sprintf("base-synth-%d", 0)[:5], out.Version[:5], "version derives from the base version")
}
