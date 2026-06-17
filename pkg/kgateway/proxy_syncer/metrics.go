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
	modeLabel         = "mode"
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
			Help: "Total per-client XDS snapshot publication deferrals, by reason. " +
				"Every increment means an update was withheld: the client kept its " +
				"last published snapshot, or — before first publish — kept waiting " +
				"up to the first-publish budget. A sustained rate for a gateway " +
				"means a connected client's per-client inputs are not becoming " +
				"consistent (#14184).",
		},
		[]string{gatewayLabel, namespaceLabel, reasonLabel},
	)
	snapshotPerClientRecoveriesTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_recoveries_total",
			Help: "Total times a per-client XDS snapshot resumed coherent publishing " +
				"after one or more deferrals.",
		},
		[]string{gatewayLabel, namespaceLabel},
	)
	snapshotPerClientBoundedPublishesTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_bounded_publishes_total",
			Help: "Total deferred snapshots published because the first-publish " +
				"budget expired (mode=first_publish), i.e. a never-published client " +
				"started on incomplete config instead of waiting indefinitely. " +
				"Nonzero means a gateway pod would otherwise have crash-looped " +
				"(#14184). The mode label is forward-compatible with carry-forward " +
				"publishes, which this minimal bound does not perform.",
		},
		[]string{gatewayLabel, namespaceLabel, modeLabel},
	)
)

// boundedPublishFirstPublish is the only bounded-publish mode this minimal
// bound emits (carry_forward publishes are a main-only follow-up).
const boundedPublishFirstPublish = "first_publish"

// recordSnapshotDefer counts one withheld publication for the client's gateway,
// per reason.
func recordSnapshotDefer(proxyKey string, reasons []string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	for _, reason := range reasons {
		snapshotPerClientDefersTotal.Inc(
			metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
			metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
			metrics.Label{Name: reasonLabel, Value: reason},
		)
	}
}

// recordSnapshotRecovery counts a client resuming coherent publication after a
// prior deferral.
func recordSnapshotRecovery(proxyKey string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	snapshotPerClientRecoveriesTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
	)
}

// recordBoundedPublish counts a deferred snapshot published because the
// first-publish budget expired.
func recordBoundedPublish(proxyKey string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	snapshotPerClientBoundedPublishesTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
		metrics.Label{Name: modeLabel, Value: boundedPublishFirstPublish},
	)
}

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
	snapshotPerClientRecoveriesTotal.Reset()
	snapshotPerClientBoundedPublishesTotal.Reset()
}
