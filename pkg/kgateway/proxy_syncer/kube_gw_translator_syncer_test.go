package proxy_syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/onsi/gomega"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

func TestSyncXdsRestoresErroredClustersFromPreviousSnapshot(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := context.Background()
	proxyKey := "test-proxy"
	xdsCache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	translator := NewProxyTranslator(xdsCache)

	healthyCluster := testCluster("cluster-a", time.Second)
	previousErroredCluster := testCluster("cluster-b", 2*time.Second)
	previousVersion := testClusterVersion(healthyCluster, previousErroredCluster)
	previousSnap := testSnapshotWithClusters(previousVersion, healthyCluster, previousErroredCluster)
	g.Expect(xdsCache.SetSnapshot(ctx, proxyKey, previousSnap)).To(gomega.Succeed())

	incomingSnap := testSnapshotWithClusters(testClusterVersion(healthyCluster), healthyCluster)
	translator.syncXds(ctx, XdsSnapWrapper{
		snap:            incomingSnap,
		proxyKey:        proxyKey,
		erroredClusters: []string{"cluster-b", "cluster-missing"},
	})

	storedSnapshot, err := xdsCache.GetSnapshot(proxyKey)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	storedClusters := storedSnapshot.GetResourcesAndTTL(envoyresource.ClusterType)

	g.Expect(storedClusters).To(gomega.HaveKey("cluster-a"))
	g.Expect(storedClusters).To(gomega.HaveKey("cluster-b"))
	g.Expect(storedClusters).NotTo(gomega.HaveKey("cluster-missing"))
	g.Expect(proto.Equal(storedClusters["cluster-b"].Resource, previousErroredCluster)).To(gomega.BeTrue())
	g.Expect(storedSnapshot.GetVersion(envoyresource.ClusterType)).To(gomega.Equal(previousVersion))

	g.Expect(incomingSnap.Resources[envoycachetypes.Cluster].Items).NotTo(gomega.HaveKey("cluster-b"),
		"restoring old clusters must not mutate the KRT-produced snapshot")
}

func TestSyncXdsRestoresErroredClustersAfterHealthyClusterChange(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := context.Background()
	proxyKey := "test-proxy"
	xdsCache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	translator := NewProxyTranslator(xdsCache)

	previousHealthyCluster := testCluster("cluster-a", time.Second)
	previousErroredCluster := testCluster("cluster-b", 2*time.Second)
	previousSnap := testSnapshotWithClusters(
		testClusterVersion(previousHealthyCluster, previousErroredCluster),
		previousHealthyCluster,
		previousErroredCluster,
	)
	g.Expect(xdsCache.SetSnapshot(ctx, proxyKey, previousSnap)).To(gomega.Succeed())

	updatedHealthyCluster := testCluster("cluster-a", 3*time.Second)
	expectedVersion := testClusterVersion(updatedHealthyCluster, previousErroredCluster)
	incomingSnap := testSnapshotWithClusters(testClusterVersion(updatedHealthyCluster), updatedHealthyCluster)
	translator.syncXds(ctx, XdsSnapWrapper{
		snap:            incomingSnap,
		proxyKey:        proxyKey,
		erroredClusters: []string{"cluster-b"},
	})

	storedSnapshot, err := xdsCache.GetSnapshot(proxyKey)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	storedClusters := storedSnapshot.GetResourcesAndTTL(envoyresource.ClusterType)

	g.Expect(proto.Equal(storedClusters["cluster-a"].Resource, updatedHealthyCluster)).To(gomega.BeTrue())
	g.Expect(proto.Equal(storedClusters["cluster-b"].Resource, previousErroredCluster)).To(gomega.BeTrue())
	g.Expect(storedSnapshot.GetVersion(envoyresource.ClusterType)).To(gomega.Equal(expectedVersion))
}

func TestSyncXdsDoesNotOverwriteCurrentClusterWhenRestoringErroredClusters(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := context.Background()
	proxyKey := "test-proxy"
	xdsCache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	translator := NewProxyTranslator(xdsCache)

	previousCluster := testCluster("cluster-a", time.Second)
	g.Expect(xdsCache.SetSnapshot(ctx, proxyKey, testSnapshotWithClusters(testClusterVersion(previousCluster), previousCluster))).To(gomega.Succeed())

	currentCluster := testCluster("cluster-a", 2*time.Second)
	currentVersion := testClusterVersion(currentCluster)
	translator.syncXds(ctx, XdsSnapWrapper{
		snap:            testSnapshotWithClusters(currentVersion, currentCluster),
		proxyKey:        proxyKey,
		erroredClusters: []string{"cluster-a"},
	})

	storedSnapshot, err := xdsCache.GetSnapshot(proxyKey)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	storedClusters := storedSnapshot.GetResourcesAndTTL(envoyresource.ClusterType)

	g.Expect(proto.Equal(storedClusters["cluster-a"].Resource, currentCluster)).To(gomega.BeTrue())
	g.Expect(storedSnapshot.GetVersion(envoyresource.ClusterType)).To(gomega.Equal(currentVersion))
}

func testSnapshotWithClusters(version string, clusters ...*envoyclusterv3.Cluster) *envoycache.Snapshot {
	resources := make([]envoycachetypes.ResourceWithTTL, 0, len(clusters))
	for _, cluster := range clusters {
		resources = append(resources, envoycachetypes.ResourceWithTTL{Resource: cluster})
	}

	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = envoycache.NewResourcesWithTTL(version, resources)
	return snap
}

func testClusterVersion(clusters ...*envoyclusterv3.Cluster) string {
	var hash uint64
	for _, cluster := range clusters {
		hash ^= utils.HashProto(cluster)
	}
	return fmt.Sprintf("%d", hash)
}

func testCluster(name string, connectTimeout time.Duration) *envoyclusterv3.Cluster {
	return &envoyclusterv3.Cluster{
		Name:           name,
		ConnectTimeout: durationpb.New(connectTimeout),
	}
}
