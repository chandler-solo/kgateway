package proxy_syncer

import (
	"errors"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

func clusterNamed(name string) *envoyclusterv3.Cluster {
	return &envoyclusterv3.Cluster{Name: name}
}

func uccWithClusterByName(got []uccWithCluster) map[string]uccWithCluster {
	m := make(map[string]uccWithCluster, len(got))
	for _, c := range got {
		m[c.Name] = c
	}
	return m
}

func waitSynced(t *testing.T, pcc PerClientEnvoyClusters) {
	t.Helper()
	require.Eventually(t, pcc.HasSynced, time.Second, 10*time.Millisecond)
}

// TestFetchClustersForClient_Merge exercises the base/delta merge: a base with
// no delta passes through unchanged, a delta overlays the base of the same name
// (winning on cluster + version), and a standalone delta (test-only) is still
// surfaced.
func TestFetchClustersForClient_Merge(t *testing.T) {
	ucc := ir.NewUniquelyConnectedClient("role", "ns", map[string]string{"k": "a"}, ir.PodLocality{})

	baseOnly := clusterNamed("base-only")
	overlaidBase := clusterNamed("overlaid")
	overlaidDelta := clusterNamed("overlaid")
	standalone := clusterNamed("standalone")

	bases := []baseEnvoyCluster{
		{Name: "base-only", Cluster: baseOnly, ClusterVersion: 1},
		{Name: "overlaid", Cluster: overlaidBase, ClusterVersion: 2},
	}
	deltas := []uccClusterDelta{
		{Client: ucc, Name: "overlaid", Cluster: overlaidDelta, ClusterVersion: 99},
		{Client: ucc, Name: "standalone", Cluster: standalone, ClusterVersion: 7},
	}

	pcc := newTestPerClientClustersRaw(bases, deltas)
	waitSynced(t, pcc)

	got := uccWithClusterByName(pcc.FetchClustersForClient(krt.TestingDummyContext{}, ucc))
	require.Len(t, got, 3)

	// base with no delta passes through unchanged
	require.Same(t, baseOnly, got["base-only"].Cluster)
	require.Equal(t, uint64(1), got["base-only"].ClusterVersion)

	// delta overlays the base of the same name, winning on cluster + version
	require.Same(t, overlaidDelta, got["overlaid"].Cluster)
	require.Equal(t, uint64(99), got["overlaid"].ClusterVersion)

	// standalone delta (no matching base) is still surfaced
	require.Same(t, standalone, got["standalone"].Cluster)
	require.Equal(t, uint64(7), got["standalone"].ClusterVersion)
}

// TestFetchClustersForClient_DeltaErrorWinsOverBaseError documents the error
// precedence in the merge: a per-UCC delta error is the more specific signal and
// takes precedence over a base error of the same name.
func TestFetchClustersForClient_DeltaErrorWinsOverBaseError(t *testing.T) {
	ucc := ir.NewUniquelyConnectedClient("role", "ns", map[string]string{"k": "a"}, ir.PodLocality{})
	baseErr := errors.New("base boom")
	deltaErr := errors.New("delta boom")

	bases := []baseEnvoyCluster{{Name: "c", Cluster: clusterNamed("c"), ClusterVersion: 1, Error: baseErr}}
	deltas := []uccClusterDelta{{Client: ucc, Name: "c", Cluster: clusterNamed("c"), ClusterVersion: 2, Error: deltaErr}}

	pcc := newTestPerClientClustersRaw(bases, deltas)
	waitSynced(t, pcc)

	got := pcc.FetchClustersForClient(krt.TestingDummyContext{}, ucc)
	require.Len(t, got, 1)
	require.Equal(t, deltaErr, got[0].Error, "delta error should win over base error")
}

// TestFetchClustersForClient_FiltersByClient confirms a delta is scoped to its
// own UCC: the owning client sees the override while another client falls back
// to the shared base.
func TestFetchClustersForClient_FiltersByClient(t *testing.T) {
	uccA := ir.NewUniquelyConnectedClient("role", "ns", map[string]string{"k": "a"}, ir.PodLocality{})
	uccB := ir.NewUniquelyConnectedClient("role", "ns", map[string]string{"k": "b"}, ir.PodLocality{})

	base := clusterNamed("c")
	bases := []baseEnvoyCluster{{Name: "c", Cluster: base, ClusterVersion: 1}}
	deltas := []uccClusterDelta{{Client: uccA, Name: "c", Cluster: clusterNamed("c-a"), ClusterVersion: 50}}

	pcc := newTestPerClientClustersRaw(bases, deltas)
	waitSynced(t, pcc)

	// uccA sees its delta override
	gotA := pcc.FetchClustersForClient(krt.TestingDummyContext{}, uccA)
	require.Len(t, gotA, 1)
	require.Equal(t, uint64(50), gotA[0].ClusterVersion)

	// uccB has no delta, sees the shared base proto
	gotB := pcc.FetchClustersForClient(krt.TestingDummyContext{}, uccB)
	require.Len(t, gotB, 1)
	require.Same(t, base, gotB[0].Cluster)
	require.Equal(t, uint64(1), gotB[0].ClusterVersion)
}
