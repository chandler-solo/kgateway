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

// updateBase inserts or replaces a base cluster entry. Used by tests that
// model errored clusters (errors live on base in production).
func (c *testClusterCols) updateBase(in uccWithCluster) {
	c.bases.UpdateObject(baseEnvoyCluster{
		Name:           in.Name,
		Cluster:        in.Cluster,
		ClusterVersion: in.ClusterVersion,
		Error:          in.Error,
	})
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
