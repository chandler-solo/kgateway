package proxy_syncer

import (
	"errors"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/stretchr/testify/require"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/endpoints"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

// TestBaseClusterVersion_InlineCLAReflectsEndpointChanges is a regression test
// for the stale-CLA bug. When the cluster type supports an inline CLA, the
// per-client CLA is built from BaseCluster.EndpointInputs (not from the cluster
// proto), so the base version must change when endpoints change — otherwise
// KRT does not re-publish the base, the delta collection does not recompute,
// and clients stay pinned to a stale LoadAssignment.
//
// The bug surfaces on non-EDS backends (e.g. ServiceEntry-style STRICT_DNS)
// where addresses live in EndpointsForBackend rather than the cluster proto.
func TestBaseClusterVersion_InlineCLAReflectsEndpointChanges(t *testing.T) {
	cluster := staticInlineCLACluster()
	us := ir.BackendObjectIR{
		ObjectSource: ir.ObjectSource{Namespace: "ns", Name: "svc"},
	}

	withNoEps := &irtranslator.BaseCluster{
		Cluster:           cluster,
		EndpointInputs:    &endpoints.EndpointsInputs{EndpointsForBackend: *ir.NewEndpointsForBackend(us)},
		SupportsInlineCLA: true,
	}
	v0 := baseClusterVersion(withNoEps)

	withOneEp := *withNoEps
	epsOne := *ir.NewEndpointsForBackend(us)
	epsOne.Add(ir.PodLocality{Region: "r1"}, ir.EndpointWithMd{
		LbEndpoint: lbEndpointPipe("a"),
	})
	withOneEp.EndpointInputs = &endpoints.EndpointsInputs{EndpointsForBackend: epsOne}
	v1 := baseClusterVersion(&withOneEp)

	withTwoEps := *withNoEps
	epsTwo := *ir.NewEndpointsForBackend(us)
	epsTwo.Add(ir.PodLocality{Region: "r1"}, ir.EndpointWithMd{
		LbEndpoint: lbEndpointPipe("a"),
	})
	epsTwo.Add(ir.PodLocality{Region: "r2"}, ir.EndpointWithMd{
		LbEndpoint: lbEndpointPipe("b"),
	})
	withTwoEps.EndpointInputs = &endpoints.EndpointsInputs{EndpointsForBackend: epsTwo}
	v2 := baseClusterVersion(&withTwoEps)

	require.NotEqual(t, v0, v1, "adding an endpoint to an inline-CLA backend must change the base version")
	require.NotEqual(t, v1, v2, "adding another endpoint must change the base version again")
}

// TestBaseClusterVersion_EDSStableAcrossEndpointChanges asserts that endpoint
// churn does NOT churn the base version for EDS clusters. EDS endpoints feed a
// separate pipeline and are not used by ApplyPerClient; if the version flipped
// here we would needlessly recompute every delta on every endpoint change.
func TestBaseClusterVersion_EDSStableAcrossEndpointChanges(t *testing.T) {
	cluster := &envoyclusterv3.Cluster{
		Name: "eds-cluster",
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
			Type: envoyclusterv3.Cluster_EDS,
		},
	}
	us := ir.BackendObjectIR{
		ObjectSource: ir.ObjectSource{Namespace: "ns", Name: "svc"},
	}

	v0 := baseClusterVersion(&irtranslator.BaseCluster{
		Cluster:           cluster,
		EndpointInputs:    &endpoints.EndpointsInputs{EndpointsForBackend: *ir.NewEndpointsForBackend(us)},
		SupportsInlineCLA: false,
	})

	withEp := *ir.NewEndpointsForBackend(us)
	withEp.Add(ir.PodLocality{Region: "r1"}, ir.EndpointWithMd{
		LbEndpoint: lbEndpointPipe("a"),
	})
	v1 := baseClusterVersion(&irtranslator.BaseCluster{
		Cluster:           cluster,
		EndpointInputs:    &endpoints.EndpointsInputs{EndpointsForBackend: withEp},
		SupportsInlineCLA: false,
	})

	require.Equal(t, v0, v1, "EDS base version must not depend on endpoint changes")
}

// TestBaseClusterVersion_ErroredHasZeroVersion confirms that errored bases
// produce a stable zero version (matching the original HashProto-only path
// where errored bases were skipped). Two errored bases compare equal so KRT
// does not churn on transient error reasons.
func TestBaseClusterVersion_ErroredHasZeroVersion(t *testing.T) {
	cluster := staticInlineCLACluster()
	v := baseClusterVersion(&irtranslator.BaseCluster{
		Cluster: cluster,
		Error:   errors.New("boom"),
	})
	require.Equal(t, uint64(0), v)
}

// TestBaseClusterVersion_ClusterChangesAreReflected sanity-checks that a
// different cluster proto produces a different version regardless of endpoints.
func TestBaseClusterVersion_ClusterChangesAreReflected(t *testing.T) {
	us := ir.BackendObjectIR{ObjectSource: ir.ObjectSource{Namespace: "ns", Name: "svc"}}
	epsIn := &endpoints.EndpointsInputs{EndpointsForBackend: *ir.NewEndpointsForBackend(us)}

	clusterA := staticInlineCLACluster()
	clusterB := staticInlineCLACluster()
	clusterB.Name = "different-name"

	vA := baseClusterVersion(&irtranslator.BaseCluster{Cluster: clusterA, EndpointInputs: epsIn, SupportsInlineCLA: true})
	vB := baseClusterVersion(&irtranslator.BaseCluster{Cluster: clusterB, EndpointInputs: epsIn, SupportsInlineCLA: true})
	require.NotEqual(t, vA, vB, "different cluster proto must produce a different version")
}

func staticInlineCLACluster() *envoyclusterv3.Cluster {
	return &envoyclusterv3.Cluster{
		Name: "inline-cla-cluster",
		ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
			Type: envoyclusterv3.Cluster_STRICT_DNS,
		},
	}
}

func lbEndpointPipe(path string) *envoyendpointv3.LbEndpoint {
	return &envoyendpointv3.LbEndpoint{
		HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{
			Endpoint: &envoyendpointv3.Endpoint{
				Address: &envoycorev3.Address{
					Address: &envoycorev3.Address_Pipe{Pipe: &envoycorev3.Pipe{Path: path}},
				},
			},
		},
	}
}
