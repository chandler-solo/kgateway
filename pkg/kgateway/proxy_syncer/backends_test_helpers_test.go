package proxy_syncer

import (
	"istio.io/istio/pkg/kube/krt"

	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// testClusterCols wraps the two static collections that back a test-built
// PerClientEnvoyClusters. Tests use UpdateObject on these to mutate state.
type testClusterCols struct {
	bases  krt.StaticCollection[baseEnvoyCluster]
	deltas krt.StaticCollection[uccClusterDelta]
}

// updateDelta inserts or replaces a per-client delta entry.
func (c *testClusterCols) updateDelta(in uccWithCluster) {
	c.deltas.UpdateObject(uccClusterDelta{
		Client:         in.Client,
		Name:           in.Name,
		Cluster:        in.Cluster,
		ClusterVersion: in.ClusterVersion,
	})
}

// newTestPerClientClustersRaw builds a PerClientEnvoyClusters directly from
// base and delta entries, giving tests full control over the merge inputs
// (e.g. a base and a delta sharing a name). Prefer newTestPerClientClusters
// for the common flat-entry case.
func newTestPerClientClustersRaw(bases []baseEnvoyCluster, deltas []uccClusterDelta) PerClientEnvoyClusters {
	baseCol := krt.NewStaticCollection[baseEnvoyCluster](nil, bases)
	deltaCol := krt.NewStaticCollection[uccClusterDelta](nil, deltas)
	idx := krtpkg.UnnamedIndex(deltaCol, func(d uccClusterDelta) []string {
		return []string{d.Client.ResourceName()}
	})
	return PerClientEnvoyClusters{
		base:       baseCol,
		deltas:     deltaCol,
		deltaByUcc: idx,
	}
}

// newTestPerClientClustersFromCol builds a PerClientEnvoyClusters whose deltas
// are derived 1:1 from a mutable uccWithCluster collection, preserving the
// pre-#14104 flat-shape test idiom: tests keep their static source collection
// and mutate it with UpdateObject/DeleteObject, and the derived delta
// collection follows. Every entry becomes a standalone per-client delta (no
// bases), which FetchClustersForClient surfaces verbatim, including errors.
func newTestPerClientClustersFromCol(col krt.Collection[uccWithCluster]) PerClientEnvoyClusters {
	deltas := krt.NewCollection(col, func(_ krt.HandlerContext, c uccWithCluster) *uccClusterDelta {
		return &uccClusterDelta{
			Client:         c.Client,
			Name:           c.Name,
			Cluster:        c.Cluster,
			ClusterVersion: c.ClusterVersion,
			Error:          c.Error,
		}
	})
	idx := krtpkg.UnnamedIndex(deltas, func(d uccClusterDelta) []string {
		return []string{d.Client.ResourceName()}
	})
	return PerClientEnvoyClusters{deltas: deltas, deltaByUcc: idx}
}

// newTestPerClientClusters builds a PerClientEnvoyClusters from flat
// per-(ucc, cluster) entries. Errored entries are routed to the base
// collection (where errors live in production); non-errored entries become
// per-client deltas. The returned testClusterCols exposes UpdateObject hooks
// for tests that mutate state mid-flight.
func newTestPerClientClusters(initial []uccWithCluster) (PerClientEnvoyClusters, *testClusterCols) {
	var bases []baseEnvoyCluster
	var deltas []uccClusterDelta
	for _, c := range initial {
		if c.Error != nil {
			bases = append(bases, baseEnvoyCluster{
				Name:           c.Name,
				Cluster:        c.Cluster,
				ClusterVersion: c.ClusterVersion,
				Error:          c.Error,
			})
			continue
		}
		deltas = append(deltas, uccClusterDelta{
			Client:         c.Client,
			Name:           c.Name,
			Cluster:        c.Cluster,
			ClusterVersion: c.ClusterVersion,
		})
	}
	baseCol := krt.NewStaticCollection[baseEnvoyCluster](nil, bases)
	deltaCol := krt.NewStaticCollection[uccClusterDelta](nil, deltas)
	idx := krtpkg.UnnamedIndex(deltaCol, func(d uccClusterDelta) []string {
		return []string{d.Client.ResourceName()}
	})
	pcc := PerClientEnvoyClusters{
		base:       baseCol,
		deltas:     deltaCol,
		deltaByUcc: idx,
	}
	return pcc, &testClusterCols{bases: baseCol, deltas: deltaCol}
}
