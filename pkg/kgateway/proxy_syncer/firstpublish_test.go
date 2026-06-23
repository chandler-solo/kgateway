package proxy_syncer

import (
	"context"
	"fmt"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPriorXDS stubs the prior-xDS-version reader. has=false models a cold
// client (the default for most tests); has=true models a warm reconnect that
// reported a prior accepted version.
type stubPriorXDS struct{ has bool }

func (s stubPriorXDS) HasPriorXDSVersion(string) bool { return s.has }

func newTestTranslator() *ProxyTranslator {
	pt := NewProxyTranslator(envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil), stubPriorXDS{})
	return &pt
}

func newTestTranslatorPriorXDS() *ProxyTranslator {
	pt := NewProxyTranslator(envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil), stubPriorXDS{has: true})
	return &pt
}

// wrapperWithVersion builds a wrapper whose listener version identifies it,
// so tests can tell which wrapper a published snapshot came from.
const testClientKey = "c1"

func wrapperWithVersion(version string, deferred bool, reasons ...string) XdsSnapWrapper {
	snapshot := &envoycache.Snapshot{}
	snapshot.Resources[envoycachetypes.Listener] = envoycache.NewResources(version, nil)
	return XdsSnapWrapper{
		snap:         snapshot,
		proxyKey:     testClientKey,
		deferred:     deferred,
		deferReasons: reasons,
	}
}

func publishedVersion(t *testing.T, pt *ProxyTranslator) (string, bool) {
	t.Helper()
	snap, err := pt.xdsCache.GetSnapshot(testClientKey)
	if err != nil {
		return "", false
	}
	return snap.GetVersion(resourcev3.ListenerType), true
}

// KGW_PER_CLIENT_PUBLISH_BUDGET=0 is the conservative opt-out: deferred
// snapshots are withheld with no deadline, the pre-budget behavior.
func TestSyncXds_BudgetZeroDisablesBoundedPublishing(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 0

	pt.syncXds(context.Background(), wrapperWithVersion("deferred", true, deferReasonMissingClusters))
	time.Sleep(100 * time.Millisecond)
	_, ok := publishedVersion(t, pt)
	assert.False(t, ok, "budget=0 must withhold deferred snapshots indefinitely")

	// A coherent snapshot still publishes normally.
	pt.syncXds(context.Background(), wrapperWithVersion("coherent", false))
	v, _ := publishedVersion(t, pt)
	assert.Equal(t, "coherent", v)
}

func TestSyncXds_CoherentPublishesImmediately(t *testing.T) {
	pt := newTestTranslator()
	pt.syncXds(context.Background(), wrapperWithVersion("v1", false))

	v, ok := publishedVersion(t, pt)
	require.True(t, ok, "coherent snapshot should publish immediately")
	assert.Equal(t, "v1", v)
}

func TestSyncXds_DeferredToNeverPublishedWaitsThenPublishes(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 50 * time.Millisecond

	pt.syncXds(context.Background(), wrapperWithVersion("v1", true, deferReasonMissingClusters))

	_, ok := publishedVersion(t, pt)
	assert.False(t, ok, "deferred snapshot must not publish before the budget expires")

	require.Eventually(t, func() bool {
		_, ok := publishedVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond, "deferred snapshot must publish after the budget expires")

	v, _ := publishedVersion(t, pt)
	assert.Equal(t, "v1", v)
}

func TestSyncXds_LatestDeferredWinsAtBudgetExpiry(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 80 * time.Millisecond

	pt.syncXds(context.Background(), wrapperWithVersion("v1", true, deferReasonMissingClusters))
	pt.syncXds(context.Background(), wrapperWithVersion("v2", true, deferReasonMissingClusters))

	require.Eventually(t, func() bool {
		_, ok := publishedVersion(t, pt)
		return ok
	}, 2*time.Second, 5*time.Millisecond)

	v, _ := publishedVersion(t, pt)
	assert.Equal(t, "v2", v, "the most recent deferred wrapper must be the one published")
}

func TestSyncXds_CoherentSupersedesPendingDeferred(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 100 * time.Millisecond

	pt.syncXds(context.Background(), wrapperWithVersion("deferred", true, deferReasonMissingEndpoints))
	pt.syncXds(context.Background(), wrapperWithVersion("coherent", false))

	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent", v)

	// Past the original budget, the canceled timer must not have replaced the
	// coherent snapshot with the stale deferred one.
	time.Sleep(200 * time.Millisecond)
	v, _ = publishedVersion(t, pt)
	assert.Equal(t, "coherent", v, "a canceled first-publish timer must never overwrite a coherent snapshot")
}

// snapshotWith builds a snapshot with explicit cluster/endpoint content; the
// listener version doubles as the snapshot's identity marker in assertions.
func snapshotWith(listenerVersion string, clusters, endpoints map[string]envoycachetypes.ResourceWithTTL) *envoycache.Snapshot {
	s := &envoycache.Snapshot{}
	s.Resources[envoycachetypes.Listener] = envoycache.NewResources(listenerVersion, nil)
	s.Resources[envoycachetypes.Cluster] = envoycache.Resources{Version: "clusters-" + listenerVersion, Items: clusters}
	s.Resources[envoycachetypes.Endpoint] = envoycache.Resources{Version: "endpoints-" + listenerVersion, Items: endpoints}
	return s
}

func realCla(name string) envoycachetypes.ResourceWithTTL {
	return envoycachetypes.ResourceWithTTL{Resource: &envoyendpointv3.ClusterLoadAssignment{
		ClusterName: name,
		Endpoints:   []*envoyendpointv3.LocalityLbEndpoints{{}},
	}}
}

func emptyCla(name string) envoycachetypes.ResourceWithTTL {
	return envoycachetypes.ResourceWithTTL{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: name}}
}

func publishedCla(t *testing.T, pt *ProxyTranslator, name string) *envoyendpointv3.ClusterLoadAssignment {
	t.Helper()
	snap, err := pt.xdsCache.GetSnapshot(testClientKey)
	require.NoError(t, err)
	res, ok := snap.GetResourcesAndTTL(resourcev3.EndpointType)[name]
	require.True(t, ok, "published snapshot must contain CLA %s", name)
	return res.Resource.(*envoyendpointv3.ClusterLoadAssignment)
}

// A warm client must be withheld within the budget, then receive the deferred
// snapshot MERGED with carry-forward at budget expiry: the new config flows,
// while the cluster the deferred snapshot is missing (mid-rebuild) and its
// real CLA are retained from the published snapshot — an incomplete publish
// can never remove a resource the client is using.
func TestSyncXds_WarmClientGetsCarryForwardMergeAtBudget(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 60 * time.Millisecond

	coherent := XdsSnapWrapper{
		proxyKey: testClientKey,
		snap: snapshotWith("v1",
			map[string]envoycachetypes.ResourceWithTTL{"cluster-a": edsClusterResource("cluster-a")},
			map[string]envoycachetypes.ResourceWithTTL{"cluster-a": realCla("cluster-a")}),
	}
	pt.syncXds(context.Background(), coherent)

	// The deferred snapshot has NEW config (listener v2, new cluster-b) but
	// lost cluster-a mid-rebuild; its only CLA is a synthesized empty for b.
	deferred := XdsSnapWrapper{
		proxyKey: testClientKey,
		snap: snapshotWith("v2",
			map[string]envoycachetypes.ResourceWithTTL{"cluster-b": edsClusterResource("cluster-b")},
			map[string]envoycachetypes.ResourceWithTTL{"cluster-b": emptyCla("cluster-b")}),
		deferred:           true,
		deferReasons:       []string{deferReasonMissingClusters},
		referencedClusters: map[string]struct{}{"cluster-a": {}, "cluster-b": {}},
		synthesizedClas:    []string{"cluster-b"},
	}
	pt.syncXds(context.Background(), deferred)

	// Within the budget: withheld, the coherent snapshot stays published.
	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "v1", v, "warm client must be withheld within the budget")

	// At budget expiry: the new config is published with carry-forward.
	require.Eventually(t, func() bool {
		v, ok := publishedVersion(t, pt)
		return ok && v == "v2"
	}, 2*time.Second, 5*time.Millisecond, "budget expiry must publish the new config to the warm client")

	snap, err := pt.xdsCache.GetSnapshot(testClientKey)
	require.NoError(t, err)
	clusters := snap.GetResourcesAndTTL(resourcev3.ClusterType)
	assert.Contains(t, clusters, "cluster-a", "the in-use cluster must be carried forward, not removed")
	assert.Contains(t, clusters, "cluster-b", "the new cluster must be present")
	assert.NotEmpty(t, publishedCla(t, pt, "cluster-a").GetEndpoints(),
		"the carried-forward cluster must keep its real endpoints")
	assert.Contains(t, snap.GetVersion(resourcev3.ClusterType), "-carry-",
		"merged resource versions must be distinct from both inputs")
}

// When the deferred snapshot still has the cluster but only a synthesized
// empty CLA for it, the merge must prefer the previously-published real CLA:
// publishing the empty would drain live traffic.
func TestSyncXds_CarryForwardPrefersPublishedClaOverSynthesized(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 40 * time.Millisecond

	pt.syncXds(context.Background(), XdsSnapWrapper{
		proxyKey: testClientKey,
		snap: snapshotWith("v1",
			map[string]envoycachetypes.ResourceWithTTL{"cluster-a": edsClusterResource("cluster-a")},
			map[string]envoycachetypes.ResourceWithTTL{"cluster-a": realCla("cluster-a")}),
	})
	pt.syncXds(context.Background(), XdsSnapWrapper{
		proxyKey: testClientKey,
		snap: snapshotWith("v2",
			map[string]envoycachetypes.ResourceWithTTL{"cluster-a": edsClusterResource("cluster-a")},
			map[string]envoycachetypes.ResourceWithTTL{"cluster-a": emptyCla("cluster-a")}),
		deferred:           true,
		deferReasons:       []string{deferReasonMissingEndpoints},
		referencedClusters: map[string]struct{}{"cluster-a": {}},
		synthesizedClas:    []string{"cluster-a"},
	})

	require.Eventually(t, func() bool {
		v, ok := publishedVersion(t, pt)
		return ok && v == "v2"
	}, 2*time.Second, 5*time.Millisecond)

	assert.NotEmpty(t, publishedCla(t, pt, "cluster-a").GetEndpoints(),
		"the published real CLA must be preferred over the synthesized empty")
}

// The bounded publish must never be a dead end: once the budget has expired
// and a deferred snapshot (with synthesized empty CLAs) was published, a later
// coherent snapshot — e.g. the real ClusterLoadAssignments arriving — must
// overwrite it. This is the guard against the classic failure mode where a
// client receives an empty endpoints set and then never observes the real
// endpoints.
func TestSyncXds_CoherentAfterBoundedPublishOverwrites(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 30 * time.Millisecond

	pt.syncXds(context.Background(), wrapperWithVersion("deferred-synth", true, deferReasonMissingEndpoints))

	require.Eventually(t, func() bool {
		v, ok := publishedVersion(t, pt)
		return ok && v == "deferred-synth"
	}, 2*time.Second, 5*time.Millisecond, "budget expiry must publish the deferred snapshot")

	// The real inputs arrive: the coherent snapshot must supersede the
	// bounded one.
	pt.syncXds(context.Background(), wrapperWithVersion("coherent-real", false))
	v, ok := publishedVersion(t, pt)
	require.True(t, ok)
	assert.Equal(t, "coherent-real", v,
		"a coherent snapshot after a bounded publish must overwrite the synthesized one")
}

func TestSyncXds_ClientDepartureCancelsPendingFirstPublish(t *testing.T) {
	pt := newTestTranslator()
	pt.publishBudget.budget = 50 * time.Millisecond

	pt.syncXds(context.Background(), wrapperWithVersion("v1", true, deferReasonMissingClusters))
	pt.publishBudget.clientDeparted(testClientKey)

	time.Sleep(150 * time.Millisecond)
	_, ok := publishedVersion(t, pt)
	assert.False(t, ok, "a departed client's pending first publish must not fire")
}

func edsClusterResource(name string) envoycachetypes.ResourceWithTTL {
	return envoycachetypes.ResourceWithTTL{
		Resource: &envoyclusterv3.Cluster{
			Name:                 name,
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
		},
	}
}

// A client with no local snapshot that reports a prior accepted xDS version is
// warm (a reconnect / controller restart), so at budget expiry its deferred
// snapshot is WITHHELD, not published — publishing incomplete config would
// replace what it is already serving (#13868). Contrast with
// TestSyncXds_DeferredToNeverPublishedWaitsThenPublishes (a cold client, which
// does get the bounded publish).
func TestSyncXds_PriorXDSVersionClientWithheldAtBudget(t *testing.T) {
	pt := newTestTranslatorPriorXDS()
	pt.publishBudget.budget = 50 * time.Millisecond

	pt.syncXds(context.Background(), wrapperWithVersion("v1", true, "missing_clusters"))

	require.Never(t, func() bool {
		_, ok := publishedVersion(t, pt)
		return ok
	}, 250*time.Millisecond, 20*time.Millisecond,
		"a prior-xDS-version (warm reconnect) client must not receive a deferred snapshot at budget expiry")
}

func TestSynthesizeEmptyEndpointResources(t *testing.T) {
	clusters := map[string]envoycachetypes.ResourceWithTTL{
		"eds-a": edsClusterResource("eds-a"),
		"eds-b": edsClusterResource("eds-b"),
		"static-c": {
			Resource: &envoyclusterv3.Cluster{
				Name:                 "static-c",
				ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC},
			},
		},
	}
	existing := envoycache.Resources{
		Version: "base",
		Items: map[string]envoycachetypes.ResourceWithTTL{
			"eds-a": {Resource: &envoyendpointv3.ClusterLoadAssignment{
				ClusterName: "eds-a",
				Endpoints:   []*envoyendpointv3.LocalityLbEndpoints{{}},
			}},
		},
	}

	out, synthesizedNames := synthesizeEmptyEndpointResources([]string{"eds-b", "static-c", "not-a-cluster"}, clusters, existing)

	assert.Equal(t, []string{"eds-b"}, synthesizedNames, "only the synthesizable EDS cluster is reported")
	require.Contains(t, out.Items, "eds-b", "missing EDS cluster gets a synthesized CLA")
	synth := out.Items["eds-b"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	assert.Equal(t, "eds-b", synth.GetClusterName())
	assert.Empty(t, synth.GetEndpoints(), "synthesized CLA must be explicitly empty")

	assert.NotContains(t, out.Items, "static-c", "non-EDS clusters need no CLA")
	assert.Len(t, out.Items, 2, "existing CLAs are preserved, unknown names skipped")
	got := out.Items["eds-a"].Resource.(*envoyendpointv3.ClusterLoadAssignment)
	assert.NotEmpty(t, got.GetEndpoints(), "existing real CLA must be untouched")

	assert.NotEqual(t, existing.Version, out.Version, "synthesis must change the resources version")
	assert.Equal(t, fmt.Sprintf("base-synth-%d", 0)[:5], out.Version[:5], "version derives from the base version")
}
