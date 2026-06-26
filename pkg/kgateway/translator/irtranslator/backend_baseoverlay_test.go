package irtranslator_test

import (
	"context"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	sdk "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

// perClientGK is the group/kind used by the synthetic per-client policy plugin below.
var perClientGK = schema.GroupKind{Group: "group", Kind: "kind"}

// newOverlayTestTranslator builds a translator with an EDS backend and a per-client policy plugin
// that sets OutlierDetection only for clients labeled version=canary. This mirrors how the real
// destination-rule plugin mutates a cluster per uniquely-connected-client.
func newOverlayTestTranslator() *irtranslator.BackendTranslator {
	return &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			perClientGK: {
				InitEnvoyBackend: func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
		},
		ContributedPolicies: map[schema.GroupKind]sdk.PolicyPlugin{
			perClientGK: {
				PerClientProcessBackend: func(_ krt.HandlerContext, _ context.Context, ucc ir.UniquelyConnectedClient, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) {
					if ucc.Labels["version"] == "canary" {
						out.OutlierDetection = &envoyclusterv3.OutlierDetection{}
					}
				},
			},
		},
	}
}

func overlayTestBackend() *ir.BackendObjectIR {
	b := ir.NewBackendObjectIR(ir.ObjectSource{
		Group:     "group",
		Kind:      "kind",
		Namespace: "default",
		Name:      "b1",
	}, 8080, "", "")
	return &b
}

// TestPerClientOverlayDoesNotMutateBase asserts the base cluster is never mutated by the per-client
// overlay (copy-on-write), so concurrent clients cannot see each other's mutations.
func TestPerClientOverlayDoesNotMutateBase(t *testing.T) {
	tr := newOverlayTestTranslator()
	backend := overlayTestBackend()
	var kctx krt.TestingDummyContext

	base := tr.TranslateBackendBase(context.Background(), backend)
	require.NotNil(t, base)
	require.NoError(t, base.Err)
	require.Nil(t, base.Cluster.GetOutlierDetection(), "base must not carry per-client overlay state")

	canary := ir.NewUniquelyConnectedClient("role", "default", map[string]string{"version": "canary"}, ir.PodLocality{})
	stable := ir.NewUniquelyConnectedClient("role", "default", map[string]string{"version": "stable"}, ir.PodLocality{})

	cCanary, err := tr.ApplyPerClientOverlay(context.Background(), kctx, canary, backend, base)
	require.NoError(t, err)
	cStable, err := tr.ApplyPerClientOverlay(context.Background(), kctx, stable, backend, base)
	require.NoError(t, err)

	assert.NotNil(t, cCanary.GetOutlierDetection(), "canary client should get the overlay")
	assert.Nil(t, cStable.GetOutlierDetection(), "stable client should not get the overlay")
	assert.Nil(t, base.Cluster.GetOutlierDetection(), "base must remain unmodified after overlays")
	assert.NotSame(t, base.Cluster, cCanary, "overlay must return a distinct proto, not the shared base")
}

// TestPerClientOverlayFastPathSharesBase asserts that when no per-client work applies (no per-client
// plugins, EDS cluster, non-strict mode) the overlay returns the shared base proto without cloning.
func TestPerClientOverlayFastPathSharesBase(t *testing.T) {
	tr := &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			perClientGK: {
				InitEnvoyBackend: func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
		},
		ContributedPolicies: map[schema.GroupKind]sdk.PolicyPlugin{},
	}
	backend := overlayTestBackend()
	var kctx krt.TestingDummyContext

	base := tr.TranslateBackendBase(context.Background(), backend)
	require.NotNil(t, base)

	ucc := ir.NewUniquelyConnectedClient("role", "default", nil, ir.PodLocality{})
	c, err := tr.ApplyPerClientOverlay(context.Background(), kctx, ucc, backend, base)
	require.NoError(t, err)
	assert.Same(t, base.Cluster, c, "fast path should reuse the shared base proto")
}

// TestTranslateBackendMatchesBaseThenOverlay asserts the TranslateBackend wrapper produces the same
// result as base translation followed by the per-client overlay.
func TestTranslateBackendMatchesBaseThenOverlay(t *testing.T) {
	tr := newOverlayTestTranslator()
	backend := overlayTestBackend()
	var kctx krt.TestingDummyContext
	canary := ir.NewUniquelyConnectedClient("role", "default", map[string]string{"version": "canary"}, ir.PodLocality{})

	wrapped, err := tr.TranslateBackend(context.Background(), kctx, canary, backend)
	require.NoError(t, err)

	base := tr.TranslateBackendBase(context.Background(), backend)
	require.NotNil(t, base)
	overlay, err := tr.ApplyPerClientOverlay(context.Background(), kctx, canary, backend, base)
	require.NoError(t, err)

	assert.True(t, proto.Equal(wrapped, overlay), "wrapper output must equal base->overlay output")
}
