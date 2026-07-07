package proxy_syncer

import (
	"github.com/kgateway-dev/kgateway/v2/pkg/metrics"
)

// Per-client publication counters. These make the publish-time resolution
// legible in the field: the #14184 triage had to infer all of this from debug
// log volume.
const (
	publicationReasonLabel = "reason"
	publicationModeLabel   = "mode"

	deferReasonMissingClusters   = "missing_clusters"
	deferReasonUnusableEndpoints = "unusable_endpoints"

	boundedPublishFirstPublish = "first_publish"
	// boundedPublishWarmTruth marks a bounded publish to a warm
	// (prior-xDS-version) client whose only remaining gaps were endpoint-less
	// referenced clusters — their steady-state truth, not propagation lag
	// (#14352). Warm clients with clusters missing from CDS stay withheld.
	boundedPublishWarmTruth = "warm_truth"

	withheldReasonPriorXDSVersion = "prior_xds_version"
)

var (
	snapshotPerClientDefersTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_defers_total",
			Help: "Total per-client XDS snapshots built with unready referenced " +
				"clusters, by reason. Each increment means publish-time resolution " +
				"ran for the client (carry-forward, scale-to-zero truth, or a held " +
				"route flip). A sustained rate for a gateway means some referenced " +
				"cluster is not becoming ready (#14184).",
		},
		[]string{gatewayLabel, namespaceLabel, publicationReasonLabel},
	)
	snapshotPerClientCarriedClustersTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_carried_clusters_total",
			Help: "Total clusters carried forward from the previously-published " +
				"snapshot because the current build was missing them. Carried " +
				"clusters serve their last-good config until the build catches up.",
		},
		[]string{gatewayLabel, namespaceLabel},
	)
	snapshotPerClientFlipsHeldTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_flips_held_total",
			Help: "Total publications in which routes/listeners/secrets were held " +
				"at their published versions because a newly-referenced cluster was " +
				"not yet ready. A sustained rate means a route flip is pinned " +
				"behind a cluster that never becomes usable.",
		},
		[]string{gatewayLabel, namespaceLabel},
	)
	snapshotPerClientBoundedPublishesTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_bounded_publishes_total",
			Help: "Total deferred snapshots published because the first-publish " +
				"budget (KGW_PER_CLIENT_PUBLISH_BUDGET) expired. " +
				"mode=first_publish: a never-published client started on " +
				"incomplete-but-consistent config instead of waiting indefinitely " +
				"(it would otherwise have crash-looped, #14184). mode=warm_truth: " +
				"a warm client's only gaps were endpoint-less referenced clusters " +
				"— steady state, not lag — and withholding would have frozen its " +
				"config indefinitely (#14352).",
		},
		[]string{gatewayLabel, namespaceLabel, publicationModeLabel},
	)
	snapshotPerClientDeferredWithheldTotal = metrics.NewCounter(
		metrics.CounterOpts{
			Subsystem: snapshotSubsystem,
			Name:      "perclient_deferred_withheld_total",
			Help: "Total deferred snapshots withheld at first-publish budget expiry " +
				"because the client may already be serving traffic (it reported a " +
				"prior accepted xDS version on connect) and some referenced cluster " +
				"was missing from CDS. Kgateway preserved last-good config after a " +
				"reconnect or controller restart instead of publishing routes that " +
				"would NC (#13868). Warm clients whose only gaps are endpoint-less " +
				"clusters are published instead (bounded_publishes_total, " +
				"mode=warm_truth).",
		},
		[]string{gatewayLabel, namespaceLabel, publicationReasonLabel},
	)
)

// recordSnapshotDefer counts one deferred build for the client's gateway, per
// recorded gap kind.
func recordSnapshotDefer(proxyKey string, missing, unusable []string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	if len(missing) > 0 {
		snapshotPerClientDefersTotal.Inc(
			metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
			metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
			metrics.Label{Name: publicationReasonLabel, Value: deferReasonMissingClusters},
		)
	}
	if len(unusable) > 0 {
		snapshotPerClientDefersTotal.Inc(
			metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
			metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
			metrics.Label{Name: publicationReasonLabel, Value: deferReasonUnusableEndpoints},
		)
	}
}

// recordCarriedClusters counts clusters carried forward in one resolution.
func recordCarriedClusters(proxyKey string, carried int) {
	if carried == 0 || !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	snapshotPerClientCarriedClustersTotal.Add(float64(carried),
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
	)
}

// recordFlipHeld counts a resolution that held the route flip.
func recordFlipHeld(proxyKey string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	snapshotPerClientFlipsHeldTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
	)
}

// recordBoundedPublish counts a deferred snapshot published at budget expiry,
// by mode (first_publish for cold clients, warm_truth for warm clients whose
// only gaps were endpoint-less clusters).
func recordBoundedPublish(proxyKey string, mode string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	snapshotPerClientBoundedPublishesTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
		metrics.Label{Name: publicationModeLabel, Value: mode},
	)
}

// recordDeferredWithheld counts a deferred snapshot withheld at budget expiry
// for a warm client.
func recordDeferredWithheld(proxyKey string) {
	if !metrics.Active() {
		return
	}
	cd := getDetailsFromXDSClientResourceName(proxyKey)
	snapshotPerClientDeferredWithheldTotal.Inc(
		metrics.Label{Name: gatewayLabel, Value: cd.Gateway},
		metrics.Label{Name: namespaceLabel, Value: cd.Namespace},
		metrics.Label{Name: publicationReasonLabel, Value: withheldReasonPriorXDSVersion},
	)
}
