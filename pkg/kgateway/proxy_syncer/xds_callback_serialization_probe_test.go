package proxy_syncer

// Probes for per-stream xDS callback serialization (assumption GCP-A4,
// devel/formal/lean/ASSUMPTIONS.md; model XdsSpec/ClientIdentity.lean).
//
// The per-request client-identity re-derivation (PR #14244,
// pkg/krtcollections/uniqueclients.go) reads the per-stream ConnectedClient
// entry under RLock, derives the identity WITHOUT the lock, and mutates the
// shared maps under Lock. That check-then-act is sound only if
// go-control-plane never runs two callbacks for the SAME stream
// concurrently; and its drift close (OnStreamRequest returning an error)
// releases the old identity's refcount only because OnStreamClosed is still
// called after the error. Both contracts hold in go-control-plane v0.14.0 by
// construction — the sotw process loop handles one request at a time in a
// single goroutine and calls OnStreamClosed from a deferred shutdown — but
// neither is documented as an API guarantee beyond a comment, so these
// probes pin them against the real server: an upgrade that parallelizes
// per-stream callback dispatch or drops the error-path close notification
// would turn the identity bookkeeping into a data race (double-add of one
// stream, leaked refcounts) and must be caught here, not in production.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoydiscoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	xdsserverv3 "github.com/envoyproxy/go-control-plane/pkg/server/v3"
)

// callbackSerializationDetector fails the probe if two callbacks for the same
// stream ever overlap. Each callback holds a per-stream "in flight" slot for a
// couple of milliseconds; concurrent dispatch of the buffered back-to-back
// requests would collide on the slot with near certainty.
type callbackSerializationDetector struct {
	inFlight     atomic.Int32
	overlaps     atomic.Int32
	requests     atomic.Int32
	closes       atomic.Int32
	closedLast   atomic.Bool
	requestErr   error
	errOnRequest int32
}

func (d *callbackSerializationDetector) enter() {
	if !d.inFlight.CompareAndSwap(0, 1) {
		d.overlaps.Add(1)
	}
	time.Sleep(2 * time.Millisecond)
}

func (d *callbackSerializationDetector) exit() {
	d.inFlight.Store(0)
}

func (d *callbackSerializationDetector) callbacks() xdsserverv3.CallbackFuncs {
	return xdsserverv3.CallbackFuncs{
		StreamRequestFunc: func(_ int64, _ *envoydiscoveryv3.DiscoveryRequest) error {
			d.enter()
			defer d.exit()
			n := d.requests.Add(1)
			d.closedLast.Store(false)
			if d.requestErr != nil && n == d.errOnRequest {
				return d.requestErr
			}
			return nil
		},
		StreamClosedFunc: func(_ int64, _ *envoycorev3.Node) {
			d.enter()
			defer d.exit()
			d.closes.Add(1)
			d.closedLast.Store(true)
		},
	}
}

func serializationProbeRequest(typeURL string) *envoydiscoveryv3.DiscoveryRequest {
	return &envoydiscoveryv3.DiscoveryRequest{
		Node:    &envoycorev3.Node{Id: probeNodeID},
		TypeUrl: typeURL,
	}
}

// TestADSCallbacksAreSerializedPerStream pins that OnStreamRequest and
// OnStreamClosed for one stream never run concurrently, even when requests
// arrive back-to-back, and that OnStreamClosed is dispatched exactly once,
// after the last request.
func TestADSCallbacksAreSerializedPerStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	detector := &callbackSerializationDetector{}
	stream := &adsProbeStream{
		ctx:  ctx,
		recv: make(chan *envoydiscoveryv3.DiscoveryRequest, 16),
		sent: make(chan *envoydiscoveryv3.DiscoveryResponse, 16),
	}
	srv := xdsserverv3.NewServer(ctx, envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil), detector.callbacks())
	streamDone := make(chan error, 1)
	go func() { streamDone <- srv.StreamAggregatedResources(stream) }()

	// Queue requests back-to-back so a parallel dispatcher would have several
	// in hand at once; alternate types so each is a fresh subscription.
	types := []string{
		envoyresourcev3.ClusterType, envoyresourcev3.RouteType,
		envoyresourcev3.ListenerType, envoyresourcev3.EndpointType,
		envoyresourcev3.ClusterType, envoyresourcev3.RouteType,
	}
	for _, typeURL := range types {
		stream.recv <- serializationProbeRequest(typeURL)
	}

	deadline := time.Now().Add(5 * time.Second)
	want := int32(6) // len(types); constant to avoid a lossy int conversion
	for detector.requests.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("server processed %d requests; want %d", detector.requests.Load(), len(types))
		}
		time.Sleep(time.Millisecond)
	}

	// End the stream; shutdown must dispatch OnStreamClosed (once), after the
	// requests, and without overlapping them.
	close(stream.recv)
	<-streamDone

	if got := detector.overlaps.Load(); got != 0 {
		t.Fatalf("observed %d concurrent callback dispatches for one stream; the identity bookkeeping in uniqueclients.go requires strict per-stream serialization", got)
	}
	if got := detector.closes.Load(); got != 1 {
		t.Fatalf("OnStreamClosed dispatched %d times; want exactly 1", got)
	}
	if !detector.closedLast.Load() {
		t.Fatal("OnStreamClosed dispatched before the final OnStreamRequest; del would race add for the same stream")
	}
}

// TestADSStreamClosedFiresAfterRequestError pins the drift-close cleanup
// contract: when OnStreamRequest returns an error (how uniqueclients.go
// closes a stream whose re-derived identity drifted), the stream terminates
// with that error AND OnStreamClosed still fires — that call is what releases
// the old identity's refcount; without it every drift heal would leak a stale
// UniqlyConnectedClient forever.
func TestADSStreamClosedFiresAfterRequestError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	driftErr := errors.New("xds client identity changed")
	detector := &callbackSerializationDetector{requestErr: driftErr, errOnRequest: 3}
	stream := &adsProbeStream{
		ctx:  ctx,
		recv: make(chan *envoydiscoveryv3.DiscoveryRequest, 16),
		sent: make(chan *envoydiscoveryv3.DiscoveryResponse, 16),
	}
	srv := xdsserverv3.NewServer(ctx, envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil), detector.callbacks())
	streamDone := make(chan error, 1)
	go func() { streamDone <- srv.StreamAggregatedResources(stream) }()

	for _, typeURL := range []string{envoyresourcev3.ClusterType, envoyresourcev3.RouteType, envoyresourcev3.ListenerType} {
		stream.recv <- serializationProbeRequest(typeURL)
	}

	select {
	case err := <-streamDone:
		if !errors.Is(err, driftErr) {
			t.Fatalf("stream ended with %v; want the OnStreamRequest error to terminate it", err)
		}
	case <-ctx.Done():
		t.Fatal("stream did not terminate after OnStreamRequest returned an error")
	}

	if got := detector.requests.Load(); got != 3 {
		t.Fatalf("server processed %d requests; want 3 (the third returns the error)", got)
	}
	if got := detector.closes.Load(); got != 1 {
		t.Fatalf("OnStreamClosed dispatched %d times after the request error; want exactly 1 — this call releases the drifted identity's refcount", got)
	}
	if got := detector.overlaps.Load(); got != 0 {
		t.Fatalf("observed %d concurrent callback dispatches on the error path", got)
	}
}
