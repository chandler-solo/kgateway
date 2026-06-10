package proxy_syncer

import (
	"context"
	"fmt"
	"maps"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

// warmupGate reports whether publication for a client should still be deferred
// because the client has never been published and its warm-up deadline has not
// expired. Implemented by perClientReconciler.
type warmupGate interface {
	shouldDeferWarmup(clientKey string) bool
}

// syncXds publishes the per-client snapshot to the xDS cache. It reports
// whether a snapshot was actually published; false means the update was
// withheld (invalid snapshot, or a warm-up deferral) — the caller should treat
// the client as still stuck so the heartbeat keeps retrying.
//
// Publication policy (devel/architecture/perclient-xds-publication.md):
//
//   - Hard validation (malformed/nil/mistyped resources, inconsistent
//     snapshot, missing SDS references) always withholds: these indicate bugs,
//     and Envoy keeps the last good snapshot.
//   - Required-but-missing CLAs are synthesized empty so the snapshot stays
//     consistent and Envoy is not stalled on initial_fetch_timeout; the real
//     CLA replaces the empty one when the endpoints pipeline produces it.
//   - Missing dataplane cluster references defer publication ONLY while the
//     client has never been published and its warm-up deadline has not expired
//     (the reconnect race #13868 addressed). Afterwards the snapshot publishes
//     as-is: the affected routes return no-cluster errors transiently, exactly
//     as before #13868, and heal on the next input event or heartbeat tick.
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) bool {
	snap := snapWrap.snap
	proxyKey := snapWrap.proxyKey

	// stringifying the snapshot may be an expensive operation, so we'd like to avoid building the large
	// string if we're not even going to log it anyway
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey)

	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	snap = synthesizeMissingEndpointResources(snap)

	if err := ValidateXDSSnapshot(snap); err != nil {
		logger.Error("invalid xds snapshot; preserving last published snapshot",
			"proxy_key", proxyKey, "error", err)
		recordSnapshotDefer(proxyKey, "invalid_snapshot")
		return false
	}

	if missing := missingSnapshotClusterReferences(snap, snapWrap.erroredClusters); len(missing) > 0 {
		if s.warmup != nil && s.warmup.shouldDeferWarmup(proxyKey) {
			logger.Info("deferring first publish during client warm-up until referenced clusters are ready",
				"proxy_key", proxyKey, "missing_clusters", missing)
			recordSnapshotDefer(proxyKey, "warmup")
			return false
		}
		// Post-warm-up (or deadline expired): publish as-is. The referencing
		// routes transiently return no-cluster errors until the per-client
		// pipeline produces the clusters — the pre-#13868 behavior, now bounded
		// by the heartbeat.
		logger.Warn("publishing snapshot with referenced clusters missing from per-client inputs",
			"proxy_key", proxyKey, "missing_clusters", missing)
		recordSnapshotDefer(proxyKey, "missing_clusters_published")
	}

	if err := s.xdsCache.SetSnapshot(ctx, proxyKey, snap); err != nil {
		logger.Error("failed to set xds snapshot", "proxy_key", proxyKey, "error", err)
		return false
	}
	return true
}

// synthesizeMissingEndpointResources returns the snapshot with an empty
// ClusterLoadAssignment added for every EDS cluster whose required CLA name is
// absent from the snapshot's endpoint resources (an endpoints-side hole: the
// endpoints pipeline always emits a CLA — even an empty one — for backends it
// knows). The synthesized CLA keeps go-control-plane's consistency check
// satisfied and lets the cluster finish warming immediately instead of waiting
// out initial_fetch_timeout; the affected backend serves no-healthy-upstream
// until the real CLA arrives (bounded by the heartbeat). The input snapshot is
// never mutated.
func synthesizeMissingEndpointResources(snap *envoycache.Snapshot) *envoycache.Snapshot {
	required := requiredEndpointResourceNames(snap.Resources[envoycachetypes.Cluster].Items)
	endpointItems := snap.Resources[envoycachetypes.Endpoint].Items
	var missing []string
	for name := range required {
		if _, ok := endpointItems[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return snap
	}

	items := maps.Clone(endpointItems)
	if items == nil {
		items = map[string]envoycachetypes.ResourceWithTTL{}
	}
	var synthHash uint64
	for _, name := range missing {
		items[name] = envoycachetypes.ResourceWithTTL{
			Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: name},
		}
		synthHash ^= utils.HashString(name)
	}

	out := &envoycache.Snapshot{}
	out.Resources = snap.Resources
	out.Resources[envoycachetypes.Endpoint] = envoycache.Resources{
		// Combine with the upstream version so the EDS version changes when the
		// synthesized set does, and reverts to the upstream derivation once the
		// real CLAs arrive.
		Version: fmt.Sprintf("%d", utils.HashString(snap.Resources[envoycachetypes.Endpoint].Version)^synthHash),
		Items:   items,
	}
	return out
}
