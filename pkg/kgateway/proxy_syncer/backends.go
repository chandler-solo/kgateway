package proxy_syncer

import (
	"context"
	"fmt"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"istio.io/istio/pkg/kube/krt"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/irtranslator"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	krtutil "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

type uccWithCluster struct {
	Client ir.UniqlyConnectedClient
	// +krtEqualsTodo include full cluster diff in equality
	Cluster        *envoyclusterv3.Cluster
	ClusterVersion uint64
	// +krtEqualsTodo reconcile name-only equality semantics
	Name string
	// +krtEqualsTodo surface translation errors in equality or drop field
	Error error
}

func (c uccWithCluster) ResourceName() string {
	return fmt.Sprintf("%s/%s", c.Client.ResourceName(), c.Name)
}

func (c uccWithCluster) Equals(in uccWithCluster) bool {
	return c.Client.Equals(in.Client) && c.ClusterVersion == in.ClusterVersion
}

type PerClientEnvoyClusters struct {
	clusters krt.Collection[uccWithCluster]
	index    krt.Index[string, uccWithCluster]
}

func (iu *PerClientEnvoyClusters) FetchClustersForClient(kctx krt.HandlerContext, ucc ir.UniqlyConnectedClient) []uccWithCluster {
	return krt.Fetch(kctx, iu.clusters, krt.FilterIndex(iu.index, ucc.ResourceName()))
}

func NewPerClientEnvoyClusters(
	ctx context.Context,
	krtopts krtutil.KrtOptions,
	translator *irtranslator.BackendTranslator,
	finalBackends krt.Collection[*ir.BackendObjectIR],
	uccs krt.Collection[ir.UniqlyConnectedClient],
) PerClientEnvoyClusters {
	clusters := krt.NewManyCollection(finalBackends, func(kctx krt.HandlerContext, backendObj *ir.BackendObjectIR) []uccWithCluster {
		backendLogger := logger.With("backend", backendObj)
		uccs := krt.Fetch(kctx, uccs)
		uccWithClusterRet := make([]uccWithCluster, 0, len(uccs))

		// Deduplicate translation by client equivalence class WITHIN this
		// transform invocation. Cluster translation's per-client inputs are
		// only ucc.Namespace and ucc.Labels (the per-client plugin hooks —
		// destination rules, waypoint — read nothing else; Role and Locality
		// do not feed the cluster output), so clients sharing both produce
		// identical clusters. Translating once per class collapses the
		// dominant cost of this collection — N_backends x N_clients full
		// translations (each an external Envoy validation in strict mode) per
		// fan-out event, all serialized on this collection's single queue
		// goroutine — to N_backends x N_classes; gateway replicas of one
		// Deployment share a class, so this is typically N_backends x 1.
		//
		// Scoping the dedup to a single invocation makes it correct by
		// construction: every kctx-fetched input the translation reads
		// (DestinationRules etc.) is constant for the duration of the
		// transform, so no cross-run invalidation is needed and none is
		// attempted. The resulting *Cluster is shared across the class's rows;
		// snapshot resources are treated as immutable downstream (debug
		// marshalling clones before redacting).
		type classTranslation struct {
			cluster *envoyclusterv3.Cluster
			version uint64
			err     error
		}
		byClass := make(map[string]*classTranslation, 2)

		for _, ucc := range uccs {
			classKey := clusterTranslationClassKey(ucc)
			tr, ok := byClass[classKey]
			if !ok {
				backendLogger.Debug("applying destination rules for backend", "ucc", ucc.ResourceName())
				c, err := translator.TranslateBackend(ctx, kctx, ucc, backendObj)
				tr = &classTranslation{cluster: c, err: err}
				if c != nil {
					tr.version = utils.HashProto(c)
				}
				byClass[classKey] = tr
			}
			if tr.cluster == nil {
				continue
			}
			uccWithClusterRet = append(uccWithClusterRet, uccWithCluster{
				Name:    tr.cluster.GetName(),
				Client:  ucc,
				Cluster: tr.cluster,
				// pass along the error(s) indicating to consumers that this cluster is not usable
				Error:          tr.err,
				ClusterVersion: tr.version,
			})
		}
		return uccWithClusterRet
	}, krtopts.ToOptions("PerClientEnvoyClusters")...)
	idx := krtpkg.UnnamedIndex(clusters, func(ucc uccWithCluster) []string {
		return []string{ucc.Client.ResourceName()}
	})

	return PerClientEnvoyClusters{
		clusters: clusters,
		index:    idx,
	}
}

// clusterTranslationClassKey groups connected clients whose backend->cluster
// translation is identical. CDS translation may depend on the client only
// through Namespace and Labels (DestinationRule selection by proxy namespace
// and workload selector; waypoint attachment by labels) — a contract for
// PerClientProcessBackend implementations. Role and Locality are deliberately
// excluded: neither feeds cluster output (Locality affects only endpoint
// translation, which has its own class key in cla.go).
//
// HashLabels is a non-cryptographic hash, so two distinct label sets could in
// principle collide into one class. This is not a new exposure: the snapshot
// key itself (labeledRole, uniqueclients.go) already collapses clients by the
// same HashLabels value, so colliding clients were already serving each
// other's snapshots before this key existed. Any collision-resistance fix
// must change both sites together.
func clusterTranslationClassKey(ucc ir.UniqlyConnectedClient) string {
	return fmt.Sprintf("%d~%s", utils.HashLabels(ucc.Labels), ucc.Namespace)
}
