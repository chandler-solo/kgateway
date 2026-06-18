package proxy_syncer

import (
	"context"

	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	krtutil "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
)

// agnosticUCC is the sentinel client used to translate backends once when pod
// locality is disabled (DISABLE_POD_LOCALITY_XDS). Its empty Namespace/Labels/
// Locality make every per-client translation hook (destrule DR matching,
// endpoint locality prioritization) produce output identical to every connected
// client, so a single translation is valid for all replicas of all gateways.
var agnosticUCC = ir.UniquelyConnectedClient{}

// NewAgnosticEnvoyClusters translates each backend's cluster exactly once, with
// no dependency on the connected-client set. This is the CDS half of the
// locality-agnostic xDS graph: because cluster output does not vary by client
// when locality is disabled, there is no need to fan out over every
// UniquelyConnectedClient as NewPerClientEnvoyClusters does. A client connecting
// or disconnecting therefore triggers zero cluster re-translation.
//
// The result reuses uccWithCluster (with agnosticUCC as Client) so the same row
// type feeds GenerateBackendStatusReport, which keys only on Error/BackendSource/
// BackendGeneration.
func NewAgnosticEnvoyClusters(
	ctx context.Context,
	krtopts krtutil.KrtOptions,
	translator *irtranslator.BackendTranslator,
	finalBackends krt.Collection[*ir.BackendObjectIR],
) krt.Collection[uccWithCluster] {
	return krt.NewCollection(finalBackends, func(kctx krt.HandlerContext, backendObj *ir.BackendObjectIR) *uccWithCluster {
		c, err := translator.TranslateBackend(ctx, kctx, agnosticUCC, backendObj)
		if c == nil {
			return nil
		}
		var backendGeneration int64
		if backendObj.Obj != nil {
			backendGeneration = backendObj.Obj.GetGeneration()
		}
		return &uccWithCluster{
			Name:    c.GetName(),
			Client:  agnosticUCC,
			Cluster: c,
			// pass along the error(s) indicating to consumers that this cluster is not usable
			Error:             err,
			ClusterVersion:    utils.HashProto(c),
			BackendSource:     backendObj.GetObjectSource(),
			BackendGeneration: backendGeneration,
		}
	}, krtopts.ToOptions("AgnosticEnvoyClusters")...)
}
