package proxy_syncer

import (
	"context"

	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

// publicationGate is the bookkeeping seam for publication, implemented by
// perClientReconciler: SetSnapshot happens atomically with the per-client
// state it updates (published/deferred/orphan clock), so cache reclaim and
// publication are totally ordered. Nil (used by tests) publishes straight to
// the cache.
type publicationGate interface {
	commitPublish(ctx context.Context, snapWrap XdsSnapWrapper) (recovered bool)
}

// syncXds publishes the per-client snapshot to the xDS cache. Snapshots
// reaching this point are coherent by construction: snapshotPerClient defers
// (emits nothing) until every referenced cluster and required CLA is present,
// so publication itself carries no policy — see
// devel/architecture/perclient-xds-publication.md.
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) {
	proxyKey := snapWrap.proxyKey

	// stringifying the snapshot may be an expensive operation, so we'd like to avoid building the large
	// string if we're not even going to log it anyway
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey)

	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	if s.gate == nil {
		if err := s.xdsCache.SetSnapshot(ctx, proxyKey, snapWrap.snap); err != nil {
			logger.Error("failed to set xds snapshot", "proxy_key", proxyKey, "error", err)
		}
		return
	}
	if recovered := s.gate.commitPublish(ctx, snapWrap); recovered {
		// A publish after a prior defer is a recovery; with the heartbeat as
		// backstop, recoveries of long-deferred clients are heartbeat-driven
		// heals (#14184).
		recordSnapshotRecovery(proxyKey)
	}
}
