package proxy_syncer

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// RF-024: a backend whose translation yields no cluster is dropped from the
// per-client CDS collection without an errored record. The only such paths in
// TranslateBackend are a group/kind with no contributed translator and a
// contributed BackendInit with a nil InitEnvoyBackend; the error returned with
// the nil cluster is discarded in NewPerClientEnvoyClusters. Every other
// failure returns a named blackhole cluster with an error, which the snapshot
// transform records as errored and exempts from the publication gate.
//
// The built-in backend plugins all set InitEnvoyBackend, so this fixture uses
// a synthetic plugin registration. It pins the current silent-drop behavior;
// the action item is to record such a backend as errored or reject it at
// collection time. When that lands, this test must be inverted, not deleted.
func TestNilBackendTranslationIsDroppedNotErrored(t *testing.T) {
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

	// Two of four backends produce a row: the healthy cluster and the errored
	// blackhole. The nil-translation backends produce nothing at all.
	var rows []uccWithCluster
	require.Eventuallyf(t, func() bool {
		rows = clusters.FetchClustersForClient(krt.TestingDummyContext{}, ucc)
		return len(rows) == 2
	}, 5*time.Second, 10*time.Millisecond, "expected exactly two rows, last: %d", len(rows))

	byName := map[string]uccWithCluster{}
	for _, row := range rows {
		byName[row.Name] = row
	}
	require.Contains(t, byName, healthy.ClusterName(), "healthy backend must produce a cluster row")
	require.NoError(t, byName[healthy.ClusterName()].Error)
	require.Contains(t, byName, invalid.ClusterName(), "pre-existing backend errors must still produce a named errored row")
	require.Error(t, byName[invalid.ClusterName()].Error, "errored row carries its error so snapshotPerClient records it as exempt")

	for _, dropped := range []*ir.BackendObjectIR{noInit, unregistered} {
		require.NotContains(t, byName, dropped.ClusterName(),
			"RF-024: %s is currently dropped without a row; if it now appears, the silent path was fixed and this test must be inverted", dropped.ClusterName())
		c, err := translator.TranslateBackend(ctx, krt.TestingDummyContext{}, ucc, dropped)
		require.Nil(t, c, "TranslateBackend returns no cluster for %s", dropped.ClusterName())
		require.Error(t, err, "TranslateBackend does return an error for %s; NewPerClientEnvoyClusters discards it with the nil cluster", dropped.ClusterName())
	}

	// The transform's errored set is derived from rows with a non-nil Error.
	// A dropped backend therefore appears in neither CDS nor the exempt set,
	// which is exactly the nonexempt-missing shape that RF-003 withholds on.
	var errored []string
	for _, row := range rows {
		if row.Error != nil {
			errored = append(errored, row.Name)
		}
	}
	require.Equal(t, []string{invalid.ClusterName()}, errored)
	published := map[string]envoycachetypes.ResourceWithTTL{}
	for _, row := range rows {
		if row.Error == nil {
			published[row.Name] = envoycachetypes.ResourceWithTTL{Resource: row.Cluster}
		}
	}
	referenced := map[string]struct{}{
		healthy.ClusterName(): {}, invalid.ClusterName(): {}, noInit.ClusterName(): {}, unregistered.ClusterName(): {},
	}
	want := []string{noInit.ClusterName(), unregistered.ClusterName()}
	slices.Sort(want)
	require.Equal(t, want, findMissingReferencedClusters(referenced, published, errored),
		"dropped backends are nonexempt missing references: they defer first publication and hold warm flips")
}
