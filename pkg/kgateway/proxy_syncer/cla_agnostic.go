package proxy_syncer

import (
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	krtutil "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// NewAgnosticEnvoyEndpoints builds each backend's ClusterLoadAssignment exactly
// once, with no dependency on the connected-client set. This is the EDS half of
// the locality-agnostic xDS graph: with pod locality disabled, endpoint
// prioritization yields uniform priorities for every client, so the CLA is
// identical across replicas and a single translation per backend is valid for
// all gateways. A client connecting or disconnecting triggers zero CLA
// re-translation.
func NewAgnosticEnvoyEndpoints(
	krtopts krtutil.KrtOptions,
	kgatewayEndpoints krt.Collection[ir.EndpointsForBackend],
	translateEndpoints func(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient, ep ir.EndpointsForBackend) (*envoyendpointv3.ClusterLoadAssignment, uint64),
) krt.Collection[UccWithEndpoints] {
	return krt.NewCollection(kgatewayEndpoints, func(kctx krt.HandlerContext, ep ir.EndpointsForBackend) *UccWithEndpoints {
		cla, additionalHash := translateEndpoints(kctx, agnosticUCC, ep)
		return &UccWithEndpoints{
			Client:        agnosticUCC,
			Endpoints:     cla,
			EndpointsHash: ep.LbEpsEqualityHash ^ additionalHash,
			endpointsName: ep.ResourceName(),
		}
	}, krtopts.ToOptions("AgnosticEnvoyEndpoints")...)
}
