package proxy_syncer

import (
	"fmt"

	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	krtutil "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

type UccWithEndpoints struct {
	Client ir.UniqlyConnectedClient
	// +krtEqualsTodo compare load assignments when equality matters
	Endpoints     *envoyendpointv3.ClusterLoadAssignment
	EndpointsHash uint64
	endpointsName string
}

func (c UccWithEndpoints) ResourceName() string {
	return fmt.Sprintf("%s/%s", c.Client.ResourceName(), c.endpointsName)
}

func (c UccWithEndpoints) Equals(in UccWithEndpoints) bool {
	return c.Client.Equals(in.Client) &&
		c.EndpointsHash == in.EndpointsHash &&
		c.endpointsName == in.endpointsName
}

type PerClientEnvoyEndpoints struct {
	endpoints krt.Collection[UccWithEndpoints]
	index     krt.Index[string, UccWithEndpoints]
}

func (ie *PerClientEnvoyEndpoints) FetchEndpointsForClient(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient) []UccWithEndpoints {
	return krt.Fetch(kctx, ie.endpoints, krt.FilterIndex(ie.index, ucc.ResourceName()))
}

func NewPerClientEnvoyEndpoints(
	krtopts krtutil.KrtOptions,
	uccs krt.Collection[ir.UniqlyConnectedClient],
	kgatewayEndpoints krt.Collection[ir.EndpointsForBackend],
	translateEndpoints func(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient, ep ir.EndpointsForBackend) (*envoyendpointv3.ClusterLoadAssignment, uint64),
) PerClientEnvoyEndpoints {
	eps := krt.NewManyCollection(kgatewayEndpoints, func(kctx krt.HandlerContext, ep ir.EndpointsForBackend) []UccWithEndpoints {
		uccs := krt.Fetch(kctx, uccs)
		uccWithEndpointsRet := make([]UccWithEndpoints, 0, len(uccs))

		// Deduplicate translation by client equivalence class WITHIN this
		// transform invocation, mirroring PerClientEnvoyClusters. Endpoint
		// translation's per-client inputs are ucc.Namespace, ucc.Labels, and —
		// unlike cluster translation — ucc.Locality (locality-aware priority
		// ordering), so the class key includes all three. Scoping the dedup to
		// one invocation keeps it correct by construction: every kctx-fetched
		// input the translation reads (DestinationRules etc.) is constant for
		// the duration of the transform, so no cross-run invalidation is
		// needed and none is attempted. The resulting CLA is shared across the
		// class's rows; snapshot resources are treated as immutable downstream.
		type classTranslation struct {
			cla            *envoyendpointv3.ClusterLoadAssignment
			additionalHash uint64
		}
		byClass := make(map[string]*classTranslation, 2)

		for _, ucc := range uccs {
			classKey := endpointTranslationClassKey(ucc)
			tr, ok := byClass[classKey]
			if !ok {
				cla, additionalHash := translateEndpoints(kctx, ucc, ep)
				tr = &classTranslation{cla: cla, additionalHash: additionalHash}
				byClass[classKey] = tr
			}
			u := UccWithEndpoints{
				Client:        ucc,
				Endpoints:     tr.cla,
				EndpointsHash: ep.LbEpsEqualityHash ^ tr.additionalHash,
				endpointsName: ep.ResourceName(),
			}
			uccWithEndpointsRet = append(uccWithEndpointsRet, u)
		}
		return uccWithEndpointsRet
	}, krtopts.ToOptions("PerClientEnvoyEndpoints")...)
	idx := krtpkg.UnnamedIndex(eps, func(ucc UccWithEndpoints) []string {
		return []string{ucc.Client.ResourceName()}
	})

	return PerClientEnvoyEndpoints{
		endpoints: eps,
		index:     idx,
	}
}

// endpointTranslationClassKey groups connected clients whose endpoint
// translation is identical: EDS output may depend on the client only through
// Namespace, Labels (DestinationRule selection), and Locality (priority
// ordering) — a contract for PerClientProcessEndpoints implementations and
// the locality prioritizer.
func endpointTranslationClassKey(ucc ir.UniqlyConnectedClient) string {
	return fmt.Sprintf("%d~%s~%s/%s/%s",
		utils.HashLabels(ucc.Labels), ucc.Namespace,
		ucc.Locality.Region, ucc.Locality.Zone, ucc.Locality.Subzone)
}
