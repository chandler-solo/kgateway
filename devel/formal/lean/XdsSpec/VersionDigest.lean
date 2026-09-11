/-
XdsSpec.VersionDigest: payload revisions and the version-digest contract
(RF-008, assumption IMPL-A1).

`Spec.lean` models an xDS version as `Option (List Name)`: the set of EDS
resource names. That abstraction supports the closure and named-response
proofs, but it forgets payload changes: a ClusterLoadAssignment whose
endpoints change while its name stays the same has the same name-set
version. The implementation (`filterEndpointResourcesForClusters`) versions
EDS by XOR-combining a 64-bit FNV-1a digest of each deterministically
marshalled CLA, so it does see payload changes, subject to collisions.

This module records that gap honestly:

  - `Resource`/`Content` add a payload revision to each named resource.
  - `nameSetVersion_blind` proves the Spec abstraction cannot distinguish a
    same-name payload change. Anything the Spec proves about versions is a
    statement about name sets only.
  - `DigestContract` states what the implementation's version function must
    satisfy for the run being checked: determinism on content-equal sets and
    collision freedom on an explicitly named domain. Both are assumptions
    about the concrete hash on the finite set of contents that a run
    compares. No finite-width digest is injective over unbounded content
    (pigeonhole); the contract therefore carries a `domain` rather than
    claiming global injectivity.
  - `payload_change_changes_digest` and `content_stable_keeps_digest` are the
    two directions the trace checker enforces per client at runtime
    (`version-reuse` and `version-churn` in TraceCheck.lean), using the same
    per-resource digests the implementation XORs.
  - `xorDigest` is the implementation's combiner. `xorDigest_pair_comm` shows
    it is order independent for a pair; `xorDigest_cancels` shows that two
    resources with equal per-resource digests contribute nothing, so their
    set shares a version with the empty set. That is a structural collision
    of the combiner, independent of FNV-1a quality.

Nothing here proves the Go hash collision free. The finite corpus test
`TestFilterEndpointResourcesForClusters_VersionDigestProperties` is
characterization; the trace rule is runtime detection on observed runs.
-/
import XdsSpec.Spec

namespace XdsSpec.Digest

open XdsSpec

/-- An EDS resource with a payload revision standing for endpoint content. -/
structure Resource (Name : Type) where
  name : Name
  revision : Nat
  deriving DecidableEq, Repr, Hashable

abbrev Content (Name : Type) := List (Resource Name)

def names (c : Content Name) : List Name := c.map (·.name)

/-- Content equality as set equality of (name, revision) pairs. -/
def contentEq [DecidableEq Name] (a b : Content Name) : Bool :=
  a.all (b.contains ·) && b.all (a.contains ·)

/-- The `Spec.lean` version abstraction applied to revised content. -/
def nameSetVersion (c : Content Name) : Version Name := some (names c)

/-- Name-set versions are blind to a same-name payload change. -/
theorem nameSetVersion_blind [DecidableEq Name] (n : Name) (r r' : Nat) :
    versionEq (nameSetVersion [⟨n, r⟩]) (nameSetVersion [⟨n, r'⟩]) = true := by
  simp [nameSetVersion, names, versionEq, NameSet.eq, NameSet.subset]

/-- ...while the revised contents differ whenever the revisions do. -/
theorem contentEq_revision_sensitive [DecidableEq Name] (n : Name) (r r' : Nat) (h : r ≠ r') :
    contentEq [Resource.mk n r] [Resource.mk n r'] = false := by
  simp [contentEq, h]

/-- What a version function must satisfy on the contents a run compares.
`domain` names that finite set; injectivity is assumed only there. -/
structure DigestContract (Name D : Type) [DecidableEq Name] [DecidableEq D] where
  digest : Content Name → D
  domain : Content Name → Prop
  /-- Determinism: content-equal sets get equal digests regardless of order
  or proto instance identity. -/
  sound : ∀ a b, contentEq a b = true → digest a = digest b
  /-- Collision freedom on the domain. This is the assumption IMPL-A1
  actually needs; it cannot hold for every content over a finite `D`. -/
  injectiveOn : ∀ a b, domain a → domain b → digest a = digest b → contentEq a b = true

section Contract
variable {Name D : Type} [DecidableEq Name] [DecidableEq D]

/-- A payload change within the domain changes the version. Trace rule
`version-reuse` checks the contrapositive on observed publications. -/
theorem payload_change_changes_digest (k : DigestContract Name D) (a b : Content Name)
    (ha : k.domain a) (hb : k.domain b) (hne : contentEq a b = false) :
    k.digest a ≠ k.digest b := by
  intro h
  have := k.injectiveOn a b ha hb h
  simp_all

/-- Unchanged content keeps the version. Trace rule `version-churn` checks
this on observed publications; a violation is a spurious xDS push. -/
theorem content_stable_keeps_digest (k : DigestContract Name D) (a b : Content Name)
    (heq : contentEq a b = true) : k.digest a = k.digest b :=
  k.sound a b heq

end Contract

/-- The implementation's combiner: XOR of per-resource digests. -/
def xorDigest (h : Resource Name → UInt64) (c : Content Name) : UInt64 :=
  c.foldl (fun acc r => acc ^^^ h r) 0

theorem xorDigest_pair_comm (h : Resource Name → UInt64) (a b : Resource Name) :
    xorDigest h [a, b] = xorDigest h [b, a] := by
  simp [xorDigest, List.foldl, UInt64.xor_comm]

/-- Two resources with equal per-resource digests cancel: their set has the
same combined version as the empty set. -/
theorem xorDigest_cancels (h : Resource Name → UInt64) (a b : Resource Name)
    (hab : h a = h b) : xorDigest h [a, b] = xorDigest h [] := by
  simp [xorDigest, List.foldl, hab]

-- Concrete illustration with a synthetic per-resource digest: content differs
-- but the combined version is equal. This is a property of XOR combining,
-- not a claim about FNV-1a.
private def toy : Resource String → UInt64
  | ⟨"a", _⟩ => 1
  | ⟨"b", _⟩ => 2
  | ⟨"c", _⟩ => 3
  | _ => 0

#guard xorDigest toy [⟨"a", 0⟩, ⟨"b", 0⟩] == xorDigest toy [⟨"c", 0⟩]
#guard contentEq [Resource.mk "a" 0, ⟨"b", 0⟩] [⟨"c", 0⟩] == false
#guard contentEq [Resource.mk "a" 0, ⟨"b", 0⟩] [⟨"b", 0⟩, ⟨"a", 0⟩] == true

end XdsSpec.Digest
