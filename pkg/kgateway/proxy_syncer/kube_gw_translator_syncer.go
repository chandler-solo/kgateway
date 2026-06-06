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

// syncXds publishes the per-client snapshot to the xDS cache, resolving any
// build-time holes first. It reports whether a snapshot was actually published;
// false means the safety net withheld it (the caller should treat the client as
// still deferred so the heartbeat keeps retrying).
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
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
	if snapWrap.needsResolution() || prev != nil {
		resolved, stats, ok := resolvePublication(snapWrap, prev)
		if !ok {
			// Safety net: a reference survived pruning (for example a cluster
			// reference embedded somewhere the pruner does not model). Publishing
			// would violate S1, so withhold this update; Envoy keeps the previous
			// snapshot and the heartbeat retries.
			logger.Warn("withholding per-client snapshot: cluster references remain unresolved after pruning",
				"proxy_key", proxyKey,
				"missing_clusters", snapWrap.missingClusters,
			)
			recordSnapshotDefer(proxyKey, "unresolvable_references")
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
		snap = resolved
	}

	s.xdsCache.SetSnapshot(ctx, proxyKey, snap)
	return true
}

type resolutionStats struct {
	carried     int
	held        int
	omitted     int
	synthesized int
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
// Returns ok=false if pruning could not restore reference closure (S1), in
// which case nothing may be published.
func resolvePublication(snapWrap XdsSnapWrapper, prev envoycache.ResourceSnapshot) (*envoycache.Snapshot, resolutionStats, bool) {
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

		if len(sets.unsatisfied) > 0 {
			// S1 safety net: pruning models RouteAction and TcpProxy references
			// (the same scope collectReferencedClusters gates on). If a reference
			// survived anyway, refuse to publish rather than hand Envoy a dangling
			// reference.
			stillReferenced := collectReferencedClusters(routes, listeners)
			if missing := findMissingReferencedClusters(stillReferenced, clusterItems, snapWrap.erroredClusters); len(missing) > 0 {
				return nil, stats, false
			}
		}
	}

	snapshot := &envoycache.Snapshot{}
	snapshot.Resources[envoycachetypes.Cluster] = clusterResources
	snapshot.Resources[envoycachetypes.Endpoint] = endpointResources
	snapshot.Resources[envoycachetypes.Route] = routes
	snapshot.Resources[envoycachetypes.Listener] = listeners
	snapshot.Resources[envoycachetypes.Secret] = snapWrap.snap.Resources[envoycachetypes.Secret]
	return snapshot, stats, true
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
