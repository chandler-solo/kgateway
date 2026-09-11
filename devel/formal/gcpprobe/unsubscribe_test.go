package gcpprobe

import (
	"context"
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	sotw "github.com/envoyproxy/go-control-plane/pkg/server/sotw/v3"
	stream "github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
	server "github.com/envoyproxy/go-control-plane/pkg/server/v3"
)

// RF-018: empty names after an explicit subscription means unsubscribe-all,
// not legacy wildcard. v0.14.0 respond/createResponse inspect the raw empty
// name list instead of that subscription state and send unrequested resources.
func TestUnsubscribeAllStillReceivesResourcesFromCache(t *testing.T) {
	for _, entry := range []string{"immediate", "parked"} {
		t.Run(entry, func(t *testing.T) {
			c := cache.NewSnapshotCache(true, kgwHash{}, nil)
			initial := "v1"
			if entry == "immediate" {
				initial = "v2"
			}
			if err := c.SetSnapshot(context.Background(), kgwNode, kgwEDS(t, initial, cla("a", 1))); err != nil {
				t.Fatal(err)
			}
			sub := stream.NewSotwSubscription([]string{"a"}, true)
			sub.SetReturnedResources(map[string]string{"a": "v1"})
			sub.SetResourceSubscription(nil)
			if sub.IsWildcard() || len(sub.SubscribedResources()) != 0 {
				t.Fatal("fixture did not unsubscribe all")
			}
			ch := make(chan cache.Response, 1)
			cancel, err := c.CreateWatch(&discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, VersionInfo: "v1"}, sub, ch)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cancel)
			if entry == "parked" {
				expectSilence(t, ch, "equal-version unsubscribe parks")
				if err = c.SetSnapshot(context.Background(), kgwNode, kgwEDS(t, "v2", cla("a", 2))); err != nil {
					t.Fatal(err)
				}
			}
			got := expectResponse(t, ch, "v0.14.0 sends to unsubscribe-all")
			if _, ok := got.GetReturnedResources()["a"]; !ok {
				t.Fatal("unrequested-resource behavior changed; assess repair and update RF-018")
			}
		})
	}
}

// Exercise the real server as well: no injected stale Response or mock cache.
func TestUnsubscribeAllResourceLeaksOnWire(t *testing.T) {
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
			s := &scriptedStream{ctx: ctx, recv: make(chan *discovery.DiscoveryRequest), sent: make(chan *discovery.DiscoveryResponse)}
			srv := server.NewServer(ctx, c, server.CallbackFuncs{})
			if ordered {
				srv = server.NewServer(ctx, c, server.CallbackFuncs{}, sotw.WithOrderedADS())
			}
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
			select {
			case s.recv <- &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, VersionInfo: first.VersionInfo, ResponseNonce: first.Nonce}:
			case <-time.After(time.Second):
				t.Fatal("unsubscribe request not consumed")
			}
			deadline := time.Now().Add(time.Second)
			for c.GetStatusInfo(kgwNode).GetNumWatches() != 1 {
				if time.Now().After(deadline) {
					t.Fatal("unsubscribe watch did not park")
				}
				time.Sleep(time.Millisecond)
			}
			if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v2", cla("a", 2))); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-s.sent:
				if got.TypeUrl != rsrc.EndpointType || len(got.Resources) != 1 {
					t.Fatalf("unexpected response: %v", got)
				}
			case <-time.After(time.Second):
				t.Fatal("unrequested response no longer leaks; assess upstream repair and RF-018")
			}
		})
	}
}
