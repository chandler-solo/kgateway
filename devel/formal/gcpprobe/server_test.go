package gcpprobe

import (
	"context"
	"testing"
	"time"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	server "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
)

type scriptedStream struct {
	grpc.ServerStream
	ctx  context.Context
	recv chan *discovery.DiscoveryRequest
	sent chan *discovery.DiscoveryResponse
}

func (s *scriptedStream) Context() context.Context { return s.ctx }
func (s *scriptedStream) Recv() (*discovery.DiscoveryRequest, error) {
	select {
	case r := <-s.recv:
		return r, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}
func (s *scriptedStream) Send(r *discovery.DiscoveryResponse) error {
	select {
	case s.sent <- r:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}
func (s *scriptedStream) exchange(t *testing.T, r *discovery.DiscoveryRequest) *discovery.DiscoveryResponse {
	t.Helper()
	select {
	case s.recv <- r:
	case <-time.After(time.Second):
		t.Fatal("request not consumed")
	}
	select {
	case response := <-s.sent:
		return response
	case <-time.After(time.Second):
		t.Fatal("no response")
	}
	return nil
}

// RF-012: v0.14.0 retries the unchanged rejected version on every matching
// NACK. This finite schedule checks recurrence and correction, not Envoy CPU
// load or a real-time throughput claim. A rejection policy remains open.
func TestRepeatedNackResendsSameVersionAndCorrectionRecovers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	publish := func(version string) {
		t.Helper()
		snap, err := cache.NewSnapshot(version, map[rsrc.Type][]types.Resource{rsrc.ClusterType: {&cluster.Cluster{Name: "a"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = c.SetSnapshot(ctx, kgwNode, snap); err != nil {
			t.Fatal(err)
		}
	}
	publish("rejected")
	s := &scriptedStream{ctx: ctx, recv: make(chan *discovery.DiscoveryRequest), sent: make(chan *discovery.DiscoveryResponse)}
	done := make(chan error, 1)
	go func() { done <- server.NewServer(ctx, c, server.CallbackFuncs{}).StreamAggregatedResources(s) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("stream did not shut down")
		}
	})
	resp := s.exchange(t, &discovery.DiscoveryRequest{Node: &core.Node{Id: kgwNode}, TypeUrl: rsrc.ClusterType})
	for i := 0; i < 32; i++ {
		previous := resp.GetNonce()
		resp = s.exchange(t, &discovery.DiscoveryRequest{TypeUrl: rsrc.ClusterType, ResponseNonce: previous, ErrorDetail: &rpcstatus.Status{Code: 3, Message: "scripted rejection"}})
		if resp.GetVersionInfo() != "rejected" || resp.GetNonce() == previous {
			t.Fatalf("unexpected retry at %d: %v", i, resp)
		}
	}
	publish("corrected")
	resp = s.exchange(t, &discovery.DiscoveryRequest{TypeUrl: rsrc.ClusterType, ResponseNonce: resp.GetNonce(), ErrorDetail: &rpcstatus.Status{Code: 3, Message: "last rejection"}})
	if resp.GetVersionInfo() != "corrected" {
		t.Fatalf("new snapshot not delivered: %v", resp)
	}
}
