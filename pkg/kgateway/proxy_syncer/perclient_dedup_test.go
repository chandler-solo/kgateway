package proxy_syncer

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/stretchr/testify/require"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// These tests pin the per-invocation translation dedup by client equivalence
// class: cluster translation depends on the client only through
// (Namespace, Labels), endpoint translation through
// (Namespace, Labels, Locality), so clients sharing a class must be served by
// ONE translation per transform run. The dedup is scoped to a single
// invocation (the fetched inputs are constant within it), so it needs no
// cross-run invalidation.

func dedupTestTranslator(calls *atomic.Int64) *irtranslator.BackendTranslator {
	return &irtranslator.BackendTranslator{
		ContributedBackends: map[schema.GroupKind]ir.BackendInit{
			{Group: "", Kind: "Service"}: {
				InitEnvoyBackend: func(_ context.Context, _ ir.BackendObjectIR, out *envoyclusterv3.Cluster) *ir.EndpointsForBackend {
					calls.Add(1)
					out.ClusterDiscoveryType = &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}
					return nil
				},
			},
		},
	}
}

func dedupTestBackend(name string) *ir.BackendObjectIR {
	b := ir.NewBackendObjectIR(ir.ObjectSource{
		Group:     "",
		Kind:      "Service",
		Namespace: "default",
		Name:      name,
	}, 443, "")
	return &b
}

func dedupTestClient(role string, labels map[string]string, locality ir.PodLocality) ir.UniqlyConnectedClient {
	return ir.NewUniqlyConnectedClient(role, "ns", labels, locality)
}

// Clients sharing (Namespace, Labels) — even across roles — are one CDS
// translation class: each backend translates once per class, every client
// still gets its row, and same-class rows share the cluster version.
func TestPerClientClusters_DedupsTranslationByClass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	sharedLabels := map[string]string{"app": "gw", "pod-template-hash": "abc"}
	a1 := dedupTestClient("role-a1", sharedLabels, ir.PodLocality{})
	a2 := dedupTestClient("role-a2", sharedLabels, ir.PodLocality{})
	b := dedupTestClient("role-b", map[string]string{"app": "gw", "pod-template-hash": "xyz"}, ir.PodLocality{})

	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{a1, a2, b}, krtopts.ToOptions("UniqueClients")...)
	finalBackends := krt.NewStaticCollection(nil,
		[]*ir.BackendObjectIR{dedupTestBackend("b1"), dedupTestBackend("b2")},
		krtopts.ToOptions("FinalBackends")...)

	var calls atomic.Int64
	clusters := NewPerClientEnvoyClusters(ctx, krtopts, dedupTestTranslator(&calls), finalBackends, uccs)

	for _, ucc := range []ir.UniqlyConnectedClient{a1, a2, b} {
		require.Eventuallyf(t, func() bool {
			return len(clusters.FetchClustersForClient(krt.TestingDummyContext{}, ucc)) == 2
		}, 5*time.Second, 10*time.Millisecond, "client %s never got its clusters", ucc.ResourceName())
	}

	// 2 backends x 2 classes ({a1,a2}, {b}) = 4 translations, not 2x3=6.
	require.EqualValues(t, 4, calls.Load(),
		"translation must run once per (backend, class), not once per (backend, client)")

	// Same-class rows share the translation output.
	a1Rows := clusters.FetchClustersForClient(krt.TestingDummyContext{}, a1)
	a2Rows := clusters.FetchClustersForClient(krt.TestingDummyContext{}, a2)
	versions := map[string]uint64{}
	for _, r := range a1Rows {
		versions[r.Name] = r.ClusterVersion
	}
	for _, r := range a2Rows {
		require.Equalf(t, versions[r.Name], r.ClusterVersion,
			"same-class clients must share cluster versions for %s", r.Name)
	}
}

// Clients differing only in Locality share a CDS class (locality does not feed
// cluster output) but are distinct EDS classes (locality drives priority
// ordering).
func TestPerClientEndpoints_DedupsTranslationByClassIncludingLocality(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	labels := map[string]string{"app": "gw"}
	east := dedupTestClient("role-a", labels, ir.PodLocality{Region: "east"})
	eastTwin := dedupTestClient("role-b", labels, ir.PodLocality{Region: "east"})
	west := dedupTestClient("role-c", labels, ir.PodLocality{Region: "west"})

	uccs := krt.NewStaticCollection(nil, []ir.UniqlyConnectedClient{east, eastTwin, west}, krtopts.ToOptions("UniqueClients")...)
	eps := krt.NewStaticCollection(nil, []ir.EndpointsForBackend{{
		ClusterName:       "cluster-a",
		LbEpsEqualityHash: 7,
	}}, krtopts.ToOptions("Endpoints")...)

	var calls atomic.Int64
	translate := func(_ krt.HandlerContext, ucc ir.UniqlyConnectedClient, ep ir.EndpointsForBackend) (*envoyendpointv3.ClusterLoadAssignment, uint64) {
		calls.Add(1)
		return &envoyendpointv3.ClusterLoadAssignment{ClusterName: ep.ClusterName}, 0
	}
	perClient := NewPerClientEnvoyEndpoints(krtopts, uccs, eps, translate)

	for _, ucc := range []ir.UniqlyConnectedClient{east, eastTwin, west} {
		require.Eventuallyf(t, func() bool {
			return len(perClient.FetchEndpointsForClient(krt.TestingDummyContext{}, ucc)) == 1
		}, 5*time.Second, 10*time.Millisecond, "client %s never got its CLA", ucc.ResourceName())
	}

	// 1 endpoint set x 2 classes ({east, eastTwin}, {west}) = 2 translations.
	require.EqualValues(t, 2, calls.Load(),
		"endpoint translation must run once per (endpoints, class) with locality in the class key")
}
