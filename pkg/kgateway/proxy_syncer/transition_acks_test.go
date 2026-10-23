package proxy_syncer

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAcks stands in for the control plane's ACK tracking with one stream.
type fakeAcks struct {
	mu        sync.Mutex
	resources map[string]map[string]struct{}
	versions  map[string]string
	notify    func(string)
}

func (f *fakeAcks) AckedResources(_, typeURL string, names []string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	acked, sent := f.resources[typeURL]
	if !sent {
		return false
	}
	for _, name := range names {
		if _, ok := acked[name]; !ok {
			return false
		}
	}
	return true
}

func (f *fakeAcks) AckedVersions(_, typeURL string) map[int64]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	version, sent := f.versions[typeURL]
	if !sent {
		return nil
	}
	return map[int64]string{1: version}
}

func (f *fakeAcks) NotifyAcks(fn func(string)) { f.notify = fn }

// ackResources ACKs a response of typeURL carrying names.
func (f *fakeAcks) ackResources(typeURL string, names ...string) {
	f.mu.Lock()
	if f.resources == nil {
		f.resources = make(map[string]map[string]struct{})
	}
	acked := make(map[string]struct{}, len(names))
	for _, name := range names {
		acked[name] = struct{}{}
	}
	f.resources[typeURL] = acked
	f.mu.Unlock()
	f.notify(publishGateTestClient)
}

// ackVersion ACKs version of typeURL.
func (f *fakeAcks) ackVersion(typeURL, version string) {
	f.mu.Lock()
	if f.versions == nil {
		f.versions = make(map[string]string)
	}
	f.versions[typeURL] = version
	f.mu.Unlock()
	f.notify(publishGateTestClient)
}

// newAckingTranslator returns a translator whose transition windows end on
// acks' ACKs, bounded by an hour unless scoping says otherwise.
func newAckingTranslator(t *testing.T, scoping clusterScoping, acks *fakeAcks) *ProxyTranslator {
	t.Helper()
	scoping.transitionAcks = true
	translator := NewProxyTranslator(newTestSnapshotCache(t), stubPriorXDS{has: true}, time.Hour, true, scoping)
	translator.gate.useTransitionAcks(acks)
	pt := &translator
	t.Cleanup(func() { pt.gate.clientDeparted(publishGateTestClient) })
	return pt
}

// A reference-ahead hold ends when the proxy has the new cluster: its cluster
// response and, since it takes endpoints from EDS, an endpoint response
// carrying its assignment. The cluster's ACK alone leaves it held.
func TestReferenceAheadHoldEndsWhenTheProxyAcksTheNewCluster(t *testing.T) {
	acks := &fakeAcks{}
	pt := newAckingTranslator(t, scopedClustersWithReferenceAhead(time.Hour), acks)
	ctx := context.Background()

	pt.syncXds(ctx, routedBuild(t, "v1", "old", "old"))
	pt.syncXds(ctx, routedBuild(t, "v2", "new", "new"))
	require.Equal(t, "v1", publishedListenerVersionOf(t, pt), "the retarget is held")

	acks.ackResources(resourcev3.ClusterType, "new")
	require.Never(t, func() bool { return publishedListenerVersionOf(t, pt) == "v2" }, 200*time.Millisecond, 10*time.Millisecond,
		"an EDS cluster is not usable until its endpoints arrive")

	acks.ackResources(resourcev3.EndpointType, "new")
	require.Eventually(t, func() bool { return publishedListenerVersionOf(t, pt) == "v2" }, 2*time.Second, 5*time.Millisecond,
		"the hold ends on the ACKs, not its hour-long window")
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

	acks.ackVersion(resourcev3.ListenerType, before.GetVersion(resourcev3.ListenerType))
	acks.ackVersion(resourcev3.RouteType, before.GetVersion(resourcev3.RouteType))
	require.Never(t, func() bool { return !slices.Contains(publishedClusterNames(t, pt), "old") }, 200*time.Millisecond, 10*time.Millisecond,
		"a proxy still on the routes that name old keeps it")

	acks.ackVersion(resourcev3.ListenerType, after.GetVersion(resourcev3.ListenerType))
	acks.ackVersion(resourcev3.RouteType, after.GetVersion(resourcev3.RouteType))
	require.Eventually(t, func() bool { return !slices.Contains(publishedClusterNames(t, pt), "old") }, 2*time.Second, 5*time.Millisecond,
		"old leaves on the ACKs, not its hour-long grace")
	assert.Contains(t, publishedClusterNames(t, pt), "new")
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
