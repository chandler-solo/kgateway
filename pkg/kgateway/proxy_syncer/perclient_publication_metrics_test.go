package proxy_syncer

import (
	"context"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/stretchr/testify/require"

	"github.com/kgateway-dev/kgateway/v2/pkg/metrics"
	"github.com/kgateway-dev/kgateway/v2/pkg/metrics/metricstest"
)

// The proxyKey has no gateway/namespace segments, so labels fall back to
// "unknown" — the metric plumbing, not name parsing, is under test.
var unknownLabels = []metrics.Label{
	{Name: "gateway", Value: "unknown"},
	{Name: "namespace", Value: "unknown"},
}

func labelsWith(name, value string) []metrics.Label {
	return append(append([]metrics.Label{}, unknownLabels...), metrics.Label{Name: name, Value: value})
}

// A deferred build followed by a bounded first publish increments
// defers_total{missing_clusters} and bounded_publishes_total{first_publish}.
func TestPublicationMetrics_DeferAndBoundedPublish(t *testing.T) {
	ResetMetrics()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pt := newFirstPublishTestTranslator(false, 30*time.Millisecond)
	pt.syncXds(ctx, deferredWrapperV("v1"))
	require.Eventually(t, func() bool {
		_, ok := publishedListenerVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond)

	g := metricstest.MustGatherMetricsContext(ctx, t,
		"kgateway_xds_snapshot_perclient_defers_total",
		"kgateway_xds_snapshot_perclient_bounded_publishes_total")
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_defers_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: labelsWith("reason", deferReasonMissingClusters),
			Test:   metricstest.GreaterOrEqual(1),
		},
	})
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_bounded_publishes_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: labelsWith("mode", boundedPublishFirstPublish),
			Test:   metricstest.Equal(1),
		},
	})
}

// A warm client published at budget expiry because its only gaps were
// endpoint-less clusters increments bounded_publishes_total{warm_truth}.
func TestPublicationMetrics_WarmTruthBoundedPublish(t *testing.T) {
	ResetMetrics()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pt := newFirstPublishTestTranslator(true, 30*time.Millisecond)
	pt.syncXds(ctx, deferredUnusableWrapperV("v1"))
	require.Eventually(t, func() bool {
		_, ok := publishedListenerVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond)

	g := metricstest.MustGatherMetricsContext(ctx, t,
		"kgateway_xds_snapshot_perclient_bounded_publishes_total")
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_bounded_publishes_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: labelsWith("mode", boundedPublishWarmTruth),
			Test:   metricstest.Equal(1),
		},
	})
}

// A warm (prior-xDS-version) client withheld at budget expiry (missing
// clusters) increments deferred_withheld_total{prior_xds_version}.
func TestPublicationMetrics_DeferredWithheld(t *testing.T) {
	ResetMetrics()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pt := newFirstPublishTestTranslator(true, 30*time.Millisecond)
	pt.syncXds(ctx, deferredWrapperV("v1"))
	time.Sleep(150 * time.Millisecond) // past the budget; withheld, not published

	g := metricstest.MustGatherMetricsContext(ctx, t,
		"kgateway_xds_snapshot_perclient_deferred_withheld_total")
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_deferred_withheld_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: labelsWith("reason", withheldReasonPriorXDSVersion),
			Test:   metricstest.GreaterOrEqual(1),
		},
	})
}

// A warm client whose deferred build carries a missing cluster forward
// increments carried_clusters_total.
func TestPublicationMetrics_CarriedClusters(t *testing.T) {
	ResetMetrics()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	pt := newFirstPublishTestTranslator(false, 0)

	// Publish a coherent snapshot containing cluster-missing, then a deferred
	// build that lost it: resolution carries it forward.
	coherent := coherentWrapperV("v1")
	coherent.snap.Resources[envoycachetypes.Cluster] = envoycache.NewResourcesWithTTL("c1", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyclusterv3.Cluster{Name: "cluster-missing"}},
	})
	pt.syncXds(ctx, coherent)
	pt.syncXds(ctx, deferredWrapperV("v2"))

	g := metricstest.MustGatherMetricsContext(ctx, t,
		"kgateway_xds_snapshot_perclient_carried_clusters_total")
	g.AssertMetricsInclude("kgateway_xds_snapshot_perclient_carried_clusters_total", []metricstest.ExpectMetric{
		&metricstest.ExpectedMetricValueTest{
			Labels: unknownLabels,
			Test:   metricstest.GreaterOrEqual(1),
		},
	})
}
