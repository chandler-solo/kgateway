package proxy_syncer

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoytlsv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

// fakeAcks stands in for the control plane's ACK tracking with one stream that
// has been sent every type.
type fakeAcks struct {
	mu       sync.Mutex
	names    map[string]map[string]struct{}
	versions map[string]string
	// current, when set, acknowledges every installed snapshot at once,
	// without notifying.
	current func(typeURL string) string
	notify  func(string)
}

func (f *fakeAcks) AckedResources(_, typeURL string, names []string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current != nil {
		return true
	}
	for _, name := range names {
		if _, ok := f.names[typeURL][name]; !ok {
			return false
		}
	}
	return true
}

func (f *fakeAcks) AckedVersions(_, typeURL string) map[int64]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	version := f.versions[typeURL]
	if f.current != nil {
		version = f.current(typeURL)
	}
	return map[int64]string{1: version}
}

func (f *fakeAcks) NotifyAcks(fn func(string)) { f.notify = fn }

// ack ACKs a typeURL response at version carrying names, and notifies.
func (f *fakeAcks) ack(typeURL, version string, names ...string) {
	f.mu.Lock()
	if f.names == nil {
		f.names = make(map[string]map[string]struct{})
		f.versions = make(map[string]string)
	}
	acked := make(map[string]struct{}, len(names))
	for _, name := range names {
		acked[name] = struct{}{}
	}
	f.names[typeURL] = acked
	f.versions[typeURL] = version
	f.mu.Unlock()
	f.notify(publishGateTestClient)
}

// ackServed ACKs the cluster and endpoint responses of the served snapshot.
func (f *fakeAcks) ackServed(t *testing.T, pt *ProxyTranslator) {
	t.Helper()
	served := servedSnapshot(t, pt)
	clusters := slices.Collect(func(yield func(string) bool) {
		for name := range served.GetResourcesAndTTL(resourcev3.ClusterType) {
			if !yield(name) {
				return
			}
		}
	})
	f.ack(resourcev3.ClusterType, served.GetVersion(resourcev3.ClusterType), clusters...)
	f.ack(resourcev3.EndpointType, served.GetVersion(resourcev3.EndpointType), clusters...)
}

// newAckingTranslator returns a translator whose transition windows end on
// acks' ACKs.
func newAckingTranslator(t *testing.T, scoping clusterScoping, acks *fakeAcks) *ProxyTranslator {
	t.Helper()
	scoping.transitionAcks = true
	translator := NewProxyTranslator(newTestSnapshotCache(t), stubPriorXDS{has: true}, time.Hour, true, scoping)
	translator.gate.useTransitionAcks(acks)
	pt := &translator
	t.Cleanup(func() { pt.gate.clientDeparted(publishGateTestClient) })
	return pt
}

// acknowledgeEverything makes acks acknowledge whatever pt has installed.
func acknowledgeEverything(t *testing.T, pt *ProxyTranslator, acks *fakeAcks) {
	acks.mu.Lock()
	defer acks.mu.Unlock()
	acks.current = func(typeURL string) string {
		snap, err := pt.xdsCache.GetSnapshot(publishGateTestClient)
		if err != nil {
			return ""
		}
		return snap.GetVersion(typeURL)
	}
}

// A reference-ahead hold ends when the proxy has the new cluster: a cluster
// response from the held install and, since it takes endpoints from EDS, an
// endpoint response carrying its assignment. The cluster's ACK alone leaves
// it held.
func TestReferenceAheadHoldEndsWhenTheProxyAcksTheNewCluster(t *testing.T) {
	acks := &fakeAcks{}
	pt := newAckingTranslator(t, scopedClustersWithReferenceAhead(time.Hour), acks)
	ctx := context.Background()

	pt.syncXds(ctx, routedBuild(t, "v1", "old", "old"))
	pt.syncXds(ctx, routedBuild(t, "v2", "new", "new"))
	require.Equal(t, "v1", publishedListenerVersionOf(t, pt), "the retarget is held")
	held := servedSnapshot(t, pt)

	acks.ack(resourcev3.ClusterType, held.GetVersion(resourcev3.ClusterType), "old", "new")
	require.Never(t, func() bool { return publishedListenerVersionOf(t, pt) == "v2" }, 200*time.Millisecond, 10*time.Millisecond,
		"an EDS cluster is not usable until its endpoints arrive")

	acks.ack(resourcev3.EndpointType, held.GetVersion(resourcev3.EndpointType), "old", "new")
	require.Eventually(t, func() bool { return publishedListenerVersionOf(t, pt) == "v2" }, 2*time.Second, 5*time.Millisecond,
		"the hold ends on the ACKs, not its hour-long window")
}

// After a to b and back to a, the proxy's last ACKs can still carry a while
// a's removal is in flight. Those ACKs predate a's window, so the hold waits
// for a response from the re-add.
func TestReferenceAheadIgnoresAcksFromBeforeTheClusterReturned(t *testing.T) {
	acks := &fakeAcks{}
	pt := newAckingTranslator(t, scopedClustersWithReferenceAhead(time.Hour), acks)
	ctx := context.Background()

	pt.syncXds(ctx, routedBuild(t, "v1", "a", "a"))
	acks.ackServed(t, pt)
	pt.syncXds(ctx, routedBuild(t, "v2", "b", "b")) // b is new: held, carrying a
	acks.ackServed(t, pt)
	require.Eventually(t, func() bool { return publishedListenerVersionOf(t, pt) == "v2" }, 2*time.Second, 5*time.Millisecond)
	require.NotContains(t, publishedClusterNames(t, pt), "a", "the release removes a; the proxy has not ACKed it")

	pt.syncXds(ctx, routedBuild(t, "v3", "a", "a")) // a is new again
	require.Never(t, func() bool { return publishedListenerVersionOf(t, pt) == "v3" }, 300*time.Millisecond, 10*time.Millisecond,
		"ACKs from before a's removal do not show the proxy has a")

	acks.ackServed(t, pt)
	require.Eventually(t, func() bool { return publishedListenerVersionOf(t, pt) == "v3" }, 2*time.Second, 5*time.Millisecond,
		"an ACK of the re-add releases it")
}

// A de-referenced cluster leaves once the proxy has applied the route update
// that stopped naming it. An ACK of the earlier routes keeps it.
func TestDereferencedClusterLeavesWhenTheProxyAcksTheRouteUpdate(t *testing.T) {
	acks := &fakeAcks{}
	pt := newAckingTranslator(t, scopedClustersWithGrace(time.Hour), acks)
	ctx := context.Background()

	pt.syncXds(ctx, routedBuild(t, "v1", "old", "old"))
	before := servedSnapshot(t, pt)
	pt.syncXds(ctx, routedBuild(t, "v2", "new", "new"))
	after := servedSnapshot(t, pt)
	require.Contains(t, publishedClusterNames(t, pt), "old", "the grace keeps old")

	acks.ack(resourcev3.ListenerType, before.GetVersion(resourcev3.ListenerType))
	acks.ack(resourcev3.RouteType, before.GetVersion(resourcev3.RouteType))
	require.Never(t, func() bool { return !slices.Contains(publishedClusterNames(t, pt), "old") }, 200*time.Millisecond, 10*time.Millisecond,
		"a proxy still on the routes that name old keeps it")

	acks.ack(resourcev3.ListenerType, after.GetVersion(resourcev3.ListenerType))
	acks.ack(resourcev3.RouteType, after.GetVersion(resourcev3.RouteType))
	require.Eventually(t, func() bool { return !slices.Contains(publishedClusterNames(t, pt), "old") }, 2*time.Second, 5*time.Millisecond,
		"old leaves on the ACKs, not its hour-long grace")
	assert.Contains(t, publishedClusterNames(t, pt), "new")
}

// ACKs that arrive while the route update is being installed, before its
// window is registered, still end the window: the gate checks once it opens.
func TestDereferenceWindowSeesAcksThatArriveDuringInstallation(t *testing.T) {
	acks := &fakeAcks{}
	pt := newAckingTranslator(t, scopedClustersWithGrace(time.Hour), acks)
	acknowledgeEverything(t, pt, acks)
	ctx := context.Background()

	pt.syncXds(ctx, routedBuild(t, "v1", "old", "old"))
	pt.syncXds(ctx, routedBuild(t, "v2", "new", "new"))
	require.Eventually(t, func() bool { return !slices.Contains(publishedClusterNames(t, pt), "old") }, 2*time.Second, 5*time.Millisecond,
		"no ACK notification follows, but old still leaves before its hour-long grace")
}

// With ACKs on, both windows still close at their durations when no ACK comes.
func TestTransitionWindowsCloseAtTheirBoundsWithoutAcks(t *testing.T) {
	scoping := scopedClustersWithReferenceAhead(100 * time.Millisecond)
	scoping.dereferenceGrace = 100 * time.Millisecond
	pt := newAckingTranslator(t, scoping, &fakeAcks{})
	ctx := context.Background()

	pt.syncXds(ctx, routedBuild(t, "v1", "old", "old"))
	pt.syncXds(ctx, routedBuild(t, "v2", "new", "new"))
	require.Eventually(t, func() bool { return publishedListenerVersionOf(t, pt) == "v2" }, 2*time.Second, 5*time.Millisecond,
		"the hold is released at its window")
	require.Eventually(t, func() bool { return !slices.Contains(publishedClusterNames(t, pt), "old") }, 2*time.Second, 5*time.Millisecond,
		"old leaves at its grace")
}

// A cluster Envoy warms on something an ACK does not show keeps its window,
// even with every response acknowledged: DNS resolution, an initial health
// check pass, secrets from SDS, or an internal listener its endpoint reaches.
func TestReferenceAheadKeepsItsWindowForClustersAnAckCannotShowReady(t *testing.T) {
	withSDS := func() *envoyclusterv3.Cluster {
		c := proto.Clone(edsClusterProto("new")).(*envoyclusterv3.Cluster)
		c.TransportSocket = &envoycorev3.TransportSocket{
			Name: "envoy.transport_sockets.tls",
			ConfigType: &envoycorev3.TransportSocket_TypedConfig{TypedConfig: utils.MustMessageToAny(&envoytlsv3.UpstreamTlsContext{
				CommonTlsContext: &envoytlsv3.CommonTlsContext{
					TlsCertificateSdsSecretConfigs: []*envoytlsv3.SdsSecretConfig{{Name: "client-cert"}},
				},
			})},
		}
		return c
	}
	staticAt := func(address *envoycorev3.Address) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{
			Name:                 "new",
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC},
			LoadAssignment: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "new", Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{
				LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{
					Endpoint: &envoyendpointv3.Endpoint{Address: address},
				}}},
			}}},
		}
	}
	socket := &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{
		Address: "10.0.0.1", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 8080},
	}}}
	pipe := &envoycorev3.Address{Address: &envoycorev3.Address_Pipe{Pipe: &envoycorev3.Pipe{Path: "@tunnel"}}}
	healthChecked := proto.Clone(edsClusterProto("new")).(*envoyclusterv3.Cluster)
	healthChecked.HealthChecks = []*envoycorev3.HealthCheck{{}}

	for _, tc := range []struct {
		name    string
		cluster *envoyclusterv3.Cluster
		early   bool
	}{
		{name: "eds", cluster: edsClusterProto("new"), early: true},
		{name: "static socket address", cluster: staticAt(socket), early: true},
		{name: "strict dns", cluster: &envoyclusterv3.Cluster{Name: "new", ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STRICT_DNS}}},
		{name: "health checks", cluster: healthChecked},
		{name: "sds secrets", cluster: withSDS()},
		{name: "static pipe", cluster: staticAt(pipe)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acks := &fakeAcks{}
			pt := newAckingTranslator(t, scopedClustersWithReferenceAhead(time.Hour), acks)
			acknowledgeEverything(t, pt, acks)
			ctx := context.Background()

			pt.syncXds(ctx, routedBuild(t, "v1", "old", "old"))
			retarget := routedBuild(t, "v2", "new", "new")
			retarget.snap.Resources[envoycachetypes.Cluster].Items["new"] = envoycachetypes.ResourceWithTTL{Resource: tc.cluster}
			if _, eds := endpointResourceNameForCluster(envoycachetypes.ResourceWithTTL{Resource: tc.cluster}); !eds {
				delete(retarget.snap.Resources[envoycachetypes.Endpoint].Items, "new")
			}
			pt.syncXds(ctx, retarget)

			released := func() bool { return publishedListenerVersionOf(t, pt) == "v2" }
			if tc.early {
				require.Eventually(t, released, 2*time.Second, 5*time.Millisecond, "ACKs show this cluster ready")
				return
			}
			require.Never(t, released, 300*time.Millisecond, 10*time.Millisecond, "no ACK shows this cluster warmed")
		})
	}
}

// A cluster the earlier configuration named from a listener (here a TCP
// route) keeps its grace even with every response acknowledged: an LDS ACK
// comes before the new listener finishes warming, and the old one accepts
// connections until then.
func TestDereferenceGraceKeepsAClusterAListenerNamed(t *testing.T) {
	acks := &fakeAcks{}
	pt := newAckingTranslator(t, scopedClustersWithGrace(time.Hour), acks)
	acknowledgeEverything(t, pt, acks)
	ctx := context.Background()

	withTCPRoute := routedBuild(t, "v1", "kept", "kept", "old")
	withTCPRoute.snap.Resources[envoycachetypes.Listener].Items["tcp"] = envoycachetypes.ResourceWithTTL{Resource: tunnelListener("tcp", "old")}
	pt.syncXds(ctx, withTCPRoute)
	pt.syncXds(ctx, routedBuild(t, "v2", "kept", "kept")) // the TCP route, and old with it, go away

	require.Never(t, func() bool { return !slices.Contains(publishedClusterNames(t, pt), "old") }, 300*time.Millisecond, 10*time.Millisecond,
		"a listener-named cluster stays for its grace")
}
