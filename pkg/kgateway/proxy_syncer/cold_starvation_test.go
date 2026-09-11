package proxy_syncer

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
)

// RF-003: a cold proxy whose candidate references a cluster that never appears
// in CDS and is never recorded as errored receives no configuration at all,
// however many unrelated revisions arrive. Every type is withheld, including a
// fully ready unrelated listener/route/cluster and a rotating secret.
//
// This is a cache-boundary probe of the research policy in syncXds. It does
// not drive Envoy, and it does not decide whether a permanently missing
// reference is legitimate input. It pins the current behavior so a later
// policy choice (treat as errored after a bound, degrade, or keep waiting)
// is made against reproduced evidence rather than a narrative.
func TestColdMissingReferencedClusterWithholdsAllTypes(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, cache.IDHash{}, nil)
	translator := NewProxyTranslator(c)

	var decisions []string
	previousSink := xdsSnapshotTraceSink
	xdsSnapshotTraceSink = func(event XdsSnapshotTraceEvent) {
		decisions = append(decisions, event.Decision)
		if previousSink != nil {
			previousSink(event)
		}
	}
	t.Cleanup(func() { xdsSnapshotTraceSink = previousSink })

	// "ghost" is referenced by the candidate routes but has no CDS entry. The
	// ready path ("ready" cluster with a usable CLA, its own listener and
	// route configuration) is unrelated to the missing reference.
	makeSnapshot := func(revision int, includeGhostCluster bool) *cache.Snapshot {
		t.Helper()
		clusters := []types.Resource{
			&envoyclusterv3.Cluster{
				Name: "ready", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
				EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: &envoycorev3.ConfigSource{ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{Ads: &envoycorev3.AggregatedConfigSource{}}}},
			},
		}
		endpoints := []types.Resource{&envoyendpointv3.ClusterLoadAssignment{
			ClusterName: "ready",
			Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{
				HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{
					SocketAddress: &envoycorev3.SocketAddress{Address: "10.0.0.1", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 8080}},
				}}}},
			}}}},
		}}
		if includeGhostCluster {
			clusters = append(clusters, &envoyclusterv3.Cluster{Name: "ghost"})
		}
		routes := []types.Resource{&envoyroutev3.RouteConfiguration{Name: "unrelated-ready-route"}}
		for _, item := range routeResourcesForClusters("ready", "ghost").Items {
			routes = append(routes, item.Resource)
		}
		resources := map[resource.Type][]types.Resource{
			resource.ClusterType:  clusters,
			resource.EndpointType: endpoints,
			resource.RouteType:    routes,
			resource.ListenerType: {
				httpListenerWithRDS(t, "ready-listener", "unrelated-ready-route"),
				httpListenerWithRDS(t, "shared-listener", "route-config"),
			},
			resource.SecretType: {&envoytlsv3.Secret{Name: "rotating-secret", Type: &envoytlsv3.Secret_GenericSecret{
				GenericSecret: &envoytlsv3.GenericSecret{Secret: &envoycorev3.DataSource{Specifier: &envoycorev3.DataSource_InlineString{
					InlineString: fmt.Sprintf("public-test-revision-%d", revision),
				}}},
			}}},
		}
		snap, err := cache.NewSnapshot(strconv.Itoa(revision), resources)
		if err != nil {
			t.Fatal(err)
		}
		return snap
	}

	const revisions = 10
	for revision := 1; revision <= revisions; revision++ {
		translator.syncXds(ctx, XdsSnapWrapper{
			snap: makeSnapshot(revision, false), proxyKey: "cold", deferred: true, missingReferenced: []string{"ghost"},
		})
		if _, err := c.GetSnapshot("cold"); err == nil {
			t.Fatalf("revision %d: cold proxy received a snapshot while a referenced cluster is missing; reassess RF-003", revision)
		}
	}
	if len(decisions) != revisions {
		t.Fatalf("expected %d trace decisions, got %d: %v", revisions, len(decisions), decisions)
	}
	for i, decision := range decisions {
		if decision != xdsTraceDecisionDeferFirstPublish {
			t.Fatalf("revision %d: decision %q, want %q", i+1, decision, xdsTraceDecisionDeferFirstPublish)
		}
	}

	// The same reference recorded as errored is exempt: the proxy receives the
	// unrelated ready configuration and a fail-closed dangling route. This is
	// the existing per-cluster fail-closed outcome, not a new policy.
	translator.syncXds(ctx, XdsSnapWrapper{
		snap: makeSnapshot(revisions+1, false), proxyKey: "cold", deferred: false, erroredClusters: []string{"ghost"},
	})
	served, err := c.GetSnapshot("cold")
	if err != nil {
		t.Fatalf("errored reference must not withhold publication: %v", err)
	}
	if _, exists := served.GetResources(resource.ClusterType)["ghost"]; exists {
		t.Fatal("errored cluster must not be published")
	}
	if _, exists := served.GetResources(resource.ListenerType)["ready-listener"]; !exists {
		t.Fatal("unrelated ready listener missing after errored-reference publication")
	}

	// A fresh cold proxy whose missing reference later arrives in CDS publishes
	// on that revision: the withhold is transient-lag tolerant by construction.
	// Nothing in syncXds distinguishes this arrival from one that never comes.
	translator.syncXds(ctx, XdsSnapWrapper{
		snap: makeSnapshot(1, false), proxyKey: "cold-transient", deferred: true, missingReferenced: []string{"ghost"},
	})
	if _, err := c.GetSnapshot("cold-transient"); err == nil {
		t.Fatal("transient case published before the reference resolved")
	}
	translator.syncXds(ctx, XdsSnapWrapper{snap: makeSnapshot(2, true), proxyKey: "cold-transient"})
	served, err = c.GetSnapshot("cold-transient")
	if err != nil {
		t.Fatalf("resolved reference must publish: %v", err)
	}
	if served.GetVersion(resource.ClusterType) != "2" {
		t.Fatalf("resolved publication version %q, want 2", served.GetVersion(resource.ClusterType))
	}
}
