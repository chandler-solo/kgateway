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

	"github.com/kgateway-dev/kgateway/v2/pkg/metrics"
	"github.com/kgateway-dev/kgateway/v2/pkg/metrics/metricstest"
)

// deferredWrapper is a deferred wrapper carrying a defer reason for the metric.
func deferredWrapper(version, reason string) XdsSnapWrapper {
	w := wrapper(version, true)
	w.deferReasons = []string{reason}
	return w
}

const testClientKey = "c1"

func newTestTranslator() *ProxyTranslator {
	pt := NewProxyTranslator(envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil))
	return &pt
}

func shortenBudget(t *testing.T, d time.Duration) {
	t.Helper()
	orig := perClientFirstPublishBudget
	perClientFirstPublishBudget = d
	t.Cleanup(func() { perClientFirstPublishBudget = orig })
}

// wrapper builds a wrapper whose listener version identifies it in assertions.
func wrapper(version string, deferred bool) XdsSnapWrapper {
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Listener] = envoycache.NewResources(version, nil)
	return XdsSnapWrapper{snap: snap, proxyKey: testClientKey, deferred: deferred}
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
	pt.syncXds(context.Background(), wrapper("v1", false))
	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "v1", v)
}

// A never-published client is withheld within the budget, then published.
func TestSyncXds_NeverPublishedDeferredPublishesAtBudget(t *testing.T) {
	shortenBudget(t, 50*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapper("v1", true))

	_, ok := publishedVersion(t, pt)
	assert.False(t, ok, "deferred snapshot must not publish before the budget expires")

	require.Eventually(t, func() bool {
		_, ok := publishedVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond, "deferred snapshot must publish after the budget expires")
	v, _ := publishedVersion(t, pt)
	assert.Equal(t, "v1", v)
}

// The latest deferred snapshot is the one published at budget expiry.
func TestSyncXds_LatestDeferredWins(t *testing.T) {
	shortenBudget(t, 80*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapper("v1", true))
	pt.syncXds(context.Background(), wrapper("v2", true))

	require.Eventually(t, func() bool {
		_, ok := publishedVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond)
	v, _ := publishedVersion(t, pt)
	assert.Equal(t, "v2", v)
}

// A coherent snapshot supersedes a pending first-publish and the canceled
// timer must never overwrite it.
func TestSyncXds_CoherentSupersedesPending(t *testing.T) {
	shortenBudget(t, 100*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapper("deferred", true))
	pt.syncXds(context.Background(), wrapper("coherent", false))

	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent", v)

	time.Sleep(200 * time.Millisecond)
	v, _ = publishedVersion(t, pt)
	assert.Equal(t, "coherent", v, "a canceled first-publish timer must not overwrite a coherent snapshot")
}

// A warm client (already published) is never sent a deferred snapshot — and is
// not bounded by the budget. This is the make-before-break guarantee.
func TestSyncXds_WarmClientNeverReceivesDeferred(t *testing.T) {
	shortenBudget(t, 50*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapper("coherent", false))
	pt.syncXds(context.Background(), wrapper("deferred", true))

	time.Sleep(150 * time.Millisecond) // well past the budget
	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent", v, "warm clients keep their last coherent snapshot, with no time bound")
}

// A never-published deferred client increments defers_total{reason} and, at
// budget expiry, bounded_publishes_total{mode=first_publish}.
func TestMetrics_DeferAndBoundedPublish(t *testing.T) {
	ResetMetrics()
	shortenBudget(t, 30*time.Millisecond)
	pt := newTestTranslator()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pt.syncXds(ctx, deferredWrapper("v1", deferReasonMissingClusters))
	require.Eventually(t, func() bool { _, ok := publishedVersion(t, pt); return ok },
		2*time.Second, 5*time.Millisecond)

	g := metricstest.MustGatherMetricsContext(ctx, t,
		"kgateway_xds_snapshot_perclient_defers_total",
		"kgateway_xds_snapshot_perclient_bounded_publishes_total")
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_defers_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: []metrics.Label{
				{Name: "gateway", Value: "unknown"},
				{Name: "namespace", Value: "unknown"},
				{Name: "reason", Value: deferReasonMissingClusters},
			},
			Test: metricstest.GreaterOrEqual(1),
		},
	})
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_bounded_publishes_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: []metrics.Label{
				{Name: "gateway", Value: "unknown"},
				{Name: "namespace", Value: "unknown"},
				{Name: "mode", Value: boundedPublishFirstPublish},
			},
			Test: metricstest.Equal(1),
		},
	})
}

// A defer followed by a coherent publish increments recoveries_total.
func TestMetrics_Recovery(t *testing.T) {
	ResetMetrics()
	pt := newTestTranslator()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pt.syncXds(ctx, deferredWrapper("v1", deferReasonMissingEndpoints)) // never-published defer
	pt.syncXds(ctx, wrapper("v2", false))                               // coherent -> recovery

	g := metricstest.MustGatherMetricsContext(ctx, t, "kgateway_xds_snapshot_perclient_recoveries_total")
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_recoveries_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: []metrics.Label{
				{Name: "gateway", Value: "unknown"},
				{Name: "namespace", Value: "unknown"},
			},
			Test: metricstest.GreaterOrEqual(1),
		},
	})
}

// A departed client's pending first publish must not fire.
func TestSyncXds_DepartureCancelsPending(t *testing.T) {
	shortenBudget(t, 50*time.Millisecond)
	pt := newTestTranslator()

	pt.syncXds(context.Background(), wrapper("v1", true))
	pt.firstPublish.clientDeparted(testClientKey)

	time.Sleep(150 * time.Millisecond)
	_, ok := publishedVersion(t, pt)
	assert.False(t, ok, "a departed client's pending first publish must not fire")
}
