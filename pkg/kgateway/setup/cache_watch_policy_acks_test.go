package setup

import (
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	discoveryv3 "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/status"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	kgwxds "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
)

// policyWithAcks returns a watch policy tracking ACKs and a channel of the
// clients it reports.
func policyWithAcks(t *testing.T) (*watchPolicy, chan string) {
	t.Helper()
	hasher := kgwxds.NewNodeRoleHasher()
	policy := newWatchPolicy(envoycache.NewSnapshotCache(true, hasher, nil), hasher, false, false)
	notified := make(chan string, 16)
	policy.NotifyAcks(func(client string) {
		select {
		case notified <- client:
		default:
		}
	})
	return policy, notified
}

func clusterResponse(t *testing.T, nonce, version string, clusters ...string) *discoveryv3.DiscoveryResponse {
	t.Helper()
	resp := &discoveryv3.DiscoveryResponse{TypeUrl: envoyresource.ClusterType, Nonce: nonce, VersionInfo: version}
	for _, name := range clusters {
		resource, err := utils.MessageToAny(&envoyclusterv3.Cluster{Name: name})
		require.NoError(t, err)
		resp.Resources = append(resp.Resources, resource)
	}
	return resp
}

// Every stream of a client that was sent the type must ACK; a NACK is not an
// ACK; a stream that leaves stops counting.
func TestWatchPolicyAcksCountEveryStreamOfTheClient(t *testing.T) {
	policy, notified := policyWithAcks(t)
	node := adsTestNode(t)
	subscribe := &discoveryv3.DiscoveryRequest{Node: node, TypeUrl: envoyresource.ClusterType}
	for _, stream := range []int64{1, 2} {
		require.NoError(t, policy.onStreamRequest(stream, subscribe))
		policy.onStreamResponse(t.Context(), stream, subscribe, clusterResponse(t, "n1", "1", "a"))
	}
	assert.False(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a"}), "sent, not yet ACKed")

	ack := &discoveryv3.DiscoveryRequest{Node: node, TypeUrl: envoyresource.ClusterType, VersionInfo: "1", ResponseNonce: "n1"}
	require.NoError(t, policy.onStreamRequest(1, ack))
	assert.Equal(t, adsTestNodeID, <-notified)
	assert.False(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a"}), "stream 2 has not ACKed")
	assert.Equal(t, map[int64]string{1: "1", 2: ""}, policy.AckedVersions(adsTestNodeID, envoyresource.ClusterType))

	nack := &discoveryv3.DiscoveryRequest{Node: node, TypeUrl: envoyresource.ClusterType, ResponseNonce: "n1", ErrorDetail: &status.Status{Message: "rejected"}}
	require.NoError(t, policy.onStreamRequest(2, nack))
	assert.False(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a"}), "a NACK is not an ACK")

	policy.onStreamClosed(2, node)
	assert.Equal(t, adsTestNodeID, <-notified, "a stream leaving is reported")
	assert.True(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a"}), "the stream left no longer has to ACK")
	assert.False(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a", "b"}), "b was never sent")
	assert.False(t, policy.AckedResources("other", envoyresource.ClusterType, []string{"a"}), "a client with no streams has acknowledged nothing")
}

// Without NotifyAcks no ACK is recorded: the stream counts as sent and
// unacknowledged.
func TestWatchPolicyTracksAcksOnlyWhenAsked(t *testing.T) {
	hasher := kgwxds.NewNodeRoleHasher()
	policy := newWatchPolicy(envoycache.NewSnapshotCache(true, hasher, nil), hasher, false, false)
	node := adsTestNode(t)
	subscribe := &discoveryv3.DiscoveryRequest{Node: node, TypeUrl: envoyresource.ClusterType}
	require.NoError(t, policy.onStreamRequest(1, subscribe))
	policy.onStreamResponse(t.Context(), 1, subscribe, clusterResponse(t, "n1", "1", "a"))
	require.NoError(t, policy.onStreamRequest(1, &discoveryv3.DiscoveryRequest{Node: node, TypeUrl: envoyresource.ClusterType, VersionInfo: "1", ResponseNonce: "n1"}))
	assert.False(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a"}))
	assert.Equal(t, map[int64]string{1: ""}, policy.AckedVersions(adsTestNodeID, envoyresource.ClusterType))
}

// Over a real ADS stream, in both modes: a cluster is acknowledged by the
// cluster response's ACK, and its endpoints by an endpoint response's ACK
// carrying its assignment.
func TestWatchPolicyAcksOverAnADSStream(t *testing.T) {
	for _, ordered := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "ordered"}[ordered], func(t *testing.T) {
			h := newPolicyHarness(t, ordered, true, true)
			policy, ok := h.cache.(*watchPolicy)
			require.True(t, ok)
			notified := make(chan string, 16)
			policy.NotifyAcks(func(client string) {
				select {
				case notified <- client:
				default:
				}
			})
			awaitAck := func() {
				t.Helper()
				select {
				case client := <-notified:
					require.Equal(t, adsTestNodeID, client)
				case <-time.After(2 * time.Second):
					t.Fatal("no ACK reported")
				}
			}

			h.setSnapshot(clusterEndpointSnapshot("1", "1", []string{"a"}, map[string]string{"a": "10.0.0.1"}))
			h.subscribe(envoyresource.ClusterType)
			clusters := h.receiveType(envoyresource.ClusterType, "1")
			assert.False(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a"}))
			h.ack(clusters)
			awaitAck()
			assert.True(t, policy.AckedResources(adsTestNodeID, envoyresource.ClusterType, []string{"a"}))

			h.subscribe(envoyresource.EndpointType, "a")
			endpoints := h.receiveType(envoyresource.EndpointType, "1")
			assert.False(t, policy.AckedResources(adsTestNodeID, envoyresource.EndpointType, []string{"a"}))
			h.ackNamed(endpoints, "a")
			awaitAck()
			assert.True(t, policy.AckedResources(adsTestNodeID, envoyresource.EndpointType, []string{"a"}))
			for _, version := range policy.AckedVersions(adsTestNodeID, envoyresource.EndpointType) {
				assert.Equal(t, "1", version)
			}
		})
	}
}
