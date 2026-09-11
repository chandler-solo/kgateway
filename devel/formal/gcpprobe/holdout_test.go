package gcpprobe

import (
	"context"
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
	server "github.com/envoyproxy/go-control-plane/pkg/server/v3"
)

// Held-out corpus mechanism go-control-plane #431: a client that unsubscribes
// from a resource and later resubscribes at its unchanged accepted version
// must receive the resource again, because it dropped it on unsubscribe.
//
// Observation on the pin through the real cache and server: the resubscribe
// request is answered immediately with the unchanged version. The subscription
// tracking in v0.14.0 drops the resource from the returned set on unsubscribe,
// so CreateWatch treats the resubscription as a newly requested unreturned
// name at equal version. The issue was closed a month before the v0.14.0
// release; this pins that the repair is present in the deployed profile.
func TestResubscribeAtEqualVersionIsAnsweredOnPin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1))); err != nil {
		t.Fatal(err)
	}
	srv := server.NewServer(ctx, c, server.CallbackFuncs{})
	s := &scriptedStream{ctx: ctx, recv: make(chan *discovery.DiscoveryRequest), sent: make(chan *discovery.DiscoveryResponse)}
	done := make(chan error, 1)
	go func() { done <- srv.StreamAggregatedResources(s) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("server did not shut down")
		}
	})

	first := s.exchange(t, &discovery.DiscoveryRequest{Node: &envoycorev3.Node{Id: kgwNode}, TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}})
	// ACK, then unsubscribe from a at the accepted version.
	for _, names := range [][]string{{"a"}, {}} {
		select {
		case s.recv <- &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: names, VersionInfo: first.GetVersionInfo(), ResponseNonce: first.GetNonce()}:
		case <-time.After(time.Second):
			t.Fatal("request not consumed")
		}
		waitForWatches(t, c, 1)
	}
	// Resubscribe to a at the same version: the protocol requires a resend.
	got := s.exchange(t, &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}, VersionInfo: first.GetVersionInfo(), ResponseNonce: first.GetNonce()})
	if got.GetVersionInfo() != first.GetVersionInfo() || len(got.GetResources()) != 1 {
		t.Fatalf("resubscribe at equal version was not re-sent the resource: %v; the #431 repair is absent from this profile, reassess the corpus entry", got)
	}
	if got.GetNonce() == first.GetNonce() {
		t.Fatal("resend reused the previous nonce")
	}
}

// Held-out corpus mechanism go-control-plane #505: ClearSnapshot removes the
// node's status while parked watches remain registered in it.
//
// Observation on the pin: after ClearSnapshot the parked watch is never
// answered; a later SetSnapshot creates fresh status with no watches, so the
// old waiter is orphaned until the client sends another request. GetStatusInfo
// reports no node between the clear and the next installation.
func TestClearSnapshotOrphansParkedWatchOnPin(t *testing.T) {
	ctx := context.Background()
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1))); err != nil {
		t.Fatal(err)
	}
	sub := stream.NewSotwSubscription([]string{"a"}, false)
	sub.SetReturnedResources(map[string]string{"a": "v1"})
	ch := make(chan cache.Response, 1)
	cancel, err := c.CreateWatch(&discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}, VersionInfo: "v1"}, sub, ch)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	expectSilence(t, ch, "equal-version request parks")
	if n := c.GetStatusInfo(kgwNode).GetNumWatches(); n != 1 {
		t.Fatalf("expected 1 parked watch, got %d", n)
	}

	c.ClearSnapshot(kgwNode)
	if info := c.GetStatusInfo(kgwNode); info != nil {
		t.Fatalf("status survived ClearSnapshot with %d watches", info.GetNumWatches())
	}
	expectSilence(t, ch, "ClearSnapshot does not answer or close the parked watch")

	// A new snapshot for the node has no memory of the parked watch.
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v2", cla("a", 2))); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-ch:
		t.Fatalf("orphaned watch was answered (%v); #505 is repaired in this profile, invert this probe and update the corpus", got.GetReturnedResources())
	case <-time.After(300 * time.Millisecond):
	}
	// SetSnapshot creates no status of its own; only a request does.
	if info := c.GetStatusInfo(kgwNode); info != nil && info.GetNumWatches() != 0 {
		t.Fatalf("new status carries %d watches; expected the old waiter to be orphaned", info.GetNumWatches())
	}
	// The server-side recovery path is a new request from the client.
	ch2 := make(chan cache.Response, 1)
	cancel2, err := c.CreateWatch(&discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}, VersionInfo: "v1"}, sub, ch2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel2)
	expectResponse(t, ch2, "a fresh request sees v2")
}
