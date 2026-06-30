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
	Client ir.UniquelyConnectedClient
	// +krtEqualsTodo include full cluster diff in equality
	Cluster        *envoyclusterv3.Cluster
	ClusterVersion uint64
	// +krtEqualsTodo reconcile name-only equality semantics
	Name string
	// Error is the translation error for this backend/client pair, if any. Compared by message
	// in Equals because all errored clusters share one blackhole proto, so ClusterVersion can't
	// tell error states apart.
	Error error
	// BackendSource identifies the Backend this cluster was translated from, for status attribution.
	BackendSource ir.ObjectSource
	// BackendGeneration is the observed generation of the source Backend.
	BackendGeneration int64
	// SharesBase is true when this row reuses the client-independent base cluster proto verbatim
	// (no per-client overlay changed it). Such rows were already validated once at the base, so the
	// per-client residual validation pass skips them.
	SharesBase bool
}

func (c uccWithCluster) ResourceName() string {
	return fmt.Sprintf("%s/%s", c.Client.ResourceName(), c.Name)
}

func (c uccWithCluster) Equals(in uccWithCluster) bool {
	return c.Client.Equals(in.Client) &&
		c.ClusterVersion == in.ClusterVersion &&
		c.BackendSource == in.BackendSource &&
		c.BackendGeneration == in.BackendGeneration &&
		c.SharesBase == in.SharesBase &&
		errString(c.Error) == errString(in.Error)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type PerClientEnvoyClusters struct {
	clusters krt.Collection[uccWithCluster]
	index    krt.Index[string, uccWithCluster]
}

func (iu *PerClientEnvoyClusters) FetchClustersForClient(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient) []uccWithCluster {
	return krt.Fetch(kctx, iu.clusters, krt.FilterIndex(iu.index, ucc.ResourceName()))
}

func NewPerClientEnvoyClusters(
	ctx context.Context,
	krtopts krtutil.KrtOptions,
	translator *irtranslator.BackendTranslator,
	finalBackends krt.Collection[*ir.BackendObjectIR],
	uccs krt.Collection[ir.UniquelyConnectedClient],
) PerClientEnvoyClusters {
	// baseClusters holds the client-independent ("base") translation of each Backend, computed once
	// per Backend rather than once per (backend, client). The per-client overlay (destination rules,
	// locality priorities) is layered on cheaply below. In strict mode the base is validated here,
	// exactly once per Backend, so the many clients that share it never re-validate identical content.
	baseClusters := krt.NewCollection(finalBackends, func(kctx krt.HandlerContext, backendObj *ir.BackendObjectIR) *irtranslator.BackendBaseCluster {
		base := translator.TranslateBackendBase(ctx, backendObj)
		if base != nil {
			translator.ValidateBaseCluster(ctx, base)
		}
		return base
	}, krtopts.ToOptions("BackendBaseClusters")...)

	// translatedClusters produces the per-(backend, client) rows by overlaying per-client concerns
	// onto the shared base. It runs no Envoy validation. Rows whose overlay did not change the base
	// proto are flagged SharesBase (and reuse the base version hash) so the validation pass below can
	// skip them: they were already validated once when the base was translated.
	translatedClusters := krt.NewManyCollection(finalBackends, func(kctx krt.HandlerContext, backendObj *ir.BackendObjectIR) []uccWithCluster {
		base := krt.FetchOne(kctx, baseClusters, krt.FilterKey(backendObj.ResourceName()))
		if base == nil || base.Cluster == nil {
			// Unsupported backend (no translator/plugin); nothing to emit.
			return nil
		}

		uccs := krt.Fetch(kctx, uccs)
		uccWithClusterRet := make([]uccWithCluster, 0, len(uccs))

		var backendGeneration int64
		if backendObj.Obj != nil {
			backendGeneration = backendObj.Obj.GetGeneration()
		}

		for _, ucc := range uccs {
			var c *envoyclusterv3.Cluster
			var err error
			if base.Err != nil {
				// Base translation/validation failed; every client shares the blackhole + error.
				c, err = base.Cluster, base.Err
			} else {
				c, err = translator.ApplyPerClientOverlay(ctx, kctx, ucc, backendObj, base)
			}
			if c == nil {
				continue
			}
			// Reuse the base version hash (and skip re-validation) for clients that share the base
			// cluster by identity. Only clients whose overlay produced a distinct proto need a fresh
			// hash and a residual validation. This keeps hashing + validation at
			// O(backends + overlays) instead of O(backends x clients).
			sharesBase := c == base.Cluster
			clusterVersion := base.Version
			if !sharesBase {
				clusterVersion = utils.HashProto(c)
			}
			uccWithClusterRet = append(uccWithClusterRet, uccWithCluster{
				Name:    c.GetName(),
				Client:  ucc,
				Cluster: c,
				// pass along the error(s) indicating to consumers that this cluster is not usable
				Error:             err,
				ClusterVersion:    clusterVersion,
				BackendSource:     backendObj.GetObjectSource(),
				BackendGeneration: backendGeneration,
				SharesBase:        sharesBase,
			})
		}
		return uccWithClusterRet
	}, krtopts.ToOptions("TranslatedPerClientEnvoyClusters")...)
	translatedIdx := krtpkg.UnnamedIndex(translatedClusters, func(ucc uccWithCluster) []string {
		return []string{ucc.Client.ResourceName()}
	})

	// clusters validates each client's residual clusters (those whose per-client overlay changed the
	// base) in a single Envoy invocation per client. A client's clusters have distinct names (one per
	// backend), so they batch safely; base-shared clusters were already validated once above.
	clusters := krt.NewManyCollection(uccs, func(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient) []uccWithCluster {
		translated := krt.Fetch(kctx, translatedClusters, krt.FilterIndex(translatedIdx, ucc.ResourceName()))
		return validateResidualClusters(ctx, translator, translated)
	}, krtopts.ToOptions("PerClientEnvoyClusters")...)
	idx := krtpkg.UnnamedIndex(clusters, func(ucc uccWithCluster) []string {
		return []string{ucc.Client.ResourceName()}
	})

	return PerClientEnvoyClusters{
		clusters: clusters,
		index:    idx,
	}
}

// validateResidualClusters validates, in strict mode, the per-client clusters whose overlay changed
// the shared base (SharesBase == false). Base-shared clusters were already validated once when the
// base was translated, so they are skipped. The residuals are validated in one Envoy call; only if
// that batch fails are they validated one-by-one to isolate the offender(s), which are blackholed,
// after which the survivors are re-checked together to catch any cross-cluster interaction.
func validateResidualClusters(
	ctx context.Context,
	translator *irtranslator.BackendTranslator,
	clusters []uccWithCluster,
) []uccWithCluster {
	if translator == nil || !translator.StrictValidationEnabled() {
		return clusters
	}

	candidates := make([]int, 0, len(clusters))
	candidateClusters := make([]*envoyclusterv3.Cluster, 0, len(clusters))
	for i := range clusters {
		if clusters[i].SharesBase || clusters[i].Error != nil || clusters[i].Cluster == nil {
			continue
		}
		candidates = append(candidates, i)
		candidateClusters = append(candidateClusters, clusters[i].Cluster)
	}
	if len(candidateClusters) == 0 {
		return clusters
	}

	if err := translator.ValidateClusterConfigs(ctx, candidateClusters); err == nil {
		return clusters
	} else {
		logger.Debug("strict backend batch validation failed; isolating invalid clusters", "clusters", len(candidateClusters), "error", err)
	}

	survivingClusters := make([]*envoyclusterv3.Cluster, 0, len(candidateClusters))
	survivingIndexes := make([]int, 0, len(candidateClusters))
	for _, idx := range candidates {
		if err := translator.ValidateClusterConfig(ctx, clusters[idx].Cluster); err != nil {
			markClusterValidationError(&clusters[idx], err)
			continue
		}
		survivingClusters = append(survivingClusters, clusters[idx].Cluster)
		survivingIndexes = append(survivingIndexes, idx)
	}

	if err := translator.ValidateClusterConfigs(ctx, survivingClusters); err != nil {
		logger.Error("strict backend batch validation failed after invalid cluster isolation", "clusters", len(survivingClusters), "error", err)
		for _, idx := range survivingIndexes {
			markClusterValidationError(&clusters[idx], err)
		}
	}
	return clusters
}

func markClusterValidationError(cluster *uccWithCluster, err error) {
	logger.Error("cluster failed xDS validation in strict mode", "cluster", cluster.Name, "error", err)
	cluster.Error = err
	cluster.Cluster = irtranslator.BlackholeClusterForName(cluster.Name)
	cluster.ClusterVersion = utils.HashProto(cluster.Cluster)
	cluster.SharesBase = false
}
