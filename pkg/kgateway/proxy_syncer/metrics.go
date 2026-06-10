package proxy_syncer

import (
	"strings"
	"time"

	"github.com/kgateway-dev/kgateway/v2/pkg/metrics"
)

const (
	statusSubsystem   = "status_syncer"
	snapshotSubsystem = "xds_snapshot"
	syncerNameLabel   = "syncer"
	gatewayLabel      = "gateway"
	nameLabel         = "name"
	namespaceLabel    = "namespace"
	resultLabel       = "result"
	resourceLabel     = "resource"
	reasonLabel       = "reason"
)

// Defer reasons: every one of these means a client's snapshot update was
// WITHHELD (Envoy keeps its last published snapshot, or has none yet).
const (
	// deferReasonIncompleteInputs: the snapshot is missing referenced clusters
	// or had CLAs synthesized empty; deferral is bounded per episode by the
	// incomplete-inputs budget, after which it publishes marked degraded.
	deferReasonIncompleteInputs = "incomplete_inputs"
	// deferReasonInvalidSnapshot: the built snapshot failed hard validation.
	deferReasonInvalidSnapshot = "invalid_snapshot"
	// deferReasonClustersNotReady / deferReasonEndpointsNotReady: the snapshot
	// transform deferred because a per-client input collection has not derived
	// this client's row yet.
	deferReasonClustersNotReady  = "clusters_not_ready"
	deferReasonEndpointsNotReady = "endpoints_not_ready"
)

// Degraded-publish reasons: the snapshot WAS published, but with known-
// incomplete data; the client stays marked stuck so the heartbeat keeps
// re-running the per-client collections until a clean publish.
const (
	// degradedReasonMissingClusters: routes reference clusters absent from the
	// published CDS (no-cluster errors on those routes until healed).
	degradedReasonMissingClusters = "missing_clusters"
	// degradedReasonSynthesizedClas: required CLAs were synthesized empty (the
	// affected backends serve no endpoints until the real CLAs arrive).
	degradedReasonSynthesizedClas = "synthesized_load_assignments"
)

var (
	statusSyncHistogramBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
	statusSyncsTotal           = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: statusSubsystem,
			Name:      "status_syncs_total",
			Help:      "Total number of status syncs",
		},
		[]string{nameLabel, namespaceLabel, syncerNameLabel, resultLabel},
	)
	statusSyncDuration = metrics.NewHistogram(
		metrics.HistogramOpts{
			Subsystem:                       statusSubsystem,
			Name:                            "status_sync_duration_seconds",
			Help:                            "Status sync duration",
			Buckets:                         statusSyncHistogramBuckets,
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		},
		[]string{nameLabel, namespaceLabel, syncerNameLabel},
	)

	transformsHistogramBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}
	snapshotTransformsTotal    = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "transforms_total",
			Help:      "Total number of XDS snapshot transforms",
		},
		[]string{gatewayLabel, namespaceLabel, resultLabel},
	)
	snapshotTransformDuration = metrics.NewHistogram(
		metrics.HistogramOpts{
			Subsystem:                       snapshotSubsystem,
			Name:                            "transform_duration_seconds",
			Help:                            "XDS snapshot transform duration",
			Buckets:                         transformsHistogramBuckets,
			NativeHistogramBucketFactor:     1.1,
			NativeHistogramMaxBucketNumber:  100,
			NativeHistogramMinResetDuration: time.Hour,
		},
		[]string{gatewayLabel, namespaceLabel},
	)
	snapshotResources = metrics.NewGauge(
		metrics.GaugeOpts{
			Subsystem: snapshotSubsystem,
			Name:      "resources",
			Help:      "Current number of resources in XDS snapshot",
		},
		[]string{gatewayLabel, namespaceLabel, resourceLabel},
	)
	snapshotPerClientDefersTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_defers_total",
			Help: "Total per-client XDS snapshot deferrals, by reason. Every increment " +
				"means an update was withheld and the client kept its last published " +
				"snapshot (or none, before first publish). A sustained rate for a " +
				"gateway means a connected client's snapshot is not becoming " +
				"publishable (#14184).",
		},
		[]string{gatewayLabel, namespaceLabel, reasonLabel},
	)
	snapshotPerClientDegradedPublishesTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_degraded_publishes_total",
			Help: "Total per-client XDS snapshots published with known-incomplete data " +
				"(routes referencing missing clusters, or synthesized empty load " +
				"assignments), by reason. Traffic IS being served, but degraded; the " +
				"heartbeat keeps recomputing until a clean publish (#14184).",
		},
		[]string{gatewayLabel, namespaceLabel, reasonLabel},
	)
	snapshotPerClientRecoveriesTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_recoveries_total",
			Help: "Total times a per-client XDS snapshot resumed CLEAN publishing after " +
				"a prior deferral or degraded publish. With the per-client heartbeat as " +
				"backstop, recoveries of long-deferred clients are heartbeat-driven " +
				"heals (#14184).",
		},
		[]string{gatewayLabel, namespaceLabel},
	)
	snapshotPerClientReclaimedTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_reclaimed_total",
			Help: "Total per-client XDS snapshot cache entries reclaimed after the " +
				"client left the connected set (#14184).",
		},
		[]string{gatewayLabel, namespaceLabel},
	)
)

// snapshotResourcesMetricLabels defines the labels for XDS snapshot resources metrics.
type snapshotResourcesMetricLabels struct {
	Gateway   string
	Namespace string
	Resource  string
}

func (r snapshotResourcesMetricLabels) toMetricsLabels() []metrics.Label {
	return []metrics.Label{
		{Name: gatewayLabel, Value: r.Gateway},
		{Name: namespaceLabel, Value: r.Namespace},
		{Name: resourceLabel, Value: r.Resource},
	}
}

// StatusSyncMetricLabels defines the labels for status sync metrics.
type StatusSyncMetricLabels struct {
	Name      string
	Namespace string
	Syncer    string
}

func (s StatusSyncMetricLabels) toMetricsLabels() []metrics.Label {
	return []metrics.Label{
		{Name: nameLabel, Value: s.Name},
		{Name: namespaceLabel, Value: s.Namespace},
		{Name: syncerNameLabel, Value: s.Syncer},
	}
}

// CollectStatusSyncMetrics is called at the start of a status sync function to
// begin metrics collection and returns a function called at the end to complete
// metrics recording.
func CollectStatusSyncMetrics(labels StatusSyncMetricLabels) func(error) {
	if !metrics.Active() {
		return func(err error) {}
	}

	start := time.Now()

	return func(err error) {
		duration := time.Since(start)

		statusSyncDuration.Observe(duration.Seconds(), labels.toMetricsLabels()...)

		result := "success"
		if err != nil {
			result = "error"
		}

		statusSyncsTotal.Inc(append(labels.toMetricsLabels(),
			metrics.Label{Name: resultLabel, Value: result},
		)...)
	}
}

// collectXDSTransformMetrics is called at the start of a transform function to
// begin metrics collection and returns a function called at the end to complete
// metrics recording.
func collectXDSTransformMetrics(clientKey string) func(error) {
	if !metrics.Active() {
		return func(err error) {}
	}

	start := time.Now()

	cd := getDetailsFromXDSClientResourceName(clientKey)
	return func(err error) {
		result := "success"
		if err != nil {
			result = "error"
		}

		snapshotTransformsTotal.Inc(
			metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
			metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
			metrics.Label{Name: resultLabel, Value: result},
		)

		duration := time.Since(start)

		snapshotTransformDuration.Observe(duration.Seconds(),
			metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
			metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
		)
	}
}

// recordSnapshotDefer increments the per-client defer counter for the gateway the
// given client belongs to. reason is one of the deferReason* constants.
func recordSnapshotDefer(clientKey, reason string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(clientKey)
	snapshotPerClientDefersTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
		metrics.Label{Name: reasonLabel, Value: reason},
	)
}

// recordDegradedPublish counts a snapshot published with known-incomplete data.
// reason is one of the degradedReason* constants.
func recordDegradedPublish(clientKey, reason string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(clientKey)
	snapshotPerClientDegradedPublishesTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
		metrics.Label{Name: reasonLabel, Value: reason},
	)
}

// recordSnapshotRecovery counts a client resuming publication after a prior defer.
func recordSnapshotRecovery(clientKey string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(clientKey)
	snapshotPerClientRecoveriesTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
	)
}

// recordSnapshotReclaimed counts a departed client's xDS cache entry being cleared.
func recordSnapshotReclaimed(clientKey string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(clientKey)
	snapshotPerClientReclaimedTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
	)
}

type resourceNameDetails struct {
	Role      string
	Namespace string
	Gateway   string
}

// getDetailsFromXDSClientResourceName extracts details from an XDS client resource name.
func getDetailsFromXDSClientResourceName(resourceName string) resourceNameDetails {
	res := resourceNameDetails{
		Role:      "unknown",
		Namespace: "unknown",
		Gateway:   "unknown",
	}

	pks := strings.SplitN(resourceName, "~", 5)

	if len(pks) > 0 {
		res.Role = pks[0]
	}

	if len(pks) > 1 {
		res.Namespace = pks[1]
	}

	if len(pks) > 2 {
		res.Gateway = pks[2]
	}

	return res
}

// ResetMetrics resets the metrics from this package.
// This is provided for testing purposes only.
func ResetMetrics() {
	statusSyncDuration.Reset()
	statusSyncsTotal.Reset()
	snapshotTransformsTotal.Reset()
	snapshotTransformDuration.Reset()
	snapshotResources.Reset()
	snapshotPerClientDefersTotal.Reset()
	snapshotPerClientDegradedPublishesTotal.Reset()
	snapshotPerClientRecoveriesTotal.Reset()
	snapshotPerClientReclaimedTotal.Reset()
}
