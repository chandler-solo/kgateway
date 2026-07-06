package proxy_syncer

import (
	"context"
	"fmt"
	"maps"
	"sort"

	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) {
	snap := snapWrap.snap
	proxyKey := snapWrap.proxyKey

	// stringifying the snapshot may be an expensive operation, so we'd like to avoid building the large
	// string if we're not even going to log it anyway
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey)

	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	if snapWrap.deferred {
		// Per-cluster readiness resolution. The snapshot was built while some
		// referenced cluster was not ready; decide per cluster against the
		// currently-published snapshot instead of withholding everything:
		//
		//   - previously-published clusters that vanished from the build are
		//     carried forward with their CLAs (make-before-break);
		//   - previously-referenced clusters whose endpoints scaled to zero
		//     publish their truth — the empty CLA — so Envoy stops routing
		//     to endpoints that no longer exist;
		//   - only a route flip onto a NEWLY-referenced not-yet-ready cluster
		//     is held: routes/listeners/secrets stay at the published
		//     versions while the new clusters warm in the background, and the
		//     flip goes out in a later snapshot once they are usable. This
		//     also gives the flip a reference-ahead shape: the CDS carrying a
		//     new cluster is published in an earlier snapshot than the RDS
		//     that uses it.
		published, err := s.xdsCache.GetSnapshot(proxyKey)
		if err != nil {
			// Never-published client: there is no last-good to hold or carry.
			// Publishing an incoherent snapshot would 503 the unready routes,
			// so withhold until the referenced clusters are ready (cold-start
			// make-before-break, unchanged from the whole-snapshot gate).
			logger.Info("withholding first publish until referenced clusters are ready",
				"proxy_key", proxyKey,
				"missing_clusters", snapWrap.missingReferenced,
				"unusable_clusters", snapWrap.unusableReferenced,
			)
			return
		}
		snap = resolveDeferredPerCluster(snapWrap, published)
	}

	// The snapshot is EDS-consistent by construction: snapshotPerClient drops
	// CLAs for clusters absent from CDS and synthesizes empty assignments for
	// EDS clusters that have no CLA yet (see filterEndpointResourcesForClusters),
	// and the per-cluster resolution above only carries cluster/CLA pairs —
	// so we do not rely on a post-hoc MakeConsistent() pass, which would also
	// have mutated the snapshot shared with the krt cache.
	if err := s.xdsCache.SetSnapshot(ctx, proxyKey, snap); err != nil {
		// A rejected snapshot leaves the client on its previous config; surface
		// it rather than silently dropping the update.
		logger.Error("failed to set xds snapshot", "proxy_key", proxyKey, "error", err)
	}
}

// publishedReferencedClusters returns the dataplane-referenced cluster set of
// the currently-published snapshot (its routes and listeners).
func publishedReferencedClusters(published envoycache.ResourceSnapshot) map[string]struct{} {
	routes := envoycache.Resources{Items: published.GetResourcesAndTTL(envoyresourcev3.RouteType)}
	listeners := envoycache.Resources{Items: published.GetResourcesAndTTL(envoyresourcev3.ListenerType)}
	return collectReferencedClusters(routes, listeners)
}

// resolveDeferredPerCluster composes the snapshot actually published for a
// deferred wrapper, given the currently-published snapshot:
//
//   - missing referenced clusters present in the published snapshot are
//     carried forward together with their CLAs;
//   - if every remaining gap is a previously-referenced cluster (present in
//     the published routes' reference set), the new routes publish as-is —
//     including empty CLAs for scale-to-zero clusters;
//   - otherwise some gap is a newly-referenced cluster that has never been
//     ready: routes, listeners, and secrets are held at their published
//     versions (so nothing flips onto the unready cluster), the held routes'
//     cluster/CLA dependencies are carried forward, and the new CDS/EDS —
//     including the warming cluster — still goes out.
func resolveDeferredPerCluster(snapWrap XdsSnapWrapper, published envoycache.ResourceSnapshot) *envoycache.Snapshot {
	publishedRefs := publishedReferencedClusters(published)
	oldClusters := published.GetResourcesAndTTL(envoyresourcev3.ClusterType)

	// A gap blocks the route flip only if the published config was not
	// already using the cluster: a previously-referenced cluster that is
	// missing gets carried forward, and one with no usable endpoints
	// publishes its truth.
	var flipBlocking []string
	for _, name := range snapWrap.missingReferenced {
		if _, wasPublished := oldClusters[name]; wasPublished {
			continue // carried forward below
		}
		flipBlocking = append(flipBlocking, name)
	}
	for _, name := range snapWrap.unusableReferenced {
		if _, wasReferenced := publishedRefs[name]; wasReferenced {
			continue // previously-referenced: truth (empty CLA) publishes
		}
		flipBlocking = append(flipBlocking, name)
	}
	sort.Strings(flipBlocking)

	composed := &envoycache.Snapshot{}
	*composed = *snapWrap.snap

	// carryRefs are the references whose cluster/CLA pairs must exist in the
	// composed snapshot: the published routes' references when the flip is
	// held (those routes stay live), plus any previously-published missing
	// clusters on the truth path.
	carryRefs := make(map[string]struct{})
	if len(flipBlocking) > 0 {
		holdType := func(rt envoycachetypes.ResponseType, typeURL envoyresourcev3.Type) {
			composed.Resources[rt] = envoycache.Resources{
				Version: published.GetVersion(typeURL),
				Items:   published.GetResourcesAndTTL(typeURL),
			}
		}
		holdType(envoycachetypes.Route, envoyresourcev3.RouteType)
		holdType(envoycachetypes.Listener, envoyresourcev3.ListenerType)
		holdType(envoycachetypes.Secret, envoyresourcev3.SecretType)
		logger.Info("holding route flip until newly-referenced clusters are ready",
			"proxy_key", snapWrap.proxyKey,
			"flip_blocking", flipBlocking,
		)
		for name := range publishedRefs {
			carryRefs[name] = struct{}{}
		}
	} else {
		for _, name := range snapWrap.missingReferenced {
			carryRefs[name] = struct{}{}
		}
	}

	// Carry forward cluster/CLA pairs (a carried CLA always travels with
	// its cluster) for carryRefs missing from the new build.
	newClusters := composed.Resources[envoycachetypes.Cluster]
	newEndpoints := composed.Resources[envoycachetypes.Endpoint]
	oldEndpoints := published.GetResourcesAndTTL(envoyresourcev3.EndpointType)
	erroredSet := stringSet(snapWrap.erroredClusters)
	var carried []string
	clusterItems := newClusters.Items
	endpointItems := newEndpoints.Items
	for name := range carryRefs {
		if name == wellknown.BlackholeClusterName {
			continue
		}
		if _, errored := erroredSet[name]; errored {
			// Fail closed: a cluster whose current translation is errored is
			// never resurrected from the published snapshot, even while a flip
			// is held for an unrelated cluster. Serving it with its stale
			// (pre-error) config would silently bypass the very policy whose
			// failure errored it — e.g. an invalid BackendTLSPolicy must 5xx
			// (Gateway API conformance
			// BackendTLSPolicyInvalidCACertificateRef), not keep serving with
			// the previous TLS configuration.
			continue
		}
		if _, ok := clusterItems[name]; ok {
			continue
		}
		old, ok := oldClusters[name]
		if !ok {
			continue // neither snapshot has it; the route 503s as it already did
		}
		if len(carried) == 0 {
			// copy-on-write: never mutate the krt-cached wrapper's maps
			clusterItems = cloneResourceItems(newClusters.Items)
			endpointItems = cloneResourceItems(newEndpoints.Items)
		}
		clusterItems[name] = old
		carried = append(carried, name)
		claName, requiresEndpointResource := endpointResourceNameForCluster(old)
		if !requiresEndpointResource {
			continue
		}
		if oldCla, ok := oldEndpoints[claName]; ok {
			endpointItems[claName] = oldCla
		}
	}
	if len(carried) > 0 {
		sort.Strings(carried)
		var carryHash uint64
		for _, name := range carried {
			carryHash ^= utils.HashString(name)
		}
		composed.Resources[envoycachetypes.Cluster] = envoycache.Resources{
			Version: fmt.Sprintf("%s-carry-%d", newClusters.Version, carryHash),
			Items:   clusterItems,
		}
		composed.Resources[envoycachetypes.Endpoint] = envoycache.Resources{
			Version: fmt.Sprintf("%s-carry-%d", newEndpoints.Version, carryHash),
			Items:   endpointItems,
		}
		logger.Info("carried forward previously-published clusters",
			"proxy_key", snapWrap.proxyKey,
			"carried", carried,
		)
	}

	return composed
}

func cloneResourceItems(items map[string]envoycachetypes.ResourceWithTTL) map[string]envoycachetypes.ResourceWithTTL {
	out := make(map[string]envoycachetypes.ResourceWithTTL, len(items))
	maps.Copy(out, items)
	return out
}
