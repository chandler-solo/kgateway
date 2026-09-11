package gcpprobe

import (
	"context"
	"testing"

	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
)

// Corpus kgateway #14471 (RF-007, RF-026): a newer control plane added a
// per-gateway local-cluster ClusterLoadAssignment to every EDS snapshot. Older
// proxies had no such cluster in their bootstrap and never requested it, so
// the ADS superset check withheld their entire EDS response and endpoint
// updates stopped flowing during the rolling upgrade.
//
// This probe pins the mechanism through the real v0.14.0 cache: a named EDS
// request for {a} against a snapshot carrying {a, local} is declined, while
// the same request against a snapshot filtered to {a} is answered. kgateway's
// repair filters CLAs to the clusters a proxy can request
// (bootstrapEndpointNames in perclient.go); this probe is the dependency-side
// half of that regression, independent of kgateway code.
func TestUnrequestedBootstrapClusterCLAWithholdsEDSForOlderProxy(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	// The newer control plane's snapshot: the proxy's cluster plus a CLA for a
	// bootstrap-only local cluster this proxy generation does not have.
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1), cla("local", 1))); err != nil {
		t.Fatal(err)
	}
	sub := stream.NewSotwSubscription([]string{"a"}, false)
	ch := make(chan cache.Response, 1)
	cancel, err := c.CreateWatch(&discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}}, sub, ch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	expectSilence(t, ch, "superset check declines {a} against {a, local}: the older proxy receives no endpoints at all")

	// A later revision that still carries the unrequested CLA is declined too;
	// the blackout persists across updates, not just at connect.
	if err = c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v2", cla("a", 2), cla("local", 1))); err != nil {
		t.Fatal(err)
	}
	expectSilence(t, ch, "a changed revision with the unrequested CLA is still declined")

	// The repaired shape: the snapshot carries only CLAs the proxy can request.
	if err = c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v3", cla("a", 3))); err != nil {
		t.Fatal(err)
	}
	ch2 := make(chan cache.Response, 1)
	cancel2, err := c.CreateWatch(&discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}}, sub, ch2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel2)
	got := expectResponse(t, ch2, "filtered snapshot answers the older proxy")
	if _, ok := got.GetReturnedResources()["a"]; !ok || len(got.GetReturnedResources()) != 1 {
		t.Fatalf("unexpected returned resources %v", got.GetReturnedResources())
	}
}
