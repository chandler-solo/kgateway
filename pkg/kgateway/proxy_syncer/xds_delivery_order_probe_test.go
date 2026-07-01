package proxy_syncer

// Probes for ADS wire-delivery ordering (assumption GCP-A3,
// devel/formal/lean/ASSUMPTIONS.md; model XdsSpec/OrderedADS.lean).
//
// The per-client publication gate guarantees every published snapshot is
// dependency-coherent, but coherence is a property of the snapshot, not of
// the wire: go-control-plane delivers each resource type as its own
// DiscoveryResponse, and the ORDER of those responses decides whether Envoy
// ever applies a route before the cluster it references (transient 503 NC).
// These probes run the real server.StreamAggregatedResources against a mock
// ADS stream and measure the actual response order:
//
//   1. Additions with both type watches open are delivered CDS before RDS —
//      by the CACHE's own write ordering (SetSnapshot sorts response writes
//      by type; pkg/cache/v3/order.go), with or without WithOrderedADS. The
//      default server's reflect.Select drain only randomizes when several
//      watch channels are simultaneously ready (busy streams);
//      WithOrderedADS (PR #14341) closes that residual window.
//   2. ACK SKEW defeats the ordering guarantee in BOTH modes: after a CDS
//      response is sent, the CDS watch is closed until Envoy ACKs it. If the
//      next snapshot (new cluster + route retarget) lands in that window,
//      only the RDS watch is open, so the route referencing the new cluster
//      reaches the wire BEFORE the CDS carrying that cluster — a
//      deterministic 503 NC window that WithOrderedADS does not close,
//      because SotW can only answer open watches.
//   3. With WithOrderedADS(), a REMOVAL (drop cluster + de-reference route in
//      one snapshot) is still delivered CDS first — the cluster disappears
//      while the applied route still references it. Ordered ADS is
//      insufficient for removals (the model's
//      orderedRemovalStillBrokenBugSystem); only a control-plane grace window
//      (de-reference in one snapshot, remove in a later one) is safe.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	envoydiscoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	sotwv3 "github.com/envoyproxy/go-control-plane/pkg/server/sotw/v3"
	xdsserverv3 "github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"google.golang.org/grpc"
)

const probeNodeID = "delivery-order-probe-node"

// adsProbeStream is a minimal AggregatedDiscoveryService stream: requests are
// fed through recv, responses collected on sent.
type adsProbeStream struct {
	ctx  context.Context
	recv chan *envoydiscoveryv3.DiscoveryRequest
	sent chan *envoydiscoveryv3.DiscoveryResponse
	grpc.ServerStream
}

func (s *adsProbeStream) Context() context.Context { return s.ctx }

func (s *adsProbeStream) Send(resp *envoydiscoveryv3.DiscoveryResponse) error {
	select {
	case s.sent <- resp:
		return nil
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *adsProbeStream) Recv() (*envoydiscoveryv3.DiscoveryRequest, error) {
	select {
	case req, ok := <-s.recv:
		if !ok {
			return nil, context.Canceled
		}
		return req, nil
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func probeRouteConfig(clusterNames ...string) *envoyroutev3.RouteConfiguration {
	routes := make([]*envoyroutev3.Route, 0, len(clusterNames))
	for _, clusterName := range clusterNames {
		routes = append(routes, &envoyroutev3.Route{
			Name: "route-" + clusterName,
			Action: &envoyroutev3.Route_Route{
				Route: &envoyroutev3.RouteAction{
					ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: clusterName},
				},
			},
		})
	}
	return &envoyroutev3.RouteConfiguration{
		Name: "route-config",
		VirtualHosts: []*envoyroutev3.VirtualHost{
			{Name: "vhost", Domains: []string{"*"}, Routes: routes},
		},
	}
}

// probeSnapshot builds a snapshot with independent per-type versions so a
// scenario can change CDS without touching the RDS version (and vice versa).
func probeSnapshot(cdsVersion string, clusterNames []string, rdsVersion string, routeTargets []string) *envoycache.Snapshot {
	clusters := make([]envoycachetypes.ResourceWithTTL, 0, len(clusterNames))
	for _, name := range clusterNames {
		clusters = append(clusters, envoycachetypes.ResourceWithTTL{Resource: edsClusterProto(name)})
	}
	snap := &envoycache.Snapshot{}
	snap.Resources[envoycachetypes.Cluster] = envoycache.NewResourcesWithTTL(cdsVersion, clusters)
	snap.Resources[envoycachetypes.Route] = envoycache.NewResourcesWithTTL(rdsVersion, []envoycachetypes.ResourceWithTTL{
		{Resource: probeRouteConfig(routeTargets...)},
	})
	return snap
}

// probeHarness runs one real ADS stream against a fresh cache/server pair.
type probeHarness struct {
	t            *testing.T
	ctx          context.Context
	cancel       context.CancelFunc
	cache        envoycache.SnapshotCache
	stream       *adsProbeStream
	streamDone   chan error
	requestsSeen atomic.Int64
}

func newProbeHarness(t *testing.T, ordered bool) *probeHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	h := &probeHarness{
		t:      t,
		ctx:    ctx,
		cancel: cancel,
		cache:  envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil),
		stream: &adsProbeStream{
			ctx:  ctx,
			recv: make(chan *envoydiscoveryv3.DiscoveryRequest, 8),
			sent: make(chan *envoydiscoveryv3.DiscoveryResponse, 8),
		},
		streamDone: make(chan error, 1),
	}
	callbacks := xdsserverv3.CallbackFuncs{
		StreamRequestFunc: func(_ int64, _ *envoydiscoveryv3.DiscoveryRequest) error {
			h.requestsSeen.Add(1)
			return nil
		},
	}
	var srv xdsserverv3.Server
	if ordered {
		srv = xdsserverv3.NewServer(ctx, h.cache, callbacks, sotwv3.WithOrderedADS())
	} else {
		srv = xdsserverv3.NewServer(ctx, h.cache, callbacks)
	}
	go func() { h.streamDone <- srv.StreamAggregatedResources(h.stream) }()
	return h
}

func (h *probeHarness) close() {
	h.cancel()
	<-h.streamDone
}

func (h *probeHarness) request(typeURL string, names []string, version, nonce string) {
	h.stream.recv <- &envoydiscoveryv3.DiscoveryRequest{
		Node:          &envoycorev3.Node{Id: probeNodeID},
		TypeUrl:       typeURL,
		ResourceNames: names,
		VersionInfo:   version,
		ResponseNonce: nonce,
	}
}

// awaitRequestsSeen waits until the server has processed n requests total
// (each processed request re-opens that type's watch in the cache) plus a
// small settle so CreateWatch has registered.
func (h *probeHarness) awaitRequestsSeen(n int64) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for h.requestsSeen.Load() < n {
		if time.Now().After(deadline) {
			h.t.Fatalf("server processed %d requests; want %d", h.requestsSeen.Load(), n)
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(5 * time.Millisecond)
}

func (h *probeHarness) awaitResponses(n int) []*envoydiscoveryv3.DiscoveryResponse {
	h.t.Helper()
	out := make([]*envoydiscoveryv3.DiscoveryResponse, 0, n)
	for len(out) < n {
		select {
		case resp := <-h.stream.sent:
			out = append(out, resp)
		case <-h.ctx.Done():
			h.t.Fatalf("timed out awaiting %d responses (got %d)", n, len(out))
		}
	}
	return out
}

func (h *probeHarness) setSnapshot(snap *envoycache.Snapshot) {
	h.t.Helper()
	if err := h.cache.SetSnapshot(h.ctx, probeNodeID, snap); err != nil {
		h.t.Fatalf("SetSnapshot: %v", err)
	}
}

func (h *probeHarness) ack(resp *envoydiscoveryv3.DiscoveryResponse) {
	names := []string(nil)
	if resp.GetTypeUrl() == envoyresourcev3.RouteType {
		names = []string{"route-config"}
	}
	h.request(resp.GetTypeUrl(), names, resp.GetVersionInfo(), resp.GetNonce())
}

// openBothWatches subscribes CDS (wildcard) and RDS and waits for both
// watches to be open, so a subsequent SetSnapshot answers them together.
func (h *probeHarness) openBothWatches() {
	h.t.Helper()
	h.request(envoyresourcev3.ClusterType, nil, "", "")
	h.request(envoyresourcev3.RouteType, []string{"route-config"}, "", "")
	h.awaitRequestsSeen(2)
}

// additionTrial applies one snapshot to two open watches and returns the
// wire order of the two response type URLs.
func additionTrial(t *testing.T, ordered bool) []string {
	t.Helper()
	h := newProbeHarness(t, ordered)
	defer h.close()
	h.openBothWatches()
	h.setSnapshot(probeSnapshot("c1", []string{"cluster-new"}, "r1", []string{"cluster-new"}))
	responses := h.awaitResponses(2)
	return []string{responses[0].GetTypeUrl(), responses[1].GetTypeUrl()}
}

// TestADSAdditionOnQuietStreamIsClusterFirst documents finding 1: on a quiet
// stream the cache's own type-ordered response writes reach the wire CDS
// before RDS in both server modes; the default server's reflect.Select
// randomization needs a busy stream to manifest. If this starts failing the
// quiet-stream ordering assumption changed and GCP-A3 must be re-derived.
func TestADSAdditionOnQuietStreamIsClusterFirst(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		clusterFirst := 0
		const trials = 20
		for range trials {
			order := additionTrial(t, ordered)
			if order[0] == envoyresourcev3.ClusterType {
				clusterFirst++
			}
		}
		t.Logf("ordered=%v: CDS delivered first in %d/%d quiet-stream addition trials", ordered, clusterFirst, trials)
		if clusterFirst < trials {
			t.Errorf("ordered=%v: expected quiet-stream additions to be CDS-first (got %d/%d); re-derive GCP-A3", ordered, clusterFirst, trials)
		}
	}
}

// TestADSAckSkewDeliversRouteBeforeClusterEvenWithOrderedADS is finding 2,
// and it is deterministic — no scheduler luck involved:
//
//	v1: cluster c-old, route -> c-old        (delivered, ACKed)
//	v2: clusters {c-old, c-x}, RDS unchanged (CDS delivered, NOT yet ACKed)
//	v3: clusters {c-old, c-new}, route -> c-new
//
// At v3 the CDS watch is closed (awaiting Envoy's v2 ACK) while the RDS watch
// is open, so the ONLY possible response is RDS: the route referencing
// c-new reaches the wire before any CDS carrying c-new, in ordered and
// unordered mode alike. Envoy would 503 NC on that route until it ACKs v2 and
// receives v3's CDS. WithOrderedADS cannot close this window (SotW answers
// only open watches); avoiding it needs control-plane pacing — do not retarget
// routes to a cluster in the same update burst that still has its CDS ACK in
// flight — or Envoy-side route-to-missing-cluster tolerance.
func TestADSAckSkewDeliversRouteBeforeClusterEvenWithOrderedADS(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		h := newProbeHarness(t, ordered)
		h.openBothWatches()

		// v1: steady state, both types delivered and ACKed.
		h.setSnapshot(probeSnapshot("c1", []string{"c-old"}, "r1", []string{"c-old"}))
		for _, resp := range h.awaitResponses(2) {
			h.ack(resp)
		}
		h.awaitRequestsSeen(4)

		// v2: CDS-only change. The CDS response goes out; we deliberately do
		// not ACK it (Envoy is still warming), so the CDS watch stays closed.
		h.setSnapshot(probeSnapshot("c2", []string{"c-old", "c-x"}, "r1", []string{"c-old"}))
		v2 := h.awaitResponses(1)[0]
		if v2.GetTypeUrl() != envoyresourcev3.ClusterType {
			t.Fatalf("ordered=%v: v2 should only answer CDS, got %s", ordered, v2.GetTypeUrl())
		}

		// v3: new cluster + route retarget in one snapshot.
		h.setSnapshot(probeSnapshot("c3", []string{"c-old", "c-new"}, "r3", []string{"c-new"}))
		first := h.awaitResponses(1)[0]
		if first.GetTypeUrl() != envoyresourcev3.RouteType {
			t.Fatalf("ordered=%v: expected ACK skew to deliver RDS before CDS, got %s first", ordered, first.GetTypeUrl())
		}
		t.Logf("ordered=%v: v3 delivered RDS (route -> c-new) while CDS v3 was still held behind the outstanding v2 ACK — 503 NC window", ordered)

		// Completeness: ACKing v2 releases v3's CDS.
		h.ack(v2)
		if got := h.awaitResponses(1)[0].GetTypeUrl(); got != envoyresourcev3.ClusterType {
			t.Fatalf("ordered=%v: expected CDS v3 after the v2 ACK, got %s", ordered, got)
		}
		h.close()
	}
}

// TestADSOrderedServerStillDeliversClusterRemovalBeforeRouteUpdate is finding
// 3: WithOrderedADS's fixed CDS-before-RDS order is the WRONG order for a
// removal. A snapshot that de-references and removes a cluster in one update
// delivers the cluster removal first, so Envoy's still-applied route briefly
// references a cluster that no longer exists (503 NC). Ordered ADS therefore
// does not replace the control-plane grace window (de-reference in one
// snapshot, remove the cluster in a later one).
func TestADSOrderedServerStillDeliversClusterRemovalBeforeRouteUpdate(t *testing.T) {
	const trials = 20
	clusterFirst := 0
	for range trials {
		h := newProbeHarness(t, true)
		h.openBothWatches()
		h.setSnapshot(probeSnapshot("c1", []string{"cluster-old"}, "r1", []string{"cluster-old"}))
		for _, resp := range h.awaitResponses(2) {
			h.ack(resp)
		}
		h.awaitRequestsSeen(4)

		// One snapshot both retargets the route and removes the old cluster.
		h.setSnapshot(probeSnapshot("c2", []string{"cluster-new"}, "r2", []string{"cluster-new"}))
		responses := h.awaitResponses(2)
		if responses[0].GetTypeUrl() == envoyresourcev3.ClusterType {
			clusterFirst++
		}
		h.close()
	}
	t.Logf("ordered server: cluster change delivered before route change in %d/%d removal trials", clusterFirst, trials)
	if clusterFirst == 0 {
		t.Fatal("expected ordered ADS to deliver the CDS change before the RDS change on a combined retarget+removal; if this no longer holds, GCP-A3's removal caveat needs re-deriving")
	}
}
