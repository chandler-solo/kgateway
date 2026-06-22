package proxy_syncer

import (
	"context"

	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) {
	snap := snapWrap.snap
	proxyKey := snapWrap.proxyKey

	// TODO: handle errored clusters by fetching them from the previous snapshot and using the old cluster

	// stringifying the snapshot may be an expensive operation, so we'd like to avoid building the large
	// string if we're not even going to log it anyway
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey)

	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	// The snapshot is EDS-consistent by construction: snapshotPerClient drops
	// CLAs for clusters absent from CDS and synthesizes empty assignments for
	// EDS clusters that have no CLA yet (see filterEndpointResourcesForClusters),
	// so we no longer rely on a post-hoc MakeConsistent() pass — which would
	// also have mutated the snapshot shared with the krt cache.
	if err := s.xdsCache.SetSnapshot(ctx, proxyKey, snap); err != nil {
		// A rejected snapshot leaves the client on its previous config; surface
		// it rather than silently dropping the update.
		logger.Error("failed to set xds snapshot", "proxy_key", proxyKey, "error", err)
	}
}
