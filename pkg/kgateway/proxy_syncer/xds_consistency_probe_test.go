package proxy_syncer

// Probe: what does go-control-plane's ads=true SnapshotCache do with an
// EDS-INCONSISTENT snapshot — a CDS cluster (EDS type) that has no matching
// ClusterLoadAssignment? The property test (perclient_property_test.go)
// showed snapshotPerClient can emit such a snapshot for a cluster no route
// references, and syncXds publishes it blind (MakeConsistent is commented
// out and the SetSnapshot error is discarded). This probe settles the
// severity: does SetSnapshot reject it (kgateway would then silently strand
// the client), does the orphan stall the EDS watch for OTHER clusters
// (traffic-affecting), or is the orphan tolerated (the warming cluster is
// harmless)? It also pins the assumption for future go-control-plane bumps.

import (
	"context"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoydiscoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	envoystreamv3 "github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
	"github.com/onsi/gomega"
)

func edsClusterProto(name string) *envoyclusterv3.Cluster {
	return &envoyclusterv3.Cluster{
		Name:                 name,
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
	}
}

func claProto(name string) *envoyendpointv3.ClusterLoadAssignment {
	return &envoyendpointv3.ClusterLoadAssignment{
		ClusterName: name,
		Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{
			LbEndpoints: []*envoyendpointv3.LbEndpoint{{
				HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{
					Endpoint: &envoyendpointv3.Endpoint{
						Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{
							SocketAddress: &envoycorev3.SocketAddress{
								Address:       "127.0.0.1",
								PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 8080},
							},
						}},
					},
				},
			}},
		}},
	}
}

// TestGoControlPlaneOrphanEDSClusterBehavior builds a snapshot with two EDS
// clusters in CDS but a CLA for only one of them (the other is the orphan the
// property test surfaced), then observes ads=true cache behavior.
func TestGoControlPlaneOrphanEDSClusterBehavior(t *testing.T) {
	g := gomega.NewWithT(t)

	const version = "v1"
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = envoycache.NewResourcesWithTTL(version, []envoycachetypes.ResourceWithTTL{
		{Resource: edsClusterProto("cluster-orphan")}, // in CDS, NO CLA
		{Resource: edsClusterProto("cluster-ok")},     // in CDS, HAS CLA
	})
	snap.Resources[envoycachetypes.Endpoint] = envoycache.NewResourcesWithTTL(version, []envoycachetypes.ResourceWithTTL{
		{Resource: claProto("cluster-ok")},
	})

	// Q: is this snapshot internally consistent per go-control-plane?
	consistentErr := snap.Consistent()
	t.Logf("snapshot.Consistent() => %v", consistentErr)
	g.Expect(consistentErr).To(gomega.HaveOccurred(),
		"the orphan-EDS-cluster snapshot should be reported inconsistent by go-control-plane")

	// Q1: does SetSnapshot (the exact call syncXds makes, ads=true) reject it?
	cache := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	nodeID := "node-1"
	setErr := cache.SetSnapshot(context.Background(), nodeID, snap)
	t.Logf("ads=true SetSnapshot(inconsistent) => %v", setErr)

	if setErr != nil {
		// kgateway discards this error (kube_gw_translator_syncer.go:29), so a
		// rejection here means the client is silently stranded on stale config.
		t.Fatalf("SEVERE: SetSnapshot rejects the inconsistent snapshot (%v); syncXds ignores the error, so the client would be stranded", setErr)
	}

	// Q2: does the orphan stall the EDS watch for the OTHER (healthy) cluster?
	// Envoy requests EDS for both clusters it learned from CDS.
	req := &envoydiscoveryv3.DiscoveryRequest{
		Node:          &envoycorev3.Node{Id: nodeID},
		TypeUrl:       envoyresourcev3.EndpointType,
		ResourceNames: []string{"cluster-orphan", "cluster-ok"},
	}
	sub := envoystreamv3.NewSotwSubscription(req.GetResourceNames(), true)
	responses := make(chan envoycache.Response, 1)
	_, err := cache.CreateWatch(req, sub, responses)
	g.Expect(err).ToNot(gomega.HaveOccurred())

	select {
	case response := <-responses:
		returned := response.GetReturnedResources()
		t.Logf("EDS watch for [cluster-orphan, cluster-ok] responded with: %v", returned)
		// The healthy cluster's endpoints must be served despite the orphan.
		if _, ok := returned["cluster-ok"]; !ok {
			t.Fatalf("SEVERE: orphan EDS cluster suppressed the EDS response for the healthy cluster (traffic-affecting); returned=%v", returned)
		}
		t.Logf("TOLERATED: healthy cluster served; orphan 'cluster-orphan' simply absent (it warms alone, no traffic impact)")
	case <-time.After(time.Second):
		t.Fatal("SEVERE: EDS watch did not respond at all with an orphan EDS cluster present (whole-stream stall)")
	}
}
