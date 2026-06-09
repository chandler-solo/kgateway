/-
XdsSpec.Proofs: unbounded safety proofs for the convergence spec.

TLC checks the TLA+ model only for the fixed two-name instance. Here we
prove, by induction over the reachable states, that every safety
invariant holds for the *parameterized* spec: any resource-name type,
any initially-served set, and any action parameters (any episode of
"replace the served set with some new set"). This removes the bounded-
cardinality caveat from the TLA+ result.

Structure:
  - `NameSet` lemmas: boolean subset/eq behave like set inclusion.
  - `IndInv`: the strengthened inductive invariant. It adds to the
    checked invariants the auxiliary facts that make induction go
    through (cache always equals last-good in the safe spec, versions
    are digests of their content, Envoy's named EDS request tracks its
    CDS knowledge).
  - `IndInv.init` / `IndInv.preserved`: the induction base and step.
  - `safety`: the headline theorem — every reachable state of the safe
    spec satisfies `safetyInvariants`.
-/
import XdsSpec.Spec

namespace XdsSpec

variable {Name : Type} [DecidableEq Name]

-- MARK: NameSet lemmas

namespace NameSet

theorem subset_iff {a b : List Name} :
    subset a b = true ↔ ∀ x ∈ a, x ∈ b := by
  simp [subset]

theorem subset_refl (a : List Name) : subset a a = true :=
  subset_iff.mpr fun _ h => h

theorem subset_trans {a b c : List Name}
    (hab : subset a b = true) (hbc : subset b c = true) :
    subset a c = true :=
  subset_iff.mpr fun x hx => subset_iff.mp hbc x (subset_iff.mp hab x hx)

theorem eq_iff {a b : List Name} :
    eq a b = true ↔ subset a b = true ∧ subset b a = true := by
  simp [eq]

theorem eq_refl (a : List Name) : eq a a = true :=
  eq_iff.mpr ⟨subset_refl a, subset_refl a⟩

theorem eq_symm {a b : List Name} (h : eq a b = true) : eq b a = true :=
  eq_iff.mpr ⟨(eq_iff.mp h).2, (eq_iff.mp h).1⟩

theorem eq_trans {a b c : List Name}
    (hab : eq a b = true) (hbc : eq b c = true) : eq a c = true :=
  eq_iff.mpr
    ⟨subset_trans (eq_iff.mp hab).1 (eq_iff.mp hbc).1,
     subset_trans (eq_iff.mp hbc).2 (eq_iff.mp hab).2⟩

theorem subset_of_eq {a b : List Name} (h : eq a b = true) :
    subset a b = true := (eq_iff.mp h).1

/-- Rewriting a subset along set equalities on both sides. -/
theorem subset_congr {a a' b b' : List Name}
    (ha : eq a a' = true) (hb : eq b b' = true)
    (h : subset a b = true) : subset a' b' = true :=
  subset_trans (eq_iff.mp ha).2 (subset_trans h (eq_iff.mp hb).1)

end NameSet

-- MARK: version lemmas

theorem versionEq_refl (v : Version Name) : versionEq v v = true := by
  cases v <;> simp [versionEq, NameSet.eq_refl]

theorem versionEq_symm {v w : Version Name}
    (h : versionEq v w = true) : versionEq w v = true := by
  cases v <;> cases w <;> simp_all [versionEq, NameSet.eq_symm]

theorem versionEq_trans {u v w : Version Name}
    (huv : versionEq u v = true) (hvw : versionEq v w = true) :
    versionEq u w = true := by
  cases u <;> cases v <;> cases w <;>
    simp_all [versionEq] <;> exact NameSet.eq_trans huv hvw

theorem snapshotDataEq_refl (d : SnapshotData Name) :
    snapshotDataEq d d = true := by
  simp [snapshotDataEq, NameSet.eq_refl, versionEq_refl]

-- MARK: safe steps and reachability

/-- The safe transitions: TLA+ `SafeNext` (any parameters). -/
def Action.isSafe : Action Name → Bool
  | .deferPartialInput .. => true
  | .inputBecomesCoherent .. => true
  | .publishCoherent => true
  | .envoyLearnsCds => true
  | .edsWatchResponds => true
  | .activateNew => true
  | .beginNextEpisode => true
  | .observeConverged => true
  | _ => false

/-- Reachability under safe actions from a generalized initial state. -/
inductive Reachable (old : List Name) : XdsState Name → Prop
  | init : Reachable old (initState old)
  | step {s s' : XdsState Name} (a : Action Name)
      (hsafe : a.isSafe = true)
      (hr : Reachable old s)
      (happ : applyAction s a = some s') : Reachable old s'

/--
The strengthened inductive invariant. The last four conjuncts are the
auxiliary facts that do not appear in the TLA+ INVARIANTS list but are
needed for the induction to close:

  - in the safe spec the cache is only ever overwritten together with
    last-good, so they always agree;
  - Envoy's named EDS request always equals its CDS knowledge (it asks
    for exactly the clusters it knows);
  - every version in flight is a digest of the EDS content it was
    computed from (assumption IMPL-A1 maps this to the implementation's
    hash-based versions).
-/
structure IndInv (s : XdsState Name) : Prop where
  cacheLastGood : snapshotDataEq s.cache s.lastGood = true
  cacheIsClosed : cacheClosed s = true
  activeIsClosed : activeClosed s = true
  reqEdsAligned : NameSet.eq s.envoyRequestedEds s.envoyKnownCds = true
  cacheVerDigest : versionEq s.cache.edsVersion (some s.cache.eds) = true
  clientVerDigest :
    versionEq s.clientEdsVersion (some s.clientEdsResources) = true
  candVerDigest : s.computedState = .coherent →
    versionEq s.candidateEdsVersion (some s.candidateEds) = true

theorem IndInv.init (old : List Name) : IndInv (initState old : XdsState Name) where
  cacheLastGood := snapshotDataEq_refl _
  cacheIsClosed := by
    simp [cacheClosed, initState, NameSet.subset_refl]
  activeIsClosed := by
    simp [activeClosed, initState, NameSet.subset_refl]
  reqEdsAligned := by simp [initState, NameSet.eq_refl]
  cacheVerDigest := by simp [initState, versionEq, NameSet.eq_refl]
  clientVerDigest := by simp [initState, versionEq, NameSet.eq_refl]
  candVerDigest := by simp [initState, versionEq, NameSet.eq_refl]

theorem IndInv.preserved {s s' : XdsState Name} {a : Action Name}
    (hsafe : a.isSafe = true) (hinv : IndInv s)
    (happ : applyAction s a = some s') : IndInv s' := by
  obtain ⟨h1, h2, h3, h4, h5, h6, h7⟩ := hinv
  cases a with
  | deferPartialInput newRefs newCds =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      exact {
        cacheLastGood := h1, cacheIsClosed := h2, activeIsClosed := h3
        reqEdsAligned := h4, cacheVerDigest := h5, clientVerDigest := h6
        candVerDigest := by intro h; simp at h }
  | inputBecomesCoherent newEds =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      exact {
        cacheLastGood := h1, cacheIsClosed := h2, activeIsClosed := h3
        reqEdsAligned := h4, cacheVerDigest := h5, clientVerDigest := h6
        candVerDigest := fun _ => versionEq_refl _ }
  | publishCoherent =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      simp only [Bool.and_eq_true, Bool.not_eq_true', beq_iff_eq] at hguard
      obtain ⟨⟨hcoherent, hclosed⟩, _hdiff⟩ := hguard
      simp only [candidateClosed, Bool.and_eq_true] at hclosed
      obtain ⟨⟨hrc, hre⟩, hec⟩ := hclosed
      exact {
        cacheLastGood := snapshotDataEq_refl _
        cacheIsClosed := by
          simp only [cacheClosed, Bool.and_eq_true]
          exact ⟨⟨hrc, hre⟩, hec⟩
        activeIsClosed := h3
        reqEdsAligned := h4
        cacheVerDigest := h7 hcoherent
        clientVerDigest := h6
        candVerDigest := fun _ => h7 hcoherent }
  | envoyLearnsCds =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      exact {
        cacheLastGood := h1, cacheIsClosed := h2, activeIsClosed := h3
        reqEdsAligned := NameSet.eq_refl _
        cacheVerDigest := h5, clientVerDigest := h6, candVerDigest := h7 }
  | edsWatchResponds =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      exact {
        cacheLastGood := h1, cacheIsClosed := h2, activeIsClosed := h3
        reqEdsAligned := h4, cacheVerDigest := h5
        clientVerDigest := h5
        candVerDigest := h7 }
  | activateNew =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      simp only [Bool.and_eq_true, beq_iff_eq] at hguard
      obtain ⟨⟨_hphase, hrc⟩, hrce⟩ := hguard
      exact {
        cacheLastGood := h1, cacheIsClosed := h2
        activeIsClosed := by
          simp only [activeClosed, Bool.and_eq_true]
          exact ⟨hrc, hrce⟩
        reqEdsAligned := h4, cacheVerDigest := h5, clientVerDigest := h6
        candVerDigest := h7 }
  | beginNextEpisode =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      exact {
        cacheLastGood := h1, cacheIsClosed := h2, activeIsClosed := h3
        reqEdsAligned := h4, cacheVerDigest := h5, clientVerDigest := h6
        candVerDigest := h7 }
  | observeConverged =>
    simp only [applyAction] at happ
    split at happ
    case isFalse => exact absurd happ (by simp)
    case isTrue hguard =>
      cases happ
      exact {
        cacheLastGood := h1, cacheIsClosed := h2, activeIsClosed := h3
        reqEdsAligned := h4, cacheVerDigest := h5, clientVerDigest := h6
        candVerDigest := h7 }
  | buggyClearCacheOnDelete => simp [Action.isSafe] at hsafe
  | buggyPublishPartial => simp [Action.isSafe] at hsafe
  | buggyPublishStaleEds => simp [Action.isSafe] at hsafe
  | buggyPublishWithoutEdsVersionChange => simp [Action.isSafe] at hsafe
  | buggyActivateBeforeEds => simp [Action.isSafe] at hsafe

theorem IndInv.of_reachable {old : List Name} {s : XdsState Name}
    (h : Reachable old s) : IndInv s := by
  induction h with
  | init => exact IndInv.init old
  | step a hsafe _ happ ih => exact ih.preserved hsafe happ

-- MARK: deriving the checked invariants from IndInv

/-- `CoherentInputCanPublish` holds in *every* state, not just reachable
ones: the publish guard is literally the premise of the invariant. -/
theorem coherentInputCanPublish_total (s : XdsState Name) :
    coherentInputCanPublish s = true := by
  simp only [coherentInputCanPublish, applyAction, Bool.or_eq_true,
    Bool.not_eq_true']
  rcases h : (s.computedState == ComputedState.coherent && candidateClosed s
      && !cacheMatchesCandidate s) with _ | _
  · exact Or.inl rfl
  · exact Or.inr (by simp)

theorem IndInv.implies_safety {s : XdsState Name} (h : IndInv s) :
    safetyInvariants s = true := by
  obtain ⟨h1, h2, h3, h4, h5, h6, h7⟩ := h
  have hcacheClosed := h2
  simp only [cacheClosed, Bool.and_eq_true] at hcacheClosed
  obtain ⟨⟨_hrc, _hre⟩, hec⟩ := hcacheClosed
  simp only [safetyInvariants, Bool.and_eq_true]
  refine ⟨⟨⟨⟨⟨⟨?_, ?_⟩, ?_⟩, ?_⟩, ?_⟩, ?_⟩, ?_⟩
  · -- DeleteRetainsLastGood
    simp only [deleteRetainsLastGood, cacheMatchesLastGood, Bool.or_eq_true]
    exact Or.inr h1
  · -- PartialDoesNotOverwriteCache
    simp only [partialDoesNotOverwriteCache, cacheMatchesLastGood,
      Bool.or_eq_true]
    exact Or.inr h1
  · -- CacheSnapshotClosed
    exact h2
  · -- AlignedEDSRequestRespondable
    simp only [alignedEdsRequestRespondable, Bool.or_eq_true,
      Bool.not_eq_true']
    rcases haligned : NameSet.eq s.envoyKnownCds s.cache.cds with _ | _
    · exact Or.inl rfl
    · -- cache.eds ⊆ cache.cds ≈ envoyKnownCds ≈ envoyRequestedEds
      refine Or.inr (NameSet.subset_congr (NameSet.eq_refl _) ?_ hec)
      exact NameSet.eq_trans (NameSet.eq_symm haligned) (NameSet.eq_symm h4)
  · -- EDSResourceSetChangeChangesVersion
    simp only [edsResourceSetChangeChangesVersion, Bool.or_eq_true,
      Bool.not_eq_true']
    rcases hv : versionEq s.cache.edsVersion s.clientEdsVersion with _ | _
    · exact Or.inr rfl
    · -- versions are digests, so equal versions force equal content
      refine Or.inl ?_
      have : versionEq (some s.cache.eds) (some s.clientEdsResources) = true :=
        versionEq_trans (versionEq_symm h5) (versionEq_trans hv h6)
      simpa [versionEq] using this
  · -- ActiveSnapshotClosed
    exact h3
  · -- CoherentInputCanPublish
    exact coherentInputCanPublish_total s

/--
Headline theorem: every state reachable under the safe transitions from
any generalized initial state satisfies all checked safety invariants —
for any name universe and any action parameters. This is the unbounded
counterpart of TLC's bounded check of
devel/formal/tla/XdsPerClientConvergence.cfg.
-/
theorem safety {old : List Name} {s : XdsState Name}
    (h : Reachable old s) : safetyInvariants s = true :=
  (IndInv.of_reachable h).implies_safety

end XdsSpec
