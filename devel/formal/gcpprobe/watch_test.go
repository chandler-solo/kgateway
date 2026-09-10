// Package gcpprobe characterizes the real cache selected by the root go.mod.
// RF-007: passing lost-watch tests reproduce v0.14.0 defects, not fixes.
package gcpprobe

import (
	"context"
	"testing"
	"time"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
)

const kgwNode = "kgw-probe-node"

type kgwHash struct{}

func (kgwHash) ID(*core.Node) string { return kgwNode }

func kgwEDS(t *testing.T, version string, clas ...*endpoint.ClusterLoadAssignment) *cache.Snapshot {
	t.Helper()
	res := make([]types.Resource, 0, len(clas))
	for _, c := range clas {
		res = append(res, c)
	}
	snap, err := cache.NewSnapshot(version, map[rsrc.Type][]types.Resource{rsrc.EndpointType: res})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snap
}

func cla(name string, hosts int) *endpoint.ClusterLoadAssignment {
	c := &endpoint.ClusterLoadAssignment{ClusterName: name}
	for i := 0; i < hosts; i++ {
		c.Endpoints = append(c.Endpoints, &endpoint.LocalityLbEndpoints{})
	}
	return c
}

func expectResponse(t *testing.T, ch chan cache.Response, what string) cache.Response {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(time.Second):
		t.Fatalf("expected a response: %s", what)
	}
	return nil
}

func expectSilence(t *testing.T, ch chan cache.Response, what string) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("expected no response (%s), got version %v", what, r.GetRequest().GetTypeUrl())
	default: // Cache calls above are synchronous; no response remains queued.
	}
}

// GCP-A5 (SetSnapshot path): a parked named EDS watch is deleted when a new
// snapshot cannot be sent to it (superset declined), so the proxy's pending
// request is stranded; a later aligned snapshot is not delivered; only a
// new request recovers.
func TestParkedNamedWatchIsDiscardedOnDeclinedResponse(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true /* ads */, kgwHash{}, nil)
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1))); err != nil {
		t.Fatal(err)
	}

	req := &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}}
	sub := stream.NewSotwSubscription([]string{"a"}, false)

	ch1 := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(req, sub, ch1); err != nil {
		t.Fatal(err)
	}
	first := expectResponse(t, ch1, "fresh named watch")
	ackResponse(first, &sub, req) // Envoy ACKs v1, has {a}

	ch2 := make(chan cache.Response, 1)
	cancel2, err := c.CreateWatch(req, sub, ch2)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()
	if n := c.GetStatusInfo(kgwNode).GetNumWatches(); n != 1 {
		t.Fatalf("expected 1 parked watch, got %d", n)
	}

	// v2 adds b (a cluster Envoy has not applied/accepted): ADS declines.
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v2", cla("a", 1), cla("b", 1))); err != nil {
		t.Fatal(err)
	}
	expectSilence(t, ch2, "superset declined {a} watch against {a,b}")

	// GCP-A5: correct behavior is 1 (retained). v0.14.0 deletes it.
	if n := c.GetStatusInfo(kgwNode).GetNumWatches(); n != 0 {
		t.Fatalf("declined watch RETAINED (%d open): the discard defect is fixed — invert this probe and update the ledger", n)
	}

	// A converging snapshot (b gone, a changed) has nothing to answer.
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v3", cla("a", 2))); err != nil {
		t.Fatal(err)
	}
	expectSilence(t, ch2, "converging snapshot against a discarded watch (GCP-A5: correct behavior is delivery)")

	// Only a NEW request (Envoy re-requesting after a CDS apply) recovers.
	ch3 := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(req, sub, ch3); err != nil {
		t.Fatal(err)
	}
	expectResponse(t, ch3, "new request against the aligned v3 snapshot")
}

// GCP-A5 (CreateWatch path): a NEW named request the current snapshot cannot
// satisfy is neither answered nor registered as a watch, so a later aligned
// snapshot is not delivered either.
func TestDeclinedNewRequestRegistersNoWatch(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v2", cla("a", 1), cla("b", 1))); err != nil {
		t.Fatal(err)
	}

	req := &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}, VersionInfo: "v1"}
	sub := stream.NewSotwSubscription([]string{"a"}, false)
	ch := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(req, sub, ch); err != nil {
		t.Fatal(err)
	}
	expectSilence(t, ch, "version differs but superset declines")
	// GCP-A5: correct behavior is a retained watch (1). v0.14.0 registers none.
	if n := c.GetStatusInfo(kgwNode).GetNumWatches(); n != 0 {
		t.Fatalf("declined request left a watch registered (%d): the defect is fixed — invert this probe and update the ledger", n)
	}
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v3", cla("a", 2))); err != nil {
		t.Fatal(err)
	}
	expectSilence(t, ch, "aligned v3 snapshot against an unregistered request (GCP-A5: correct behavior is delivery)")
}

// GCP-A1 correction: v0.14.0 answers at an UNCHANGED version when the
// subscription names a resource present in the snapshot but not yet returned
// to the client (newly subscribed). "answers iff version differs" is
// incomplete; the model's edsWatchNoChange treats equal-version as never
// answered.
func TestEqualVersionRespondsForNewlySubscribedResource(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1), cla("b", 1))); err != nil {
		t.Fatal(err)
	}

	req := &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a", "b"}}
	sub := stream.NewSotwSubscription([]string{"a", "b"}, false)
	ch1 := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(req, sub, ch1); err != nil {
		t.Fatal(err)
	}
	first := expectResponse(t, ch1, "initial {a,b}")
	ackResponse(first, &sub, req) // acked v1 with {a,b} returned

	// Envoy drops b and later re-subscribes to it (e.g. a cluster removed and
	// re-added on its side) — but the snapshot never changed: version still v1.
	sub.SetResourceSubscription([]string{"a"})
	sub.SetReturnedResources(map[string]string{"a": "v1"}) // b no longer held by the client
	sub.SetResourceSubscription([]string{"a", "b"})
	ch2 := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(req, sub, ch2); err != nil {
		t.Fatal(err)
	}
	r := expectResponse(t, ch2, "equal version, newly subscribed b present in snapshot")
	if _, ok := r.GetReturnedResources()["b"]; !ok {
		t.Fatalf("expected the newly subscribed resource b in the equal-version response, got %v", r.GetReturnedResources())
	}
}

// Wildcard (LDS/CDS-shaped) watches are never subject to the superset check,
// so the discard defect is confined to named types (EDS/RDS/SDS).
func TestWildcardWatchIsNeverDeclined(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1))); err != nil {
		t.Fatal(err)
	}
	req := &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType} // no names: wildcard
	sub := stream.NewSotwSubscription(nil, true)
	ch1 := make(chan cache.Response, 1)
	if _, err := c.CreateWatch(req, sub, ch1); err != nil {
		t.Fatal(err)
	}
	ackResponse(expectResponse(t, ch1, "wildcard initial"), &sub, req)
	ch2 := make(chan cache.Response, 1)
	cancel, err := c.CreateWatch(req, sub, ch2)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v2", cla("a", 1), cla("b", 1))); err != nil {
		t.Fatal(err)
	}
	expectResponse(t, ch2, "wildcard delivered despite the extra resource")
}

func ackResponse(resp cache.Response, sub *stream.Subscription, req *cache.Request) {
	sub.SetReturnedResources(resp.GetReturnedResources())
	version, err := resp.GetVersion()
	if err != nil {
		panic(err) // All fixtures use materialized snapshots, not passthrough responses.
	}
	req.VersionInfo = version
}
