package gcpprobe

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
	server "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	rpcstatus "google.golang.org/genproto/googleapis/rpc/status"

	"github.com/kgateway-dev/kgateway/v2/devel/formal/lifecycle"
)

func finishLifecycle(t *testing.T, recorder *lifecycle.Recorder) lifecycle.Stats {
	t.Helper()
	events := recorder.Finish()
	stats, err := lifecycle.Check(events)
	if err != nil {
		t.Fatal(err)
	}
	if out := os.Getenv("FORMAL_LIFECYCLE_OUT"); out != "" {
		if err := os.MkdirAll(out, 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(out, strings.ReplaceAll(t.Name(), "/", "_")+".jsonl")
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		writeErr := lifecycle.Write(f, events)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("trace write: %v; close: %v", writeErr, closeErr)
		}
	}
	return stats
}

func recordInstalled(t *testing.T, recorder *lifecycle.Recorder, c cache.SnapshotCache, typ string) {
	t.Helper()
	snapshot, err := c.GetSnapshot(kgwNode)
	if err != nil {
		t.Fatal(err)
	}
	recorder.Add(lifecycle.Event{Kind: "cache-install", Key: kgwNode, Type: typ, Version: snapshot.GetVersion(typ)})
}

// These traces link the repaired parked-watch and unrepaired entry paths to
// actual synchronous cache observations. A finite absence is not temporal proof.
func TestLifecycleWatchAdmissionProfiles(t *testing.T) {
	for _, parked := range []bool{false, true} {
		t.Run(fmt.Sprintf("parked-%t", parked), func(t *testing.T) {
			r := lifecycle.New(t.Name(), "cache-api")
			c := cache.NewSnapshotCache(true, kgwHash{}, nil)
			publish := func(version string, extra bool) {
				snap := kgwEDS(t, version, cla("a", 1))
				if extra {
					snap = kgwEDS(t, version, cla("a", 1), cla("b", 1))
				}
				if err := c.SetSnapshot(t.Context(), kgwNode, snap); err != nil {
					t.Fatal(err)
				}
				recordInstalled(t, r, c, rsrc.EndpointType)
			}
			if parked {
				publish("v1", false)
			} else {
				publish("v2", true)
			}
			r.Add(lifecycle.Event{Kind: "open", Key: kgwNode, Stream: 1})
			req := &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}, VersionInfo: "v1"}
			sub := stream.NewSotwSubscription(req.ResourceNames, false)
			// Returned-resource history is supplied explicitly, just as in the
			// existing ACK-based parked-watch probe. This is not an actual ACK.
			sub.SetReturnedResources(map[string]string{"a": ""})
			base := lifecycle.Event{Key: kgwNode, Stream: 1, Type: rsrc.EndpointType, Request: "request-1"}
			e := base
			e.Kind, e.Version, e.Names = "request", req.VersionInfo, req.ResourceNames
			r.Add(e)
			ch := make(chan cache.Response, 1)
			cancel, err := c.CreateWatch(req, sub, ch)
			if err != nil {
				t.Fatal(err)
			}
			if cancel != nil {
				defer cancel()
			}
			if parked {
				publish("v2", true)
			}
			expectSilence(t, ch, "ineligible snapshot")
			n := c.GetStatusInfo(kgwNode).GetNumWatches()
			if (parked && n != 1) || (!parked && n != 0) {
				t.Fatalf("profile changed: parked=%t watches=%d", parked, n)
			}
			e = base
			e.Kind, e.Status = "watch-status", "absent"
			if parked {
				e.Status = "parked"
			}
			r.Add(e)
			publish("v3", false)
			if parked {
				response := expectResponse(t, ch, "correction without a new request")
				e = base
				e.Kind, e.Version = "cache-response", response.GetResponseVersion()
				r.Add(e)
			} else {
				expectSilence(t, ch, "lost request remains absent after correction")
			}
			r.Add(lifecycle.Event{Kind: "close", Key: kgwNode, Stream: 1})
			stats := finishLifecycle(t, r)
			if stats.Acknowledged != 0 || stats.Counts["transport-observed"] != 0 {
				t.Fatal("cache-only probe claimed acknowledgment or transport")
			}
		})
	}
}

// Correlate callbacks, responses received by a mock transport, NACK retries and
// ACK by stream/type/nonce. No Envoy or applied-configuration event is inferred.
func TestLifecycleServerAcknowledgmentCorrelation(t *testing.T) {
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	r := lifecycle.New(t.Name(), "server-mock")
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	if err := c.SetSnapshot(ctx, kgwNode, kgwEDS(t, "v1", cla("a", 1))); err != nil {
		t.Fatal(err)
	}
	recordInstalled(t, r, c, rsrc.EndpointType)
	var requests sync.Map
	var counter int // serialized by the single server stream
	ackSeen := make(chan struct{}, 1)
	callbacks := server.CallbackFuncs{
		StreamOpenFunc: func(_ context.Context, id int64, _ string) error {
			r.Add(lifecycle.Event{Kind: "open", Key: kgwNode, Stream: id})
			return nil
		},
		StreamClosedFunc: func(id int64, _ *envoycorev3.Node) {
			r.Add(lifecycle.Event{Kind: "close", Key: kgwNode, Stream: id})
		},
		StreamRequestFunc: func(id int64, req *discovery.DiscoveryRequest) error {
			counter++
			e := lifecycle.Event{
				Kind: "request", Key: kgwNode, Stream: id, Type: req.TypeUrl,
				Version: req.VersionInfo, Nonce: req.ResponseNonce, Names: req.ResourceNames, Request: fmt.Sprintf("r%d", counter),
			}
			requests.Store(req, e)
			r.Add(e)
			if req.ResponseNonce != "" {
				e.Kind = "ack"
				if req.ErrorDetail != nil {
					e.Kind = "nack"
				}
				r.Add(e)
				if e.Kind == "ack" {
					ackSeen <- struct{}{}
				}
			}
			return nil
		},
		StreamResponseFunc: func(_ context.Context, _ int64, req *discovery.DiscoveryRequest, response *discovery.DiscoveryResponse) {
			value, ok := requests.Load(req)
			if !ok {
				t.Error("response callback has no observed request")
				return
			}
			e := value.(lifecycle.Event)
			e.Kind, e.Version, e.Nonce = "response-attempt", response.VersionInfo, response.Nonce
			r.Add(e)
		},
	}
	s := &scriptedStream{ctx: ctx, recv: make(chan *discovery.DiscoveryRequest), sent: make(chan *discovery.DiscoveryResponse)}
	done := make(chan error, 1)
	go func() { done <- server.NewServer(ctx, c, callbacks).StreamAggregatedResources(s) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("server did not shut down")
		}
	})
	exchange := func(req *discovery.DiscoveryRequest) *discovery.DiscoveryResponse {
		response := s.exchange(t, req)
		value, ok := requests.Load(req)
		if !ok {
			t.Fatal("mock transport has no observed request")
		}
		e := value.(lifecycle.Event)
		e.Kind, e.Version, e.Nonce = "transport-observed", response.VersionInfo, response.Nonce
		r.Add(e)
		return response
	}
	response := exchange(&discovery.DiscoveryRequest{Node: &envoycorev3.Node{Id: kgwNode}, TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}})
	response = exchange(&discovery.DiscoveryRequest{
		TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"},
		ResponseNonce: response.Nonce, ErrorDetail: &rpcstatus.Status{Code: 3, Message: "controlled rejection"},
	})
	select {
	case s.recv <- &discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}, VersionInfo: response.VersionInfo, ResponseNonce: response.Nonce}:
	case <-time.After(time.Second):
		t.Fatal("ACK not consumed")
	}
	select {
	case <-ackSeen:
	case <-time.After(time.Second):
		t.Fatal("ACK not observed")
	}
	stop()
	select {
	case err := <-done:
		done <- err // leave the joined outcome available to cleanup
	case <-time.After(time.Second):
		t.Fatal("server did not shut down")
	}
	stats := finishLifecycle(t, r)
	if stats.Acknowledged != 1 || stats.Counts["nack"] != 1 || stats.Counts["transport-observed"] != 2 {
		t.Fatalf("missing required observations: %+v", stats)
	}
}
