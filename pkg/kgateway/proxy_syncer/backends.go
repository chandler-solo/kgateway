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
	// Error is the translation error for this backend, if any. Compared by message in
	// Equals because all errored clusters share one blackhole proto and baseClusterVersion
	// collapses every error to 0, so ClusterVersion can't tell error states apart.
	Error error
	// BackendSource identifies the Backend this cluster was translated from, for status attribution.
	BackendSource ir.ObjectSource
	// BackendGeneration is the observed generation of the source Backend.
	BackendGeneration int64
	// Base is the rest of the base-translation result, retained so the deltas
	// collection can build per-client clusters without redoing translation.
	// Not included in equality — it is derived deterministically from Backend,
	// whose equality is checked.
	// +noKrtEquals
	Base *irtranslator.BaseCluster
	// Backend pointer for per-client overlay plugins that take BackendObjectIR.
	// Included in equality: overlay plugins self-determine applicability from
	// backend state that can change without changing the translated base proto,
	// its generation, or its error — e.g. the waypoint plugin reads the
	// Service's ingress-use-waypoint label, and label edits do not bump
	// metadata.generation. Suppressing such updates here would leave the deltas
	// collection evaluating overlays against a stale Backend forever.
	Backend *ir.BackendObjectIR
}

func (b baseEnvoyCluster) ResourceName() string { return b.Name }

func (b baseEnvoyCluster) Equals(in baseEnvoyCluster) bool {
	if !(b.Name == in.Name &&
		b.ClusterVersion == in.ClusterVersion &&
		b.BackendSource == in.BackendSource &&
		b.BackendGeneration == in.BackendGeneration &&
		errString(b.Error) == errString(in.Error)) {
		return false
	}
	if b.Backend == nil || in.Backend == nil {
		return b.Backend == in.Backend
	}
	return b.Backend.Equals(*in.Backend)
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
// resolved cluster (base or delta) along with any translation error and the
// source Backend identity used for status attribution.
type uccWithCluster struct {
	Client         ir.UniquelyConnectedClient
	Cluster        *envoyclusterv3.Cluster
	ClusterVersion uint64
	Name           string
	Error          error
	// BackendSource identifies the Backend this cluster was translated from, for status attribution.
	BackendSource ir.ObjectSource
	// BackendGeneration is the observed generation of the source Backend.
	BackendGeneration int64
}

func (c uccWithCluster) ResourceName() string {
	return fmt.Sprintf("%s/%s", c.Client.ResourceName(), c.Name)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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
	// Track only the deltas consumed by a base (at most K, the per-UCC delta
	// count) rather than every base name (M). Lets us skip the standalone scan
	// entirely in the common case where every delta overlays an existing base.
	var consumed map[string]struct{}
	for _, b := range bases {
		if d, ok := deltaByName[b.Name]; ok {
			if consumed == nil {
				consumed = make(map[string]struct{}, len(deltas))
			}
			consumed[b.Name] = struct{}{}
			// Delta wins on cluster + version. Delta error wins over base error
			// because a per-UCC failure (e.g. strict-mode validation of the
			// post-overlay cluster) is the more specific signal — base errors
			// are caught by the short-circuit in the deltas builder, so reaching
			// this branch with a base.Error set is impossible in production.
			// Backend identity always comes from the base, which is where the
			// source Backend is tracked.
			derr := d.Error
			if derr == nil {
				derr = b.Error
			}
			out = append(out, uccWithCluster{
				Client:            ucc,
				Cluster:           d.Cluster,
				ClusterVersion:    d.ClusterVersion,
				Name:              d.Name,
				Error:             derr,
				BackendSource:     b.BackendSource,
				BackendGeneration: b.BackendGeneration,
			})
			continue
		}
		out = append(out, uccWithCluster{
			Client:            ucc,
			Cluster:           b.Cluster,
			ClusterVersion:    b.ClusterVersion,
			Name:              b.Name,
			Error:             b.Error,
			BackendSource:     b.BackendSource,
			BackendGeneration: b.BackendGeneration,
		})
	}
	// Standalone deltas (no matching base) only arise in tests; production deltas
	// are always emitted off an existing base, so this scan is skipped there.
	if len(consumed) < len(deltas) {
		for i := range deltas {
			if _, ok := consumed[deltas[i].Name]; ok {
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
	}
	return out
}

// FetchForStatus returns the cluster views needed for fleet-wide Backend status
// attribution: one entry per base cluster (carrying the source Backend identity and
// any UCC-invariant translation error) plus one entry per errored per-client delta
// (carrying the per-client translation error attributed to the same Backend). Only
// Error, BackendSource, and BackendGeneration are populated — the fields
// GenerateBackendStatusReport consumes. Non-errored deltas contribute nothing to
// status and are skipped.
func (iu *PerClientEnvoyClusters) FetchForStatus(kctx krt.HandlerContext) []uccWithCluster {
	var bases []baseEnvoyCluster
	if iu.base != nil {
		bases = krt.Fetch(kctx, iu.base)
	}
	var deltas []uccClusterDelta
	if iu.deltas != nil {
		deltas = krt.Fetch(kctx, iu.deltas)
	}

	baseByName := make(map[string]*baseEnvoyCluster, len(bases))
	out := make([]uccWithCluster, 0, len(bases)+len(deltas))
	for i := range bases {
		b := &bases[i]
		baseByName[b.Name] = b
		out = append(out, uccWithCluster{
			Name:              b.Name,
			Error:             b.Error,
			BackendSource:     b.BackendSource,
			BackendGeneration: b.BackendGeneration,
		})
	}
	for i := range deltas {
		d := &deltas[i]
		if d.Error == nil {
			continue
		}
		// Backend identity lives on the base; production deltas always overlay one.
		var src ir.ObjectSource
		var gen int64
		if b, ok := baseByName[d.Name]; ok {
			src = b.BackendSource
			gen = b.BackendGeneration
		}
		out = append(out, uccWithCluster{
			Client:            d.Client,
			Name:              d.Name,
			Error:             d.Error,
			BackendSource:     src,
			BackendGeneration: gen,
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
		var backendGeneration int64
		if backendObj.Obj != nil {
			backendGeneration = backendObj.Obj.GetGeneration()
		}
		return &baseEnvoyCluster{
			Name:              baseRes.Cluster.GetName(),
			Cluster:           baseRes.Cluster,
			ClusterVersion:    baseClusterVersion(baseRes),
			Error:             baseRes.Error,
			BackendSource:     backendObj.GetObjectSource(),
			BackendGeneration: backendGeneration,
			Base:              baseRes,
			Backend:           backendObj,
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
		// Intern identical per-client clusters across UCCs. Inline-CLA backends
		// materialize a delta for every UCC, but UCCs that share the relevant
		// inputs (e.g. the same locality) produce byte-identical clusters; sharing
		// one proto instead of N clones cuts allocations to O(distinct). The protos
		// are read-only on the consumer side, so aliasing is safe.
		internByVersion := map[uint64]*envoyclusterv3.Cluster{}
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
			clusterVersion := utils.HashProto(perClient)
			if shared, ok := internByVersion[clusterVersion]; ok {
				perClient = shared
			} else {
				internByVersion[clusterVersion] = perClient
			}
			out = append(out, uccClusterDelta{
				Client:         ucc,
				Name:           perClient.GetName(),
				Cluster:        perClient,
				ClusterVersion: clusterVersion,
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
