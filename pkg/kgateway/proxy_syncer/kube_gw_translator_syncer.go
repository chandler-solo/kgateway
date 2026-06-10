package proxy_syncer

import (
	"context"
	"maps"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

// publicationGate is the per-client publication policy and bookkeeping seam,
// implemented by perClientReconciler. Nil disables deferral and stuck
// tracking (used by tests); publication then goes straight to the cache.
type publicationGate interface {
	// shouldDeferIncomplete reports whether a publish attempt with incomplete
	// inputs should still be withheld (bounded per episode by a budget).
	shouldDeferIncomplete(clientKey string) bool
	// observeWithheld records a withheld publication; a non-nil pending wrapper
	// is retained for direct retry by the heartbeat loop.
	observeWithheld(clientKey string, pending *XdsSnapWrapper)
	// commitPublish atomically publishes and records the outcome; see
	// perClientReconciler.commitPublish.
	commitPublish(ctx context.Context, snapWrap XdsSnapWrapper, degraded bool, expectSeq *uint64) (published, recovered bool)
}

// syncXds publishes the per-client snapshot to the xDS cache, resolving any
// build-time holes first. It reports whether a snapshot was actually published;
// false means the update was withheld (invalid snapshot, or an episode-bounded
// deferral of unresolvable references) — the client stays marked stuck so the
// heartbeat keeps retrying.
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) bool {
	return s.syncXdsAttempt(ctx, snapWrap, nil)
}

// syncXdsAttempt is syncXds plus the retry entry point: expectSeq non-nil
// marks a re-attempt of a previously withheld snapshot, which only commits if
// no newer event superseded it (see perClientReconciler.commitPublish).
// Resolution re-runs on retry against the then-current previous snapshot.
func (s *ProxyTranslator) syncXdsAttempt(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
	expectSeq *uint64,
) bool {
	snap := snapWrap.snap
	proxyKey := snapWrap.proxyKey

	// TODO: handle errored clusters by fetching them from the previous snapshot and using the old cluster

	// stringifying the snapshot may be an expensive operation, so we'd like to avoid building the large
	// string if we're not even going to log it anyway
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey)

	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	// Resolution runs when the snapshot was built with holes (referenced
	// clusters absent, or present EDS clusters lacking CLAs — R1-R3 in
	// devel/architecture/perclient-xds-publication.md) and, whenever a
	// previously published snapshot exists, to apply route-transition
	// readiness (S4): changed/new routes and TCP chains activate only when
	// their new target is usable. Cold start (no previous snapshot) publishes
	// as built — there is no baseline to define "changed", and Envoy's own
	// cluster warming covers that window.
	var prev envoycache.ResourceSnapshot
	if p, err := s.xdsCache.GetSnapshot(proxyKey); err == nil {
		prev = p
	}
	var stats resolutionStats
	if snapWrap.needsResolution() || prev != nil {
		snap, stats = resolvePublication(snapWrap, prev)
	}

	// Hard validation of the FINAL artifact (post carry-forward, synthesis,
	// and pruning): malformed/nil/mistyped resources, generated proto
	// validation, duplicate listener filter chain matches, snapshot
	// consistency, and SDS references whose secret is absent always withhold —
	// these indicate bugs, and Envoy keeps the last good snapshot. Retrying
	// the same data cannot heal these, so no pending retry is retained; the
	// heal is a new build.
	if err := ValidateXDSSnapshot(snap); err != nil {
		logger.Error("invalid xds snapshot; preserving last published snapshot",
			"proxy_key", proxyKey, "error", err)
		recordSnapshotDefer(proxyKey, deferReasonInvalidSnapshot)
		if s.gate != nil && expectSeq == nil {
			s.gate.observeWithheld(proxyKey, nil)
		}
		return false
	}

	// S1 enforcement on the final snapshot, covering every path uniformly:
	// references the pruner cannot rewrite (shapes outside RouteAction/TcpProxy
	// surgery) and held entries whose PREVIOUS target has since left the
	// cluster set. The per-gateway precomputed reference set describes the
	// BUILT routes/listeners; when pruning held or omitted entries the final
	// reference set may differ, so only then is the final artifact re-walked.
	referenced := snapWrap.referencedClusters
	if stats.held+stats.omitted > 0 {
		referenced = collectReferencedClusters(
			snap.Resources[envoycachetypes.Route],
			snap.Resources[envoycachetypes.Listener],
		)
	}
	missing := findMissingReferencedClusters(
		referenced,
		snap.Resources[envoycachetypes.Cluster].Items,
		snapWrap.erroredClusters,
	)
	if len(missing) > 0 && s.gate != nil && s.gate.shouldDeferIncomplete(proxyKey) {
		// Withhold rather than hand Envoy a dangling reference, bounded per
		// episode by a budget: the original wrapper is retained so the
		// heartbeat loop can re-attempt it directly (a withhold produces no
		// KRT event, and an unchanged recompute is hash-suppressed, so budget
		// expiry cannot depend on the event stream). Past the budget the
		// snapshot publishes marked degraded — the referencing routes return
		// no-cluster errors — instead of being withheld forever.
		logger.Warn("withholding per-client snapshot: cluster references remain unresolved after pruning",
			"proxy_key", proxyKey,
			"missing_clusters", missing,
		)
		recordSnapshotDefer(proxyKey, deferReasonUnresolvedReferences)
		if expectSeq == nil {
			s.gate.observeWithheld(proxyKey, &snapWrap)
		}
		return false
	}

	// Degraded means published with known-incomplete or stale-substituted data:
	// unresolvable references published past the budget, carried-forward
	// clusters/CLAs, synthesized empty CLAs, or entries pruned because their
	// target was unsatisfied. Degraded clients count as stuck, so the heartbeat
	// keeps re-running the per-client collections until a clean publish heals
	// them. S4 holds/omits for not-yet-usable targets are NOT degraded: they
	// are expected warming, healed by the endpoint events themselves.
	degraded := len(missing) > 0 || stats.carried > 0 || stats.synthesized > 0 || stats.unsatisfiedTargets > 0

	var published, recovered bool
	if s.gate == nil {
		if err := s.xdsCache.SetSnapshot(ctx, proxyKey, snap); err != nil {
			logger.Error("failed to set xds snapshot", "proxy_key", proxyKey, "error", err)
			return false
		}
		published = true
	} else {
		published, recovered = s.gate.commitPublish(ctx, snapWrap.WithSnapshot(snap), degraded, expectSeq)
	}
	if !published {
		return false
	}

	if stats.carried+stats.held+stats.omitted+stats.synthesized > 0 {
		logger.Info("resolved per-client snapshot at publish time",
			"proxy_key", proxyKey,
			"carried_clusters", stats.carried,
			"held_routes", stats.held,
			"omitted_routes", stats.omitted,
			"synthesized_load_assignments", stats.synthesized,
		)
		recordSnapshotResolution(proxyKey, stats.carried, stats.held, stats.omitted, stats.synthesized)
	}
	if len(missing) > 0 {
		logger.Warn("published snapshot with unresolved cluster references",
			"proxy_key", proxyKey, "missing_clusters", missing)
		recordDegradedPublish(proxyKey, degradedReasonUnresolvedReferences)
	}
	if recovered {
		// A clean publish after a prior defer or degraded publish is a
		// recovery; with the heartbeat as backstop, recoveries of
		// long-deferred clients are heartbeat-driven heals (#14184).
		recordSnapshotRecovery(proxyKey)
	}
	return true
}

type resolutionStats struct {
	carried     int
	held        int
	omitted     int
	synthesized int
	// unsatisfiedTargets counts referenced-but-absent clusters with nothing to
	// carry forward (R3): their referencing entries were held or omitted
	// because of a HOLE, as opposed to S4 usability holds. Used to mark the
	// publish degraded so the heartbeat heals the hole.
	unsatisfiedTargets int
}

// resolvePublication produces a publishable snapshot:
//
//   - R2: a referenced cluster absent from current inputs but present in the
//     previously published snapshot is carried forward together with its CLA.
//   - R3: a referenced cluster with nothing to carry has its referencing route
//     entries / TCP filter chains held at their previous version or omitted.
//   - R1 missing-CLA edge: a present EDS cluster lacking a CLA gets its previous
//     CLA, or a synthesized empty one (so Envoy is not stalled on
//     initial_fetch_timeout and the EDS subset invariant holds).
//   - S4: when a previous snapshot exists and the route/listener resources
//     changed, route entries / TCP chains that are new or retargeted activate
//     only once their target is usable; until then they are held at their
//     previous version or omitted. Entries with unchanged targets always
//     publish (endpoint truth, S3).
//
// Resolution is best-effort and infallible; whether the result may actually be
// published (hard validation, reference closure) is decided in syncXds.
func resolvePublication(snapWrap XdsSnapWrapper, prev envoycache.ResourceSnapshot) (*envoycache.Snapshot, resolutionStats) {
	var stats resolutionStats

	var prevCDS, prevEDS, prevRDS, prevLDS map[string]envoycachetypes.ResourceWithTTL
	if prev != nil {
		prevCDS = prev.GetResourcesAndTTL(envoyresource.ClusterType)
		prevEDS = prev.GetResourcesAndTTL(envoyresource.EndpointType)
		prevRDS = prev.GetResourcesAndTTL(envoyresource.RouteType)
		prevLDS = prev.GetResourcesAndTTL(envoyresource.ListenerType)
	}

	clusterResources := snapWrap.snap.Resources[envoycachetypes.Cluster]
	endpointResources := snapWrap.snap.Resources[envoycachetypes.Endpoint]
	clusterItems := clusterResources.Items
	endpointItems := endpointResources.Items

	unsatisfied := map[string]struct{}{}
	if snapWrap.needsResolution() {
		clusterItems = maps.Clone(clusterItems)
		if clusterItems == nil {
			clusterItems = map[string]envoycachetypes.ResourceWithTTL{}
		}
		endpointItems = maps.Clone(endpointItems)
		if endpointItems == nil {
			endpointItems = map[string]envoycachetypes.ResourceWithTTL{}
		}
		clustersMutated, endpointsMutated := false, false

		// R2 / R3 classification of referenced-but-absent clusters.
		for _, name := range snapWrap.missingClusters {
			prevItem, ok := prevCDS[name]
			if !ok {
				unsatisfied[name] = struct{}{}
				continue
			}
			clusterItems[name] = prevItem
			clustersMutated = true
			stats.carried++
			if claName, isEDS := endpointResourceNameForCluster(prevItem); isEDS {
				if _, ok := endpointItems[claName]; ok {
					continue
				}
				if prevCla, ok := prevEDS[claName]; ok {
					endpointItems[claName] = prevCla
				} else {
					endpointItems[claName] = synthesizeEmptyCla(claName)
					stats.synthesized++
				}
				endpointsMutated = true
			}
		}

		// R1 missing-CLA edge for clusters that are present in current inputs.
		for _, claName := range snapWrap.missingEndpointClusters {
			if _, ok := endpointItems[claName]; ok {
				continue
			}
			if prevCla, ok := prevEDS[claName]; ok {
				endpointItems[claName] = prevCla
			} else {
				endpointItems[claName] = synthesizeEmptyCla(claName)
				stats.synthesized++
			}
			endpointsMutated = true
		}

		stats.unsatisfiedTargets = len(unsatisfied)

		// Versions must change when contents change; recompute from final
		// contents, but ONLY for resource types actually mutated here — the
		// build-time versions carry signals (the policy-attachment EDS version
		// bump) that a pure content hash would erase.
		if clustersMutated {
			clusterResources = resourcesWithRecomputedVersion(itemsToSlice(clusterItems))
		}
		if endpointsMutated {
			// Carried/synthesized CLAs are added only for clusters present in the
			// final CDS set and the build-time filter already enforced the subset
			// for current inputs, so S2 holds by construction here.
			endpointResources = resourcesWithRecomputedVersion(itemsToSlice(endpointItems))
		}
	}

	routes := snapWrap.snap.Resources[envoycachetypes.Route]
	listeners := snapWrap.snap.Resources[envoycachetypes.Listener]

	sets := pruneSets{unsatisfied: unsatisfied}
	if prev != nil {
		// S4 transition checking is gated on the route/listener resources having
		// changed relative to what was last published: an endpoints-only update
		// cannot introduce a route transition, so the (per-entry) diff walk is
		// skipped on that hot path.
		routesChanged := prev.GetVersion(envoyresource.RouteType) != routes.Version
		listenersChanged := prev.GetVersion(envoyresource.ListenerType) != listeners.Version
		if routesChanged || listenersChanged {
			sets.checkTransitions = true
			sets.notUsable = clustersWithoutUsableEndpoints(clusterItems, endpointItems)
		}
	}

	if len(sets.unsatisfied) > 0 || (sets.checkTransitions && len(sets.notUsable) > 0) {
		var heldR, omittedR, heldL, omittedL int
		routes, heldR, omittedR = pruneRouteConfigurations(routes, prevRDS, sets)
		listeners, heldL, omittedL = pruneListeners(listeners, prevLDS, sets)
		stats.held = heldR + heldL
		stats.omitted = omittedR + omittedL
	}

	snapshot := &envoycache.Snapshot{}
	snapshot.Resources[envoycachetypes.Cluster] = clusterResources
	snapshot.Resources[envoycachetypes.Endpoint] = endpointResources
	snapshot.Resources[envoycachetypes.Route] = routes
	snapshot.Resources[envoycachetypes.Listener] = listeners
	snapshot.Resources[envoycachetypes.Secret] = snapWrap.snap.Resources[envoycachetypes.Secret]
	return snapshot, stats
}

func synthesizeEmptyCla(claName string) envoycachetypes.ResourceWithTTL {
	return envoycachetypes.ResourceWithTTL{
		Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: claName},
	}
}

func itemsToSlice(items map[string]envoycachetypes.ResourceWithTTL) []envoycachetypes.ResourceWithTTL {
	out := make([]envoycachetypes.ResourceWithTTL, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}
