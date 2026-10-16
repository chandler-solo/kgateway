package proxy_syncer

import (
	"context"
	"errors"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// RF-024: unsupported backend translations now produce named errored rows.
// Preserve the publication-gate regression: these backends must be excluded
// from CDS and exempt from readiness checks, so they cannot starve first
// publication or hold a warm route flip indefinitely.
func TestUnsupportedBackendTranslationIsExemptFromPublicationGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	serviceGK := schema.GroupKind{Group: "", Kind: "Service"}
	noInitGK := schema.GroupKind{Group: "example.test", Kind: "NoInitBackend"}
	translator := &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			serviceGK: {
				InitEnvoyBackend: func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
			// Contributed backends collection, no cluster initializer.
			noInitGK: {},
		},
	}

	makeBackend := func(gk schema.GroupKind, name string, errs ...error) *ir.BackendObjectIR {
		b := ir.NewBackendObjectIR(ir.ObjectSource{Group: gk.Group, Kind: gk.Kind, Namespace: "default", Name: name}, 443, "", "")
		b.Errors = errs
		return &b
	}
	healthy := makeBackend(serviceGK, "healthy")
	invalid := makeBackend(serviceGK, "invalid", errors.New("synthetic pre-existing translation error"))
	noInit := makeBackend(noInitGK, "no-init")
	unregistered := makeBackend(schema.GroupKind{Group: "example.test", Kind: "Unregistered"}, "unregistered")

	ucc := ir.NewUniquelyConnectedClient("role-rf024", "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection(nil, []ir.UniquelyConnectedClient{ucc}, krtopts.ToOptions("UniqueClients")...)
	finalBackends := krt.NewStaticCollection(nil, []*ir.BackendObjectIR{healthy, invalid, noInit, unregistered}, krtopts.ToOptions("FinalBackends")...)
	clusters := NewPerClientEnvoyClusters(ctx, krtopts, translator, finalBackends, uccs)

	var snapshot *clustersWithErrors
	require.Eventually(t, func() bool {
		snapshot = clusters.perClient.GetKey(ucc.ResourceName())
		return snapshot != nil && len(snapshot.erroredClusters) == 3
	}, 5*time.Second, 10*time.Millisecond, "all unsupported and invalid backends must reach the errored set")
	require.Len(t, snapshot.clusters.Items, 1)
	require.Contains(t, snapshot.clusters.Items, healthy.ClusterName())

	for _, unsupported := range []*ir.BackendObjectIR{noInit, unregistered} {
		base := translator.TranslateBackendBase(krt.TestingDummyContext{}, ctx, unsupported)
		require.NotNil(t, base.Cluster, "unsupported backend must produce a named blackhole cluster")
		require.Equal(t, unsupported.ClusterName(), base.Cluster.GetName())
		require.Error(t, base.Error, "unsupported backend must retain its translation error")
	}

	require.ElementsMatch(t, []string{invalid.ClusterName(), noInit.ClusterName(), unregistered.ClusterName()}, snapshot.erroredClusters)
	referenced := map[string]struct{}{
		healthy.ClusterName(): {}, invalid.ClusterName(): {}, noInit.ClusterName(): {}, unregistered.ClusterName(): {},
	}
	require.Empty(t, findMissingReferencedClusters(referenced, snapshot.clusters.Items, snapshot.erroredClusters),
		"unsupported backends must not defer first publication or hold warm route flips")
}
