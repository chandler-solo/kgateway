package proxy_syncer

import (
	"os"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/stretchr/testify/require"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

// TestMain forces the shared-proto tripwire on for every test in this package.
// The integration tests here run the real producer collections
// (NewPerClientEnvoyClusters, NewPerClientEnvoyEndpoints) through
// snapshotPerClient, so with the flag on, any code path that mutates a shared
// proto between creation and snapshot assembly panics in CI instead of
// silently corrupting sibling clients. Test fixtures that construct rows
// directly are unaffected: their assertProtoHash is 0, which verification
// skips.
func TestMain(m *testing.M) {
	assertSharedProtos = true
	os.Exit(m.Run())
}

func TestVerifySharedProtoHash_PanicsOnMutation(t *testing.T) {
	cluster := &envoyclusterv3.Cluster{Name: "shared"}
	captured := utils.HashProto(cluster)

	require.NotPanics(t, func() {
		verifySharedProtoHash("cluster", cluster.GetName(), "ucc", cluster, captured)
	}, "an unmutated proto must pass verification")

	// The canonical nasty mistake: a post-fetch mutation of a shared proto.
	cluster.OutlierDetection = &envoyclusterv3.OutlierDetection{}

	require.Panics(t, func() {
		verifySharedProtoHash("cluster", cluster.GetName(), "ucc", cluster, captured)
	}, "a mutated shared proto must trip the assertion")
}

func TestVerifySharedProtoHash_SkipsUncapturedRows(t *testing.T) {
	cluster := &envoyclusterv3.Cluster{Name: "fixture"}
	cluster.OutlierDetection = &envoyclusterv3.OutlierDetection{}

	require.NotPanics(t, func() {
		verifySharedProtoHash("cluster", cluster.GetName(), "ucc", cluster, 0)
	}, "captured hash 0 means the row was never captured (flag off or test fixture) and must be skipped")
}

func TestCaptureSharedProtoHash_RespectsFlag(t *testing.T) {
	cluster := &envoyclusterv3.Cluster{Name: "c"}

	require.Equal(t, utils.HashProto(cluster), captureSharedProtoHash(cluster),
		"capture must return the content hash while assertions are enabled (TestMain)")
	require.Equal(t, utils.HashProto(cluster), sharedProtoAssertValue(utils.HashProto(cluster)))

	assertSharedProtos = false
	t.Cleanup(func() { assertSharedProtos = true })
	require.Zero(t, captureSharedProtoHash(cluster), "capture must be free when assertions are disabled")
	require.Zero(t, sharedProtoAssertValue(42))
}
