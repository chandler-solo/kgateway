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

// baseEnvoyCluster is the UCC-invariant translation result for a single backend.
// The Cluster proto is shared across every UCC that targets this backend — it is
// read-only on the consumer side, and per-client mutations clone it before
// modifying. This is the change that lets the per-client collection stay sparse.
type baseEnvoyCluster struct {
	Name string
	// +krtEqualsTodo include full cluster diff in equality
	Cluster        *envoyclusterv3.Cluster
	ClusterVersion uint64
	// +krtEqualsTodo surface translation errors in equality or drop field
	Error error
	// Base is the rest of the base-translation result, retained so the deltas
	// collection can build per-client clusters without redoing translation.
	// Not included in equality — ClusterVersion captures the relevant state.
	// +noKrtEquals
	Base *irtranslator.BaseCluster
	// Backend pointer for per-client overlay plugins that take BackendObjectIR.
	// +noKrtEquals
	Backend *ir.BackendObjectIR
}

func (b baseEnvoyCluster) ResourceName() string { return b.Name }

func (b baseEnvoyCluster) Equals(in baseEnvoyCluster) bool {
	return b.Name == in.Name && b.ClusterVersion == in.ClusterVersion
}

// uccClusterDelta is a per-client cluster materialized only when at least one
// PerClientClusterOverlay returns non-nil for (ucc, backend) or when the cluster
// needs an inline CLA (which is always per-client via PrioritizeEndpoints).
//
// The KRT manyCollection that produces these entries emits nothing for the
// dominant case where no overlay applies — that is what shrinks the per-client
// cluster KRT footprint from O(N*M) to O(N*K), where K is the count of
// backends that genuinely vary per UCC. In typical workloads K << M.
type uccClusterDelta struct {
	Client ir.UniquelyConnectedClient
	Name   string
	// +krtEqualsTodo include full cluster diff in equality
	Cluster        *envoyclusterv3.Cluster
	ClusterVersion uint64
}

func (d uccClusterDelta) ResourceName() string {
	return fmt.Sprintf("%s/%s", d.Client.ResourceName(), d.Name)
}

func (d uccClusterDelta) Equals(in uccClusterDelta) bool {
	return d.Client.Equals(in.Client) && d.Name == in.Name && d.ClusterVersion == in.ClusterVersion
}

// uccWithCluster is the merged view returned by FetchClustersForClient: the
// resolved cluster (base or delta) along with any base-translation error.
type uccWithCluster struct {
	Client         ir.UniquelyConnectedClient
	Cluster        *envoyclusterv3.Cluster
	ClusterVersion uint64
	Name           string
	Error          error
}

func (c uccWithCluster) ResourceName() string {
	return fmt.Sprintf("%s/%s", c.Client.ResourceName(), c.Name)
}

type PerClientEnvoyClusters struct {
	base       krt.Collection[baseEnvoyCluster]
	deltas     krt.Collection[uccClusterDelta]
	deltaByUcc krt.Index[string, uccClusterDelta]
}

// HasSynced reports whether both the base and delta collections have synced.
// Used to gate xDS publishing until cluster translation has reached steady state.
func (iu *PerClientEnvoyClusters) HasSynced() bool {
	if iu.base != nil && !iu.base.HasSynced() {
		return false
	}
	if iu.deltas != nil && !iu.deltas.HasSynced() {
		return false
	}
	return true
}

// FetchClustersForClient returns the merged set of clusters for a UCC: a per-client
// delta for each backend that has one, and the shared base cluster otherwise. A
// delta whose name does not match any base is included as a standalone entry
// (this only happens in tests; production deltas are always emitted off an
// existing base).
//
// The *Cluster protos in the returned slice are shared with other UCCs (base)
// or unique to this UCC (delta); callers MUST NOT mutate them.
func (iu *PerClientEnvoyClusters) FetchClustersForClient(kctx krt.HandlerContext, ucc ir.UniquelyConnectedClient) []uccWithCluster {
	var bases []baseEnvoyCluster
	if iu.base != nil {
		bases = krt.Fetch(kctx, iu.base)
	}
	var deltas []uccClusterDelta
	if iu.deltas != nil {
		deltas = krt.Fetch(kctx, iu.deltas, krt.FilterIndex(iu.deltaByUcc, ucc.ResourceName()))
	}

	var deltaByName map[string]*uccClusterDelta
	if len(deltas) > 0 {
		deltaByName = make(map[string]*uccClusterDelta, len(deltas))
		for i := range deltas {
			deltaByName[deltas[i].Name] = &deltas[i]
		}
	}

	out := make([]uccWithCluster, 0, len(bases)+len(deltas))
	seen := make(map[string]struct{}, len(bases))
	for _, b := range bases {
		seen[b.Name] = struct{}{}
		if d, ok := deltaByName[b.Name]; ok {
			out = append(out, uccWithCluster{
				Client:         ucc,
				Cluster:        d.Cluster,
				ClusterVersion: d.ClusterVersion,
				Name:           d.Name,
				Error:          b.Error,
			})
			continue
		}
		out = append(out, uccWithCluster{
			Client:         ucc,
			Cluster:        b.Cluster,
			ClusterVersion: b.ClusterVersion,
			Name:           b.Name,
			Error:          b.Error,
		})
	}
	for i := range deltas {
		if _, ok := seen[deltas[i].Name]; ok {
			continue
		}
		out = append(out, uccWithCluster{
			Client:         ucc,
			Cluster:        deltas[i].Cluster,
			ClusterVersion: deltas[i].ClusterVersion,
			Name:           deltas[i].Name,
		})
	}
	return out
}

func NewPerClientEnvoyClusters(
	ctx context.Context,
	krtopts krtutil.KrtOptions,
	translator *irtranslator.BackendTranslator,
	finalBackends krt.Collection[*ir.BackendObjectIR],
	uccs krt.Collection[ir.UniquelyConnectedClient],
) PerClientEnvoyClusters {
	// Base clusters: one entry per backend, computed once and shared across all
	// UCCs. Anything that does not depend on the UCC lives here:
	// initializeCluster, InitEnvoyBackend, DNS lookup family, non-per-client
	// ProcessBackend hooks, gateway client certificate injection, and strict-mode
	// validation.
	base := krt.NewCollection(finalBackends, func(kctx krt.HandlerContext, backendObj *ir.BackendObjectIR) *baseEnvoyCluster {
		baseRes := translator.TranslateBackendBase(ctx, backendObj)
		if baseRes == nil {
			return nil
		}
		var version uint64
		if baseRes.Error == nil {
			version = utils.HashProto(baseRes.Cluster)
		}
		return &baseEnvoyCluster{
			Name:           baseRes.Cluster.GetName(),
			Cluster:        baseRes.Cluster,
			ClusterVersion: version,
			Error:          baseRes.Error,
			Base:           baseRes,
			Backend:        backendObj,
		}
	}, krtopts.ToOptions("BaseEnvoyClusters")...)

	// Per-client deltas: only emitted for (ucc, backend) pairs that genuinely
	// vary — at least one PerClientClusterOverlay returned non-nil, or the
	// cluster requires a UCC-dependent inline CLA. Most pairs emit nothing,
	// which is what keeps the collection sparse.
	//
	// Driven off the base collection (not finalBackends) so a UCC churn does
	// not re-translate the base — we reuse the already-computed BaseCluster.
	deltas := krt.NewManyCollection(base, func(kctx krt.HandlerContext, b baseEnvoyCluster) []uccClusterDelta {
		if b.Error != nil || b.Base == nil || b.Backend == nil {
			// Errored base: every UCC sees the same blackhole, no per-client
			// variation possible.
			return nil
		}
		clients := krt.Fetch(kctx, uccs)
		if len(clients) == 0 {
			return nil
		}
		out := make([]uccClusterDelta, 0, len(clients))
		for _, ucc := range clients {
			perClient, err := translator.ApplyPerClient(kctx, ctx, ucc, b.Backend, b.Base)
			if err != nil {
				logger.Error("failed to apply per-client overlay",
					"backend", b.Name, "ucc", ucc.ResourceName(), "error", err)
				continue
			}
			if perClient == nil {
				// No per-client variation. Snapshot will reference the shared
				// base cluster instead.
				continue
			}
			out = append(out, uccClusterDelta{
				Client:         ucc,
				Name:           perClient.GetName(),
				Cluster:        perClient,
				ClusterVersion: utils.HashProto(perClient),
			})
		}
		return out
	}, krtopts.ToOptions("PerClientEnvoyClusterDeltas")...)

	deltaByUcc := krtpkg.UnnamedIndex(deltas, func(d uccClusterDelta) []string {
		return []string{d.Client.ResourceName()}
	})

	return PerClientEnvoyClusters{
		base:       base,
		deltas:     deltas,
		deltaByUcc: deltaByUcc,
	}
}
