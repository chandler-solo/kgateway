package proxy_syncer

import (
	"context"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	sdk "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// TestNewPerClientEnvoyClusters_SparseOverlayWiring exercises the real KRT
// wiring end-to-end (base collection -> deltas many-collection -> index ->
// FetchClustersForClient merge) rather than the static-collection test helpers.
// It pins the headline behaviors of the base+overlay split:
//
//   - A UCC the overlay declines sees the shared base proto (no delta emitted).
//   - A UCC the overlay matches sees a distinct per-client proto carrying the
//     mutation, while the base proto stays pristine.
//   - Two UCCs whose overlay produces byte-identical clusters share one interned
//     delta proto (allocation dedup), so equivalent clients do not each clone.
func TestNewPerClientEnvoyClusters_SparseOverlayWiring(t *testing.T) {
	ctx := t.Context()
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	backendGK := schema.GroupKind{Group: "group", Kind: "kind"}
	overlayGK := schema.GroupKind{Group: "test", Kind: "Overlay"}

	translator := &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			backendGK: {
				InitEnvoyBackend: func(ctx context.Context, in ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
		},
		ContributedPolicies: map[schema.GroupKind]sdk.PolicyPlugin{
			overlayGK: {
				// Self-gating overlay: only clients labeled match=yes get a
				// mutation; everyone else takes the fast path (nil => share base).
				PerClientClusterOverlay: func(kctx krt.HandlerContext, ctx context.Context, ucc ir.UniquelyConnectedClient, in ir.BackendObjectIR) *sdk.ClusterOverlay {
					if ucc.Labels["match"] != "yes" {
						return nil
					}
					return &sdk.ClusterOverlay{
						Mutate: func(out *envoyclusterv3.Cluster) {
							out.OutlierDetection = &envoyclusterv3.OutlierDetection{}
						},
					}
				},
			},
		},
	}

	backend := ir.NewBackendObjectIR(ir.ObjectSource{Group: "group", Kind: "kind", Namespace: "ns", Name: "svc"}, 80, "", "")
	backend.AttachedPolicies = ir.AttachedPolicies{Policies: map[schema.GroupKind][]ir.PolicyAtt{}}
	finalBackends := krt.NewStaticCollection(nil, []*ir.BackendObjectIR{&backend}, krtopts.ToOptions("FinalBackends")...)

	// matchA and matchB produce byte-identical overlaid clusters; other is declined.
	matchA := ir.NewUniquelyConnectedClient("a", "ns", map[string]string{"match": "yes", "id": "a"}, ir.PodLocality{})
	matchB := ir.NewUniquelyConnectedClient("b", "ns", map[string]string{"match": "yes", "id": "b"}, ir.PodLocality{})
	other := ir.NewUniquelyConnectedClient("c", "ns", map[string]string{"match": "no"}, ir.PodLocality{})
	uccs := krt.NewStaticCollection(nil, []ir.UniquelyConnectedClient{matchA, matchB, other}, krtopts.ToOptions("UCCs")...)

	pcc := NewPerClientEnvoyClusters(ctx, krtopts, translator, finalBackends, uccs)
	require.Eventually(t, pcc.HasSynced, time.Second, 10*time.Millisecond)

	var gotA, gotB, gotOther []uccWithCluster
	require.Eventually(t, func() bool {
		gotA = pcc.FetchClustersForClient(krt.TestingDummyContext{}, matchA)
		gotB = pcc.FetchClustersForClient(krt.TestingDummyContext{}, matchB)
		gotOther = pcc.FetchClustersForClient(krt.TestingDummyContext{}, other)
		return len(gotA) == 1 && len(gotB) == 1 && len(gotOther) == 1
	}, 2*time.Second, 20*time.Millisecond)

	// Declined client: shared base proto, no mutation.
	require.NoError(t, gotOther[0].Error)
	assert.Nil(t, gotOther[0].Cluster.GetOutlierDetection(), "declined client must see the un-overlaid base")

	// Matched client: distinct proto carrying the overlay mutation.
	require.NoError(t, gotA[0].Error)
	assert.NotNil(t, gotA[0].Cluster.GetOutlierDetection(), "matched client must see the overlay mutation")
	assert.NotSame(t, gotOther[0].Cluster, gotA[0].Cluster, "matched client must not share the base proto")

	// Interning: equivalent matched clients share one delta proto.
	assert.Same(t, gotA[0].Cluster, gotB[0].Cluster,
		"clients whose overlay output is byte-identical must share one interned proto")
}

// TestNewPerClientEnvoyClusters_BackendLabelChangeReachesOverlay reproduces the
// waypoint ingress-use-waypoint regression: overlay plugins self-determine
// applicability from backend state (here a label on the backend's Obj) that can
// change WITHOUT changing the translated base proto, the generation, or the
// error. A label-only edit must still re-emit the base row — if baseEnvoyCluster
// equality swallows it, the deltas collection keeps evaluating the overlay
// against the stale Backend and the per-client mutation never materializes
// (TestKgatewayWaypoint/WaypointIngress/TestIngressHTTPRouteServiceLabel).
func TestNewPerClientEnvoyClusters_BackendLabelChangeReachesOverlay(t *testing.T) {
	ctx := t.Context()
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	backendGK := schema.GroupKind{Group: "group", Kind: "kind"}
	overlayGK := schema.GroupKind{Group: "test", Kind: "Overlay"}

	translator := &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			backendGK: {
				InitEnvoyBackend: func(ctx context.Context, in ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
		},
		ContributedPolicies: map[schema.GroupKind]sdk.PolicyPlugin{
			overlayGK: {
				// Gate on the BACKEND's label, like the waypoint plugin's
				// istio.io/ingress-use-waypoint Service-label check.
				PerClientClusterOverlay: func(kctx krt.HandlerContext, ctx context.Context, ucc ir.UniquelyConnectedClient, in ir.BackendObjectIR) *sdk.ClusterOverlay {
					if in.Obj.GetLabels()["use-overlay"] != "true" {
						return nil
					}
					return &sdk.ClusterOverlay{
						Mutate: func(out *envoyclusterv3.Cluster) {
							out.OutlierDetection = &envoyclusterv3.OutlierDetection{}
						},
					}
				},
			},
		},
	}

	mkBackend := func(labels map[string]string, resourceVersion string) *ir.BackendObjectIR {
		backend := ir.NewBackendObjectIR(ir.ObjectSource{Group: "group", Kind: "kind", Namespace: "ns", Name: "svc"}, 80, "", "")
		backend.AttachedPolicies = ir.AttachedPolicies{Policies: map[schema.GroupKind][]ir.PolicyAtt{}}
		backend.Obj = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "svc",
				Namespace:       "ns",
				ResourceVersion: resourceVersion,
				Labels:          labels,
			},
		}
		return &backend
	}

	finalBackends := krt.NewStaticCollection(nil, []*ir.BackendObjectIR{mkBackend(nil, "1")}, krtopts.ToOptions("FinalBackends")...)

	client := ir.NewUniquelyConnectedClient("a", "ns", map[string]string{"id": "a"}, ir.PodLocality{})
	uccs := krt.NewStaticCollection(nil, []ir.UniquelyConnectedClient{client}, krtopts.ToOptions("UCCs")...)

	pcc := NewPerClientEnvoyClusters(ctx, krtopts, translator, finalBackends, uccs)
	require.Eventually(t, pcc.HasSynced, time.Second, 10*time.Millisecond)

	var got []uccWithCluster
	require.Eventually(t, func() bool {
		got = pcc.FetchClustersForClient(krt.TestingDummyContext{}, client)
		return len(got) == 1
	}, 2*time.Second, 20*time.Millisecond)
	require.NoError(t, got[0].Error)
	require.Nil(t, got[0].Cluster.GetOutlierDetection(), "unlabeled backend must see the un-overlaid base")

	// Label the backend. The translated base proto, the generation, and the
	// error are all unchanged — only the Backend the overlay reads differs.
	finalBackends.UpdateObject(mkBackend(map[string]string{"use-overlay": "true"}, "2"))

	assert.Eventually(t, func() bool {
		got = pcc.FetchClustersForClient(krt.TestingDummyContext{}, client)
		return len(got) == 1 && got[0].Error == nil && got[0].Cluster.GetOutlierDetection() != nil
	}, 2*time.Second, 20*time.Millisecond, "backend label change must re-run the overlay and materialize the per-client delta")

	// And the reverse: removing the label must drop the delta again.
	finalBackends.UpdateObject(mkBackend(nil, "3"))

	assert.Eventually(t, func() bool {
		got = pcc.FetchClustersForClient(krt.TestingDummyContext{}, client)
		return len(got) == 1 && got[0].Error == nil && got[0].Cluster.GetOutlierDetection() == nil
	}, 2*time.Second, 20*time.Millisecond, "label removal must revert the client to the shared base")
}
