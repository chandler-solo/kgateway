package gcpprobe

import (
	"context"
	"errors"
	"testing"
	"time"

	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	rsrc "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
)

// RF-007: SetSnapshot is not transactional. Installation precedes fan-out;
// cancellation can leave CDS answered and EDS pending with the NEW cache.
func TestInstallationSurvivesPartialResponseFailure(t *testing.T) {
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	cds := make(chan cache.Response)
	eds := make(chan cache.Response)
	for typ, ch := range map[string]chan cache.Response{rsrc.ClusterType: cds, rsrc.EndpointType: eds} {
		cancel, err := c.CreateWatch(&discovery.DiscoveryRequest{TypeUrl: typ}, stream.NewSotwSubscription(nil, true), ch)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(cancel)
	}
	snap, err := cache.NewSnapshot("v2", map[rsrc.Type][]types.Resource{rsrc.ClusterType: {}, rsrc.EndpointType: {cla("a", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- c.SetSnapshot(ctx, kgwNode, snap) }()
	expectResponse(t, cds, "CDS is ordered before EDS")
	// No EDS receiver: cancellation is the only ready response-select branch.
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation after partial response, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SetSnapshot did not observe cancellation")
	}
	got, err := c.GetSnapshot(kgwNode)
	if err != nil || got != snap {
		t.Fatalf("cache installation rolled back: snapshot=%v error=%v", got, err)
	}
	if n := c.GetStatusInfo(kgwNode).GetNumWatches(); n != 1 {
		t.Fatalf("want unserved EDS watch retained, got %d", n)
	}
	expectSilence(t, eds, "EDS was not delivered")
}

type observedSnapshot struct {
	cache.ResourceSnapshot
	entered chan struct{}
}

func (s observedSnapshot) GetResourcesAndTTL(typ string) map[string]types.ResourceWithTTL {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	return s.ResourceSnapshot.GetResourcesAndTTL(typ)
}

// RF-011: an immediate send uses Background while holding the cache-wide lock.
// The stock server's fresh capacity-one channel avoids this supplied-full-channel
// API hazard; wrappers need their own capacity proof. Drain and join every
// goroutine so characterization does not leak a permanently locked cache.
func TestFullImmediateChannelBlocksOtherNode(t *testing.T) {
	c := cache.NewSnapshotCache(true, kgwHash{}, nil)
	entered := make(chan struct{}, 1)
	snap := observedSnapshot{kgwEDS(t, "v1", cla("a", 1)), entered}
	if err := c.SetSnapshot(context.Background(), kgwNode, snap); err != nil {
		t.Fatal(err)
	}
	full := make(chan cache.Response, 1)
	full <- nil
	done := make(chan error, 1)
	go func() {
		_, err := c.CreateWatch(&discovery.DiscoveryRequest{TypeUrl: rsrc.EndpointType, ResourceNames: []string{"a"}}, stream.NewSotwSubscription([]string{"a"}, false), full)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("CreateWatch never entered snapshot read")
	}
	other := make(chan error, 1)
	otherSnap := kgwEDS(t, "v9", cla("z", 1))
	go func() { other <- c.SetSnapshot(context.Background(), "another-node", otherSnap) }()
	// Bounded absence observation after the immediate path entered under lock.
	select {
	case err := <-other:
		t.Errorf("unrelated node escaped held cache lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	<-full
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("CreateWatch did not recover after drain")
	}
	expectResponse(t, full, "immediate response after drain")
	select {
	case err := <-other:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("other node did not recover after drain")
	}
}
