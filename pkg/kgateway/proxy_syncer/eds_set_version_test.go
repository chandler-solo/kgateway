package proxy_syncer

import (
	"testing"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/stretchr/testify/require"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

func edsSetVersionItems(names ...string) map[string]envoycachetypes.ResourceWithTTL {
	items := make([]envoycachetypes.ResourceWithTTL, 0, len(names))
	for _, name := range names {
		items = append(items, envoycachetypes.ResourceWithTTL{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: name}})
	}
	return envoycache.NewResourcesWithTTL("", items).Items
}

// A set published through a filter is versioned exactly as the same set
// published directly: the version is a function of the published rows, not
// of the path that published them.
func TestEndpointSetVersionIsPathIndependent(t *testing.T) {
	rows := map[string]uint64{"a": 11, "b": 22, "c": 33}

	direct := endpointSetVersion(endpointSetHashes(edsSetVersionItems("a", "c"), map[string]uint64{"a": 11, "c": 33}, nil))
	filtered := endpointSetVersion(endpointSetHashes(edsSetVersionItems("a", "c"), rows, nil))
	require.Equal(t, direct, filtered, "dropping b must version {a, c} as if b had never been built")

	full := endpointSetVersion(endpointSetHashes(edsSetVersionItems("a", "b", "c"), rows, nil))
	require.NotEqual(t, full, filtered, "dropping b must move the version")
	require.Equal(t, full, endpointSetVersion(rows), "the full set keeps the XOR of its row hashes")
}

func TestEndpointSetVersionIsOrderIndependent(t *testing.T) {
	rows := map[string]uint64{"a": 11, "b": 22, "c": 33}
	v1 := endpointSetVersion(map[string]uint64{"a": rows["a"], "b": rows["b"], "c": rows["c"]})
	v2 := endpointSetVersion(map[string]uint64{"c": rows["c"], "a": rows["a"], "b": rows["b"]})
	require.Equal(t, v1, v2)
}

// Negative control from the randomized fixture: distinct contributions can
// collide as sets even when no two individual contributions are equal.
// This characterizes the XOR combiner, not a discovered production hash input.
func TestEndpointSetVersionDistinctContributionsCanCollide(t *testing.T) {
	before := endpointSetVersion(map[string]uint64{"cluster-3": 16})
	after := endpointSetVersion(map[string]uint64{"cluster-0": 18, "cluster-1": 20, "cluster-3": 22})
	require.Equal(t, before, after, "16 == 18^20^22; per-row distinctness is insufficient")
}

// A row that changes behind a filter still moves the filtered version, and a
// row that recovers its inputs restores the version it had.
func TestEndpointSetVersionFollowsRowHashes(t *testing.T) {
	items := edsSetVersionItems("a", "c")
	before := endpointSetVersion(endpointSetHashes(items, map[string]uint64{"a": 11, "b": 22, "c": 33}, nil))
	changed := endpointSetVersion(endpointSetHashes(items, map[string]uint64{"a": 12, "b": 22, "c": 33}, nil))
	require.NotEqual(t, before, changed, "a changed row behind the filter must move the version")
	restored := endpointSetVersion(endpointSetHashes(items, map[string]uint64{"a": 11, "b": 22, "c": 33}, nil))
	require.Equal(t, before, restored, "restoring the row must restore the version")
}

// Contribution precedence: a built row wins over a carried hash, a carried
// hash wins over the name hash, and anything else is versioned by name.
func TestEndpointSetHashesPrecedence(t *testing.T) {
	items := edsSetVersionItems("built", "carried", "synth")
	rows := map[string]uint64{"built": 1, "unpublished": 9}
	carried := func(name string) (uint64, bool) {
		switch name {
		case "built":
			return 100, true
		case "carried":
			return 200, true
		}
		return 0, false
	}

	got := endpointSetHashes(items, rows, carried)
	require.Equal(t, map[string]uint64{
		"built":   1,
		"carried": 200,
		"synth":   utils.HashString("synth"),
	}, got, "only published assignments contribute, each from its most authoritative source")

	withoutCarry := endpointSetHashes(items, rows, nil)
	require.Equal(t, utils.HashString("carried"), withoutCarry["carried"], "with nothing remembered a carried assignment is versioned by name")
	require.Equal(t, uint64(1), withoutCarry["built"])
}

// A carried assignment restored with the hash it was published under gives
// the composed set the version the directly built set would have.
func TestEndpointSetVersionCarryRestoresPublishedVersion(t *testing.T) {
	publishedRows := map[string]uint64{"old": 11, "other": 22}
	published := endpointSetVersion(endpointSetHashes(edsSetVersionItems("old", "other"), publishedRows, nil))

	// The candidate rebuilt only "other"; "old" is copied from the published
	// snapshot and remembered under its published contribution.
	lastPublished := func(name string) (uint64, bool) {
		h, ok := publishedRows[name]
		return h, ok
	}
	composed := endpointSetVersion(endpointSetHashes(edsSetVersionItems("old", "other"), map[string]uint64{"other": 22}, lastPublished))
	require.Equal(t, published, composed, "carrying old forward unchanged must not move the version")

	otherChanged := endpointSetVersion(endpointSetHashes(edsSetVersionItems("old", "other"), map[string]uint64{"other": 23}, lastPublished))
	require.NotEqual(t, published, otherChanged, "a rebuilt row still moves the composed version")
}

func TestVersionEndpointSetKeepsItems(t *testing.T) {
	res := envoycache.NewResourcesWithTTL("ignored", []envoycachetypes.ResourceWithTTL{
		{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: "a"}},
	})
	hashes := map[string]uint64{"a": 5}
	out := versionEndpointSet(res, hashes)
	require.Equal(t, endpointSetVersion(hashes), out.Version)
	require.Equal(t, res.Items, out.Items)
}

func TestPublishedEndpointHashesTracker(t *testing.T) {
	var nilTracker *publishedEndpointHashes
	nilTracker.record("client", map[string]uint64{"a": 1})
	require.Nil(t, nilTracker.lookup("client"), "a nil tracker remembers nothing")

	tracker := newPublishedEndpointHashes()
	require.Nil(t, tracker.lookup("client"), "nothing is remembered before the first publication")

	tracker.record("client", map[string]uint64{"a": 7})
	lookup := tracker.lookup("client")
	require.NotNil(t, lookup)
	h, ok := lookup("a")
	require.True(t, ok)
	require.Equal(t, uint64(7), h)
	_, ok = lookup("b")
	require.False(t, ok)
	require.Nil(t, tracker.lookup("other"), "clients are remembered separately")

	tracker.record("client", map[string]uint64{"a": 8})
	h, _ = tracker.lookup("client")("a")
	require.Equal(t, uint64(8), h, "a later publication replaces the remembered contributions")
	h, _ = lookup("a")
	require.Equal(t, uint64(7), h, "a lookup taken earlier keeps answering for its publication")
}
