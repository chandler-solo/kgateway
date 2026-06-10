package proxy_syncer

import (
	"context"

	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
)

// publicationGate is the per-client publication policy and bookkeeping seam,
// implemented by perClientReconciler. Nil disables deferral and stuck
// tracking (used by tests); publication then goes straight to the cache.
type publicationGate interface {
	// shouldDeferIncomplete reports whether a publish attempt with incomplete
	// inputs should still be withheld (bounded per episode by a budget).
	shouldDeferIncomplete(clientKey string) bool
	// observeWithheld records a withheld publication; a non-nil pending wrapper
	// is retained for direct retry by the heartbeat loop.
	observeWithheld(clientKey string, pending *XdsSnapWrapper)
	// commitPublish atomically publishes and records the outcome; see
	// perClientReconciler.commitPublish.
	commitPublish(ctx context.Context, snapWrap XdsSnapWrapper, degraded bool, expectSeq *uint64) (published, recovered bool)
}

// syncXds publishes the per-client snapshot to the xDS cache. It reports
// whether a snapshot was actually published; false means the update was
// withheld (invalid snapshot, or an incomplete-inputs deferral) — the client
// stays marked stuck so the heartbeat keeps retrying.
//
// Publication policy (devel/architecture/perclient-xds-publication.md):
//
//   - Hard validation (malformed/nil/mistyped resources, inconsistent
//     snapshot, missing SDS references) always withholds: these indicate bugs,
//     and Envoy keeps the last good snapshot. Retrying the same data cannot
//     heal these, so no pending retry is retained; the heal is a new build.
//   - INCOMPLETE inputs — routes referencing clusters absent from the
//     per-client CDS, or CLAs synthesized empty for missing endpoint rows —
//     defer publication, bounded per episode by a budget. For a cold client
//     this is the reconnect race #13868 addressed; for a published client it
//     prevents a transiently incomplete rebuild from regressing Envoy's
//     state-of-the-world config (removing live clusters or wiping endpoints).
//     Envoy keeps its last coherent config while events or heartbeat
//     recomputes heal the inputs. The withheld snapshot is retained so the
//     heartbeat loop can re-attempt it directly: budget expiry must not
//     depend on a KRT event that quiet inputs will never produce.
//   - Past the budget the snapshot publishes as-is, marked DEGRADED, logged,
//     and counted: affected routes return no-cluster errors and synthesized
//     CLAs serve no endpoints — never silently. Degraded clients count as
//     stuck, so the heartbeat re-runs the per-client collections every tick
//     until a clean publish heals them, and the still-open episode lets
//     further incomplete updates flow immediately rather than re-deferring.
func (s *ProxyTranslator) syncXds(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
) bool {
	return s.syncXdsAttempt(ctx, snapWrap, nil)
}

// syncXdsAttempt is syncXds plus the retry entry point: expectSeq non-nil
// marks a re-attempt of a previously withheld snapshot, which only commits if
// no newer event superseded it (see perClientReconciler.commitPublish).
func (s *ProxyTranslator) syncXdsAttempt(
	ctx context.Context,
	snapWrap XdsSnapWrapper,
	expectSeq *uint64,
) bool {
	snap := snapWrap.snap
	proxyKey := snapWrap.proxyKey

	// stringifying the snapshot may be an expensive operation, so we'd like to avoid building the large
	// string if we're not even going to log it anyway
	logger.Debug("syncing xds snapshot", "proxy_key", proxyKey)

	logger.Log(ctx, logging.LevelTrace, "syncing xds snapshot", "proxy_key", proxyKey)

	if err := ValidateXDSSnapshot(snap); err != nil {
		logger.Error("invalid xds snapshot; preserving last published snapshot",
			"proxy_key", proxyKey, "error", err)
		recordSnapshotDefer(proxyKey, deferReasonInvalidSnapshot)
		if s.gate != nil && expectSeq == nil {
			s.gate.observeWithheld(proxyKey, nil)
		}
		return false
	}

	missing := findMissingReferencedClusters(
		snapWrap.referencedClusters,
		snap.Resources[envoycachetypes.Cluster].Items,
		snapWrap.erroredClusters,
	)
	incomplete := len(missing) > 0 || len(snapWrap.synthesizedClas) > 0
	if incomplete && s.gate != nil && s.gate.shouldDeferIncomplete(proxyKey) {
		logger.Info("deferring publish until per-client inputs are complete",
			"proxy_key", proxyKey,
			"missing_clusters", missing,
			"synthesized_load_assignments", snapWrap.synthesizedClas)
		recordSnapshotDefer(proxyKey, deferReasonIncompleteInputs)
		if expectSeq == nil {
			s.gate.observeWithheld(proxyKey, &snapWrap)
		}
		return false
	}

	degraded := incomplete

	var published, recovered bool
	if s.gate == nil {
		if err := s.xdsCache.SetSnapshot(ctx, proxyKey, snap); err != nil {
			logger.Error("failed to set xds snapshot", "proxy_key", proxyKey, "error", err)
			return false
		}
		published = true
	} else {
		published, recovered = s.gate.commitPublish(ctx, snapWrap, degraded, expectSeq)
	}
	if !published {
		return false
	}

	if len(missing) > 0 {
		// Budget expired: published as-is. The referencing routes return
		// no-cluster errors until the per-client pipeline produces the
		// clusters; the degraded mark keeps the heartbeat re-running the
		// per-client collections until then.
		logger.Warn("published snapshot with referenced clusters missing from per-client inputs",
			"proxy_key", proxyKey, "missing_clusters", missing)
		recordDegradedPublish(proxyKey, degradedReasonMissingClusters)
	}
	if len(snapWrap.synthesizedClas) > 0 {
		logger.Warn("published snapshot with synthesized empty load assignments; affected backends serve no endpoints",
			"proxy_key", proxyKey, "synthesized_load_assignments", snapWrap.synthesizedClas)
		recordDegradedPublish(proxyKey, degradedReasonSynthesizedClas)
	}
	if recovered {
		// A clean publish after a prior defer or degraded publish is a
		// recovery; with the heartbeat as backstop, recoveries of
		// long-deferred clients are heartbeat-driven heals (#14184).
		recordSnapshotRecovery(proxyKey)
	}
	return true
}
