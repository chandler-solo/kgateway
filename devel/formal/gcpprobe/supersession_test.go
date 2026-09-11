package gcpprobe

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	sotw "github.com/envoyproxy/go-control-plane/pkg/server/sotw/v3"
	server "github.com/envoyproxy/go-control-plane/pkg/server/v3"
)

// RF-018: go-control-plane #1498 drops, at send time, a response whose
// resources are no longer in the current subscription. Its regression test
// injects that response from a mock cache on the second CreateWatch. This
// probe constructs the same shape with the real v0.14.0 SnapshotCache: a
// parked named watch is answered by a snapshot change exactly while the
// server is processing a subscription change for the same type, so the
// response is queued when the old watch is superseded.
//
// Observation on the pin, both ADS modes: the superseded response never
// reaches the wire. In ordered ADS the loop drains the shared channel and
// discards same-type responses before creating the new watch; in default
// mode the old per-watch channel leaves the select set. Because SetSnapshot
// fans out under the cache mutex that cancelWatch also takes, the cache
// cannot answer a watch after its cancel returns, so no later injection point
// exists for this cache. The #1498 schedule therefore needs a cache that
// responds after cancellation; it is unobserved with SnapshotCache, which is
// not a proof for wrappers or other cache implementations.
func TestQueuedSupersededResponseIsDroppedOnSubscriptionChange(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		name := "default"
		if ordered {
			name = "ordered"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			c := cache.NewSnapshotCache(true, kgwHash{}, nil)
			if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1))); err != nil {
				t.Fatal(err)
			}

			// The hook runs inside the server loop, after the request is read
			// and before the existing watch is closed. Publishing here answers
			// the parked old watch while its supersession is in progress.
			var watchesAfterPublish atomic.Int64
			watchesAfterPublish.Store(-1)
			callbacks := server.CallbackFuncs{StreamRequestFunc: func(_ int64, req *discovery.DiscoveryRequest) error {
				if !slices.Contains(req.GetResourceNames(), "c") {
					return nil
				}
				if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v2", cla("a", 2))); err != nil {
					return err
				}
				watchesAfterPublish.Store(int64(c.GetStatusInfo(kgwNode).GetNumWatches()))
				return nil
			}}
			srv := server.NewServer(ctx, c, callbacks)
			if ordered {
				srv = server.NewServer(ctx, c, callbacks, sotw.WithOrderedADS())
			}
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
			if first.GetVersionInfo() != "v1" {
				t.Fatalf("first response version %q", first.GetVersionInfo())
			}
			// ACK v1 with unchanged names: equal version parks a real watch.
			select {
			case s.recv <- &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}, VersionInfo: first.GetVersionInfo(), ResponseNonce: first.GetNonce()}:
			case <-time.After(time.Second):
				t.Fatal("ACK not consumed")
			}
			waitForWatches(t, c, 1)

			// Subscription change {a} -> {a,c}; the hook publishes v2 while the
			// parked {a} watch is still open.
			second := s.exchange(t, &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a", "c"}, VersionInfo: first.GetVersionInfo(), ResponseNonce: first.GetNonce()})
			if second.GetVersionInfo() != "v2" || len(second.GetResources()) != 1 {
				t.Fatalf("expected the new watch's v2 response with one resource, got %v", second)
			}
			// The cache answered and discarded the parked watch inside the hook:
			// a response for the superseded watch was queued.
			if got := watchesAfterPublish.Load(); got != 0 {
				t.Fatalf("expected the parked watch to be answered inside the hook (0 open), got %d", got)
			}
			// That queued response must not follow the new watch's response.
			select {
			case extra := <-s.sent:
				t.Fatalf("superseded watch response reached the wire: version %q nonce %q; reassess RF-018 and #1498 applicability", extra.GetVersionInfo(), extra.GetNonce())
			case <-time.After(300 * time.Millisecond):
			}
			if second.GetNonce() == first.GetNonce() {
				t.Fatal("new response reused the previous nonce")
			}
		})
	}
}
