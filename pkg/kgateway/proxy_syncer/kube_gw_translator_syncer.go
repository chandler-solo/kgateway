package proxy_syncer

import (
	"context"
	"fmt"
	"maps"
	"strconv"

	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) {
	snap := snapWrap.snap
	proxyKey := snapWrap.proxyKey

	snap = s.withRestoredErroredClusters(proxyKey, snap, snapWrap.erroredClusters)

	// stringifying the snapshot may be an expensive operation, so we'd like to avoid building the large
	// string if we're not even going to log it anyway
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey)

	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	// if the snapshot is not consistent, make it so
	// TODO: me may need to copy this to not change krt cache.
	// TODO: this is also may not be needed now that envoy has
	// a default initial fetch timeout
	// snap.MakeConsistent()
	s.xdsCache.SetSnapshot(ctx, proxyKey, snap)
}

func (s *ProxyTranslator) withRestoredErroredClusters(
	proxyKey string,
	snap *envoycache.Snapshot,
	erroredClusters []string,
) *envoycache.Snapshot {
	if snap == nil || len(erroredClusters) == 0 {
		return snap
	}

	previousSnapshot, err := s.xdsCache.GetSnapshot(proxyKey)
	if err != nil {
		logger.Debug("no previous xds snapshot available for errored clusters", "proxy_key", proxyKey, "error", err)
		return snap
	}
	if previousSnapshot == nil {
		logger.Debug("previous xds snapshot is nil", "proxy_key", proxyKey)
		return snap
	}

	previousClusters := previousSnapshot.GetResourcesAndTTL(envoyresource.ClusterType)
	if len(previousClusters) == 0 {
		logger.Debug("previous xds snapshot has no clusters to restore", "proxy_key", proxyKey)
		return snap
	}

	clusterResources := snap.Resources[envoycachetypes.Cluster]
	clusterItems := make(map[string]envoycachetypes.ResourceWithTTL, len(clusterResources.Items)+len(erroredClusters))
	maps.Copy(clusterItems, clusterResources.Items)

	clusterVersionHash, err := strconv.ParseUint(clusterResources.Version, 10, 64)
	if err != nil {
		clusterVersionHash = utils.HashString(clusterResources.Version)
	}

	restoredClusters := make([]string, 0, len(erroredClusters))
	for _, clusterName := range erroredClusters {
		if _, ok := clusterItems[clusterName]; ok {
			continue
		}

		previousCluster, ok := previousClusters[clusterName]
		if !ok || previousCluster.Resource == nil {
			continue
		}

		clusterItems[clusterName] = previousCluster
		clusterVersionHash ^= utils.HashProto(previousCluster.Resource)
		restoredClusters = append(restoredClusters, clusterName)
	}
	if len(restoredClusters) == 0 {
		return snap
	}

	// Clone only the mutated cluster resource group so the KRT-produced snapshot is not rewritten.
	clusterResources.Items = clusterItems
	clusterResources.Version = fmt.Sprintf("%d", clusterVersionHash)

	restoredSnap := *snap
	restoredSnap.Resources[envoycachetypes.Cluster] = clusterResources
	restoredSnap.VersionMap = nil

	logger.Info(
		"restored errored clusters from previous xds snapshot",
		"proxy_key", proxyKey,
		"clusters", restoredClusters,
	)

	return &restoredSnap
}
