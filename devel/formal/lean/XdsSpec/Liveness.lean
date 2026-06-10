/-
XdsSpec.Liveness: the stuck-client progress theorem.

The safety proofs in XdsSpec/Proofs.lean say nothing about progress, and
the production incident behind assumption KRT-A1 (see ASSUMPTIONS.md)
lived exactly in that gap: when the KRT fan-out event that would have
made the per-client inputs coherent is dropped, `inputBecomesCoherent`
never fires and the client sits at `deferredPartial` forever. Nothing
the spec proved was violated — convergence was simply never reached.

The watchdog heartbeat (`heartbeatRederive`) is the mechanism that
closes the gap: it recomputes the per-client inputs from current truth
without waiting for an event. This module proves it is sufficient:

  `stuck_client_converges`: from ANY reachable deferred state, one
  heartbeat re-derivation followed by the ordinary publication path
  reaches a converged state (Envoy active on the new snapshot, or
  steady state because the recomputed snapshot already matches the
  cache) in at most five steps — for any name universe.

The proof is constructive: it exhibits the action sequence
(heartbeat -> publish or observe-converged -> Envoy learns CDS ->
EDS watch responds or is already current -> activate) and discharges
every guard from the inductive invariant. The finite-instance
counterpart is checked by the model checker in Main.lean: the
DroppedFanout system (no `inputBecomesCoherent`) violates liveness, and
adding `heartbeatRederive` restores it.
-/
import XdsSpec.Proofs

namespace XdsSpec

variable {Name : Type} [DecidableEq Name]

/-- Reflexive-transitive closure of safe steps starting at `s`. -/
inductive SafeSteps : XdsState Name → XdsState Name → Prop
  | refl (s : XdsState Name) : SafeSteps s s
  | head {s t u : XdsState Name} (a : Action Name)
      (hsafe : a.isSafe = true)
      (happ : applyAction s a = some t)
      (h : SafeSteps t u) : SafeSteps s u

/-- Converged: Envoy is active on the new snapshot, or the system is in
steady state (the recomputed snapshot already matched the cache). -/
def Converged (s : XdsState Name) : Prop :=
  s.phase = .activeNew ∨ s.phase = .stableOld

theorem versionEq_some_subset {a b : List Name}
    (h : versionEq (some a) (some b) = true) :
    NameSet.subset a b = true := by
  simp only [versionEq] at h
  exact NameSet.subset_of_eq h

/--
The progress theorem: a client stuck at `deferredPartial` converges
after a single heartbeat re-derivation, via the ordinary publication
path, in at most five steps. Holds for any reachable state and any
name universe.

The heartbeat parameters instantiate KRT-A1: the watchdog recomputes
the cluster and endpoint inputs from current truth, so the recomputed
candidate is dependency-closed. Here the recomputed truth is taken to
be `desiredRefs` for both CDS and EDS — the simplest closed candidate;
any closed recomputation admits the same path.
-/
theorem stuck_client_converges {old : List Name} {s : XdsState Name}
    (hr : Reachable old s) (hstuck : s.phase = .deferredPartial) :
    ∃ s', SafeSteps s s' ∧ Converged s' := by
  have hinv0 := IndInv.of_reachable hr
  -- Step 1: heartbeat recomputes a trivially-closed candidate.
  let u : XdsState Name := { s with
    phase := .coherentInput
    candidateCds := s.desiredRefs
    candidateEds := s.desiredRefs
    candidateEdsVersion := some s.desiredRefs
    computedState := .coherent
    krtEvent := .update }
  have happ1 : applyAction s (.heartbeatRederive s.desiredRefs s.desiredRefs)
      = some u := by
    simp [applyAction, hstuck, u]
  have hinv1 : IndInv u := hinv0.preserved rfl happ1
  by_cases hmatch : cacheMatchesCandidate u = true
  · -- The recomputed snapshot already matches the cache: converged;
    -- snapshotPerClient would emit no change event.
    have happ2 : applyAction u .observeConverged
        = some { u with phase := .stableOld, krtEvent := .none } := by
      simp [applyAction, hmatch]; rfl
    exact ⟨_, .head _ rfl happ1 (.head _ rfl happ2 (.refl _)), Or.inr rfl⟩
  · -- The recomputed snapshot differs: publish it.
    have hmatch' : cacheMatchesCandidate u = false :=
      Bool.eq_false_iff.mpr hmatch
    have hclosed : candidateClosed u = true := by
      simp [candidateClosed, u, NameSet.subset_refl]
    let v : XdsState Name := { u with
      phase := .publishedNew
      cache := ⟨u.desiredRefs, u.candidateCds, u.candidateEds,
                u.candidateEdsVersion⟩
      lastGood := ⟨u.desiredRefs, u.candidateCds, u.candidateEds,
                   u.candidateEdsVersion⟩
      krtEvent := .add
      edsWatchState := .idle }
    have happ2 : applyAction u .publishCoherent = some v := by
      simp [applyAction, hclosed, hmatch', u, v]
    have hinv2 : IndInv v := hinv1.preserved rfl happ2
    -- Step 3: Envoy ACKs CDS and opens the named EDS watch. All of the
    -- published cache fields are `s.desiredRefs` by construction.
    let w : XdsState Name := { v with
      phase := .warmingNew
      envoyKnownRefs := v.cache.refs
      envoyKnownCds := v.cache.cds
      envoyRequestedEds := v.cache.cds
      edsWatchState := .opened }
    have happ3 : applyAction v .envoyLearnsCds = some w := by
      simp [applyAction, v, w]
    have hinv3 : IndInv w := hinv2.preserved rfl happ3
    have hrespond : canRespond w.cache.eds w.envoyRequestedEds = true :=
      NameSet.subset_refl _
    by_cases hver : versionEq w.cache.edsVersion w.clientEdsVersion = true
    · -- Envoy's EDS version already matches the published snapshot:
      -- warming completes from its currently held resources.
      let x : XdsState Name := { w with
        phase := .edsResponded, edsWatchState := .responded }
      have happ4 : applyAction w .edsWatchNoChange = some x := by
        simp [applyAction, hver, hrespond, w, x]
      -- Equal versions force set-equal content (versions are digests),
      -- so Envoy's held EDS covers the published references.
      have hsub : NameSet.subset x.envoyKnownRefs x.clientEdsResources
          = true :=
        versionEq_some_subset
          (versionEq_trans (hver) hinv3.clientVerDigest)
      have happ5 : applyAction x .activateNew
          = some { x with
            phase := .activeNew
            envoyActiveRefs := x.envoyKnownRefs
            envoyActiveCds := x.envoyKnownCds
            envoyActiveEds := x.clientEdsResources
            edsWatchState := .idle } := by
        simp [applyAction, NameSet.subset_refl, hsub, x, w, v, u]
      exact ⟨_, .head _ rfl happ1 (.head _ rfl happ2 (.head _ rfl happ3
        (.head _ rfl happ4 (.head _ rfl happ5 (.refl _))))), Or.inl rfl⟩
    · -- Versions differ: the named watch responds with the new EDS.
      have hver' : versionEq w.cache.edsVersion w.clientEdsVersion = false :=
        Bool.eq_false_iff.mpr hver
      let x : XdsState Name := { w with
        phase := .edsResponded
        edsWatchState := .responded
        clientEdsResources := w.cache.eds
        clientEdsVersion := w.cache.edsVersion }
      have happ4 : applyAction w .edsWatchResponds = some x := by
        simp [applyAction, hver', hrespond, w, x]
      have happ5 : applyAction x .activateNew
          = some { x with
            phase := .activeNew
            envoyActiveRefs := x.envoyKnownRefs
            envoyActiveCds := x.envoyKnownCds
            envoyActiveEds := x.clientEdsResources
            edsWatchState := .idle } := by
        simp [applyAction, NameSet.subset_refl, x, w, v, u]
      exact ⟨_, .head _ rfl happ1 (.head _ rfl happ2 (.head _ rfl happ3
        (.head _ rfl happ4 (.head _ rfl happ5 (.refl _))))), Or.inl rfl⟩

end XdsSpec
