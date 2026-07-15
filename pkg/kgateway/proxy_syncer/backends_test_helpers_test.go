package proxy_syncer

import (
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

// testClusterCols keeps the static collections backing a test-built
// PerClientEnvoyClusters alive and available to tests that need direct access.
type testClusterCols struct {
	bases   krt.StaticCollection[baseEnvoyCluster]
	deltas  krt.StaticCollection[backendClusterDeltaSet]
	clients krt.StaticCollection[ir.UniquelyConnectedClient]
}

// newTestPerClientClustersRaw builds a generation-consistent
// PerClientEnvoyClusters directly from base and sparse delta entries. clients
// must include every UCC that the caller will query; passing them explicitly
// models the production resolved-client fence.
func newTestPerClientClustersRaw(
	bases []baseEnvoyCluster,
	deltas []uccClusterDelta,
	clients ...ir.UniquelyConnectedClient,
) PerClientEnvoyClusters {
	clientFingerprint := fingerprintClients(clients)
	deltaSets := make([]backendClusterDeltaSet, 0, len(bases))
	for _, base := range bases {
		set := backendClusterDeltaSet{
			Name:               base.Name,
			BaseFingerprint:    base.Fingerprint,
			ClientsFingerprint: clientFingerprint,
		}
		for _, delta := range deltas {
			if delta.Name != base.Name {
				continue
			}
			delta.BaseFingerprint = base.Fingerprint
			if set.Deltas == nil {
				set.Deltas = make(map[string]uccClusterDelta)
			}
			set.Deltas[delta.Client.ResourceName()] = delta
		}
		deltaSets = append(deltaSets, set)
	}

	baseCol := krt.NewStaticCollection[baseEnvoyCluster](nil, bases)
	deltaCol := krt.NewStaticCollection[backendClusterDeltaSet](nil, deltaSets)
	clientCol := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, clients)
	return PerClientEnvoyClusters{
		base:    baseCol,
		deltas:  deltaCol,
		clients: clientCol,
	}
}

// newTestPerClientClusters builds a PerClientEnvoyClusters from flat cluster
// entries. These snapshot tests do not exercise overlays, so each entry is a
// resolved shared base and each distinct client is included in the client set.
func newTestPerClientClusters(initial []uccWithCluster) (PerClientEnvoyClusters, *testClusterCols) {
	basesByName := make(map[string]baseEnvoyCluster)
	clientsByName := make(map[string]ir.UniquelyConnectedClient)
	for _, cluster := range initial {
		basesByName[cluster.Name] = baseEnvoyCluster{
			Name:              cluster.Name,
			Cluster:           cluster.Cluster,
			ClusterVersion:    cluster.ClusterVersion,
			Error:             cluster.Error,
			BackendSource:     cluster.BackendSource,
			BackendGeneration: cluster.BackendGeneration,
		}
		clientsByName[cluster.Client.ResourceName()] = cluster.Client
	}

	bases := make([]baseEnvoyCluster, 0, len(basesByName))
	for _, base := range basesByName {
		bases = append(bases, base)
	}
	clients := make([]ir.UniquelyConnectedClient, 0, len(clientsByName))
	for _, client := range clientsByName {
		clients = append(clients, client)
	}

	clientFingerprint := fingerprintClients(clients)
	deltaSets := make([]backendClusterDeltaSet, 0, len(bases))
	for _, base := range bases {
		deltaSets = append(deltaSets, backendClusterDeltaSet{
			Name:               base.Name,
			BaseFingerprint:    base.Fingerprint,
			ClientsFingerprint: clientFingerprint,
		})
	}

	baseCol := krt.NewStaticCollection[baseEnvoyCluster](nil, bases)
	deltaCol := krt.NewStaticCollection[backendClusterDeltaSet](nil, deltaSets)
	clientCol := krt.NewStaticCollection[ir.UniquelyConnectedClient](nil, clients)
	pcc := PerClientEnvoyClusters{base: baseCol, deltas: deltaCol, clients: clientCol}
	return pcc, &testClusterCols{bases: baseCol, deltas: deltaCol, clients: clientCol}
}
