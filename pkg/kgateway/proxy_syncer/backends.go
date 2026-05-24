package proxy_syncer

import (
	"context"
	"fmt"
	"hash/fnv"

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
// PerClientClusterOverlay returns non-nil for (ucc, backend), when the cluster
// needs an inline CLA (which is always per-client via PrioritizeEndpoints), or
// when strict-mode validation fails on the per-client cluster (the delta then
// carries the blackhole + error so the snapshot tracks it as errored for this
// UCC only — other UCCs may still see a valid cluster).
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
	// +krtEqualsTodo surface translation errors in equality or drop field
	Error error
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

// baseClusterVersion returns the equality hash for a base translation result.
// It folds the inline endpoints hash into the cluster proto hash when the cluster
// type supports an inline CLA: the per-client CLA is built from
// BaseCluster.EndpointInputs and is NOT part of the base proto, so without this
// the base would not re-publish when endpoints change — leaving clients pinned
// to stale LoadAssignments for non-EDS backends (e.g. ServiceEntry-style).
//
// For EDS clusters EndpointInputs may also be non-nil, but those endpoints feed
// the separate EDS pipeline and are not used by ApplyPerClient; gating on
// SupportsInlineCLA keeps the version stable for the EDS case so equivalent
// translations do not churn the snapshot.
func baseClusterVersion(b *irtranslator.BaseCluster) uint64 {
	if b.Error != nil {
		return 0
	}
	hasher := fnv.New64a()
	utils.HashProtoWithHasher(hasher, b.Cluster)
	if b.SupportsInlineCLA && b.EndpointInputs != nil {
		utils.HashUint64(hasher, b.EndpointInputs.EndpointsForBackend.LbEpsEqualityHash)
	}
	return hasher.Sum64()
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
			// Delta wins on cluster + version. Delta error wins over base error
			// because a per-UCC failure (e.g. strict-mode validation of the
			// post-overlay cluster) is the more specific signal — base errors
			// are caught by the short-circuit in the deltas builder, so reaching
			// this branch with a base.Error set is impossible in production.
			derr := d.Error
			if derr == nil {
				derr = b.Error
			}
			out = append(out, uccWithCluster{
				Client:         ucc,
				Cluster:        d.Cluster,
				ClusterVersion: d.ClusterVersion,
				Name:           d.Name,
				Error:          derr,
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
			Error:          deltas[i].Error,
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
		return &baseEnvoyCluster{
			Name:           baseRes.Cluster.GetName(),
			Cluster:        baseRes.Cluster,
			ClusterVersion: baseClusterVersion(baseRes),
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
				// Emit a delta entry that carries the error so the snapshot
				// tracks this cluster as errored for THIS UCC only. Falling
				// back to the (valid) base would defeat strict-mode validation;
				// the user opted in to having broken configs surface as errors
				// rather than NACKs at the Envoy data plane.
				logger.Error("failed to apply per-client overlay",
					"backend", b.Name, "ucc", ucc.ResourceName(), "error", err)
				name := b.Name
				if perClient != nil {
					name = perClient.GetName()
				}
				out = append(out, uccClusterDelta{
					Client:         ucc,
					Name:           name,
					Cluster:        perClient,
					ClusterVersion: utils.HashString(err.Error()),
					Error:          err,
				})
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
