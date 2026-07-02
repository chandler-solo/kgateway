/-
XdsSpec.ClientIdentity: per-request xDS client-identity re-derivation
(PR #14244, pkg/krtcollections/uniqueclients.go).

A stream's identity (UniqlyConnectedClient resource name =
role~hash(AugmentedLabels)~namespace) is derived from a point-in-time
pod-informer read OUTSIDE KRT dependency tracking — nothing re-runs the
derivation when the pod record changes. Before the PR, the identity was
derived once, on the stream's first request, and frozen for the stream's
whole lifetime; a first request racing the pod/node informers (controller
start is exactly when every Envoy reconnects) froze an identity built from
incomplete labels/locality — wrong DestinationRule selection and failover
priorities with nothing to ever correct it short of an Envoy restart.

The PR re-derives the identity from current pod state on EVERY request:

  - genuine drift (the re-derived name differs) CLOSES the stream; Envoy
    reconnects with backoff and re-identifies against current state, and
    OnStreamClosed releases the OLD identity's refcount;
  - a transient derivation failure (pod record briefly absent) keeps the
    established identity rather than churn the stream;
  - re-derivation starts from the stream's pinned ORIGINAL role, never from
    the request node's role: newStream rewrites the node's role in place to
    the unique cache key, and go-control-plane reuses that mutated Node
    object for follow-up SotW requests that omit Node — deriving from it
    would re-augment the already-augmented role and close the stream on
    every ACK.

This model checks the identity/refcount algebra exhaustively over two
streams and a lagging informer view, and keeps each rejected or pre-PR
design as a counterexample system:

  safeSystem                   PR semantics; all invariants + heal/stability
                               liveness hold.
  frozenIdentityBugSystem      pre-PR: identity frozen at first request;
                               a stale identity never heals (liveness).
  reaugmentFalseCloseBugSystem re-derivation from the mutated (augmented)
                               role: every ACK is a false identity change —
                               a reconnect storm; the stream never serves
                               two consecutive requests (liveness).
  quietStreamStuckBugSystem    the PR's DISCLOSED limitation: re-derivation
                               only runs when a DiscoveryRequest arrives, so
                               a stream that receives nothing never heals
                               (liveness). Remediation would need a
                               server-side request source (watchdog /
                               keepalive / MaxConnectionAge) — declared a
                               non-goal in the PR.
  blipCloseBugSystem           the rejected transient-failure design: close
                               on derivation failure. Under a PERMANENT pod-
                               record absence (informer wedge, discovery-
                               namespace filter change) the stream tears
                               down and no reconnect can ever re-identify —
                               permanent outage, where the PR's keep choice
                               serves the established config (liveness).
  driftMiscountBugSystem       teeth for CountsMatchStreams: a drift close
                               that releases the refcount of the freshly-
                               derived name instead of the established one
                               (safety).

Model assumptions, discharged on the Go side:

  - Name determinism: utils.HashLabels is an XOR fold of independent
    per-entry FNV-64 hashes — deterministic and iteration-order-independent,
    so a resource name never changes without a genuine label change (false
    drift is impossible at the hash layer). Locality is folded INTO
    AugmentedLabels by augmentPodLabels/AugmentLabels, so the name covers
    every identity-relevant field.
  - Per-stream callback serialization (assumption GCP-A4): add() reads the
    per-stream entry under RLock, derives WITHOUT the lock, then mutates
    under Lock; this check-then-act is sound only because go-control-plane
    calls OnStreamRequest/OnStreamClosed for one stream strictly
    sequentially (sotw process loop; OnStreamClosed deferred). Pinned by
    TestADSCallbacksAreSerializedPerStream.
-/
import XdsSpec.Checker

namespace XdsSpec.ClientIdentity

open XdsSpec

/-- A client identity (resource name). `true` is the identity derived from
the pod's full augmented labels — the pod's REAL identity, fixed for the
whole run; `false` is the identity derived from the incomplete informer view
at connect time. Fixing the truth and letting the VIEW lag it also covers a
live-pod label change: a relabel is a new truth with the old identity still
established — the same shape as the startup race. -/
abbrev Ident := Bool

def freshIdent : Ident := true

structure StreamSt where
  /-- uniqueClientName: the identity the stream established under (pinned in
  ConnectedClient together with the original role; also the snapshot cache
  key, which is why an established identity cannot be changed in place). -/
  ident : Ident
  /-- Ghost: some follow-up re-derivation matched the established identity —
  the stream demonstrably serves stably (no reconnect storm). -/
  cleanAck : Bool
  deriving DecidableEq, Repr, Hashable

structure CIState where
  /-- The pod informer's view of the pod: `none` = record absent (blip, or
  permanently lost); `some i` = deriveClientIdentity yields identity `i`.
  The view only ever syncs TOWARD the fixed truth — informers do not
  regress. -/
  view : Option Ident
  /-- Two stream slots (`none` = disconnected; Envoy retries with backoff).
  Two streams over the same identity exercise the shared-refcount paths
  (same-labeled pods dedup into one UniqlyConnectedClient). -/
  s1 : Option StreamSt
  s2 : Option StreamSt
  /-- The Go code's EXPLICIT refcounts (uniqClientsCount), kept separately
  from the streams so the checker can falsify the add/del algebra. The
  UniqueConnectedClients collection content is their support. -/
  countStale : Nat
  countFresh : Nat
  /-- Ghost: a decrement hit a zero count (Go: uint64 underflow). -/
  underflow : Bool
  deriving DecidableEq, Repr, Hashable

/-- The startup race: the pod's true labels are `freshIdent`, but the
informer still surfaces the incomplete view. -/
def init : CIState where
  view := some false
  s1 := none
  s2 := none
  countStale := 0
  countFresh := 0
  underflow := false

def getCount (s : CIState) (i : Ident) : Nat :=
  if i then s.countFresh else s.countStale

def setCount (s : CIState) (i : Ident) (n : Nat) : CIState :=
  if i then { s with countFresh := n } else { s with countStale := n }

def incCount (s : CIState) (i : Ident) : CIState :=
  setCount s i (getCount s i + 1)

/-- del: decrement, recording an underflow instead of saturating silently. -/
def decCount (s : CIState) (i : Ident) : CIState :=
  match getCount s i with
  | 0 => { setCount s i 0 with underflow := true }
  | n + 1 => setCount s i n

inductive Slot
  | one | two
  deriving DecidableEq, Repr, Hashable

def getSlot (s : CIState) : Slot → Option StreamSt
  | .one => s.s1
  | .two => s.s2

def setSlot (s : CIState) (k : Slot) (v : Option StreamSt) : CIState :=
  match k with
  | .one => { s with s1 := v }
  | .two => { s with s2 := v }

inductive CIAction
  /-- The informer catches up to the pod's true labels. -/
  | informerSync
  /-- The pod record drops out of the informer view. -/
  | informerBlip
  /-- First request on a fresh stream (add, new-stream path): derive from
  the CURRENT view and publish the identity into the refcounted maps. A
  failed derivation closes the stream before any state is recorded, so it
  is no transition at all. -/
  | connect (k : Slot)
  /-- Follow-up request on an established stream (the PR): re-derive from
  the pinned original role + current pod state. Genuine drift closes the
  stream and del releases the ESTABLISHED identity's refcount; a matching
  derivation is a clean ACK; a transient derivation failure keeps the
  established identity (no transition). -/
  | ackRederive (k : Slot)
  /-- The stream drops for stream-level reasons (network, Envoy restart):
  OnStreamClosed → del. -/
  | netClose (k : Slot)
  /-- Pre-PR bug: the follow-up does NOT re-derive; the connect-time
  identity serves for the stream's whole lifetime. -/
  | buggyAckFrozen (k : Slot)
  /-- The false close the pinned original role prevents: re-derivation
  starts from the ALREADY-AUGMENTED role, so even with NO pod drift every
  ACK derives a different name and closes the stream. -/
  | buggyAckReaugment (k : Slot)
  /-- The rejected transient-failure design: a derivation failure on an
  established stream closes it instead of keeping the identity. -/
  | buggyAckBlipClose (k : Slot)
  /-- A plausible mis-implementation of the drift close: del releases the
  refcount of the NEWLY-derived identity instead of the established one
  (using the fresh ucc where the stored ConnectedClient is required). -/
  | buggyAckDriftMiscount (k : Slot)
  deriving DecidableEq, Repr, Hashable

def step (s : CIState) : CIAction → Option CIState
  | .informerSync =>
    if s.view == some freshIdent then none
    else some { s with view := some freshIdent }
  | .informerBlip =>
    if s.view == none then none else some { s with view := none }
  | .connect k =>
    match getSlot s k, s.view with
    | none, some i => some (incCount (setSlot s k (some ⟨i, false⟩)) i)
    | _, _ => none
  | .ackRederive k =>
    match getSlot s k, s.view with
    | some st, some i =>
      if i == st.ident then
        if st.cleanAck then none
        else some (setSlot s k (some { st with cleanAck := true }))
      else
        some (decCount (setSlot s k none) st.ident)
    | _, _ => none
  | .netClose k =>
    match getSlot s k with
    | some st => some (decCount (setSlot s k none) st.ident)
    | none => none
  | .buggyAckFrozen k =>
    match getSlot s k with
    | some st =>
      if st.cleanAck then none
      else some (setSlot s k (some { st with cleanAck := true }))
    | none => none
  | .buggyAckReaugment k =>
    match getSlot s k, s.view with
    | some st, some i =>
      if i == st.ident then some (decCount (setSlot s k none) st.ident)
      else none
    | _, _ => none
  | .buggyAckBlipClose k =>
    match getSlot s k, s.view with
    | some st, none => some (decCount (setSlot s k none) st.ident)
    | _, _ => none
  | .buggyAckDriftMiscount k =>
    match getSlot s k, s.view with
    | some st, some i =>
      if i == st.ident then none
      else some (decCount (setSlot s k none) i)
    | _, _ => none

def describeSlot : Slot → String
  | .one => "1"
  | .two => "2"

def describe : CIAction → String
  | .informerSync => "InformerSync"
  | .informerBlip => "InformerBlip"
  | .connect k => s!"Connect{describeSlot k}"
  | .ackRederive k => s!"AckRederive{describeSlot k}"
  | .netClose k => s!"NetClose{describeSlot k}"
  | .buggyAckFrozen k => s!"BuggyAckFrozen{describeSlot k}"
  | .buggyAckReaugment k => s!"BuggyAckReaugment{describeSlot k}"
  | .buggyAckBlipClose k => s!"BuggyAckBlipClose{describeSlot k}"
  | .buggyAckDriftMiscount k => s!"BuggyAckDriftMiscount{describeSlot k}"

def mkSystem (name : String) (actions : List CIAction) :
    System CIState CIAction where
  name := name
  init := init
  actions := actions
  step := step
  describeAction := describe

-- MARK: invariants

def streamCount (s : CIState) (i : Ident) : Nat :=
  (match s.s1 with
   | some st => if st.ident == i then 1 else 0
   | none => 0) +
  (match s.s2 with
   | some st => if st.ident == i then 1 else 0
   | none => 0)

/-- The Go algebra: uniqClientsCount[name] equals the number of open streams
established under that name — over every interleaving of connects, clean
ACKs, drift closes, and network closes, including two streams sharing one
identity. The collection content (uniqClients) is the support of the counts,
so this is also "the collection tracks exactly the connected identities". -/
def countsMatchStreams (s : CIState) : Bool :=
  s.countStale == streamCount s false && s.countFresh == streamCount s true

def noUnderflow (s : CIState) : Bool :=
  !s.underflow

def invariantList : List (String × (CIState → Bool)) :=
  [ ("CountsMatchStreams", countsMatchStreams),
    ("NoUnderflow", noUnderflow) ]

-- MARK: liveness predicates

/-- Stream 1 serves under a stale identity while the informer already
surfaces the pod's true state — the healable form of the startup race. -/
def staleServing1 (s : CIState) : Bool :=
  match s.s1 with
  | some st => st.ident != freshIdent && s.view == some freshIdent
  | none => false

def s1Fresh (s : CIState) : Bool :=
  match s.s1 with
  | some st => st.ident == freshIdent
  | none => false

def s1Established (s : CIState) : Bool :=
  s.s1.isSome

def s1CleanAck (s : CIState) : Bool :=
  match s.s1 with
  | some st => st.cleanAck
  | none => false

/-- Stream 1 is down while the pod record is absent from the view. -/
def disconnectedNoView1 (s : CIState) : Bool :=
  s.s1 == none && s.view == none

-- MARK: systems

/-- PR #14244 semantics over two streams. -/
def safeSystem : System CIState CIAction :=
  mkSystem "ClientIdentitySafe"
    [ .informerSync, .informerBlip,
      .connect .one, .connect .two,
      .ackRederive .one, .ackRederive .two,
      .netClose .one, .netClose .two ]

/-- Pre-PR (and current-main) semantics: the identity is frozen on the first
request. `netClose` is deliberately absent — the heal must not depend on the
stream happening to die for unrelated reasons. Expected: a stale identity
never heals (`StaleServing ~> FreshIdentity` violated). -/
def frozenIdentityBugSystem : System CIState CIAction :=
  mkSystem "FrozenIdentityBug"
    [ .informerSync, .informerBlip, .connect .one, .buggyAckFrozen .one ]

/-- Re-derivation from the mutated node role (what the pinned original role
prevents): every ACK reads as an identity change and closes the stream, so
the client reconnects forever without ever serving two consecutive requests.
Expected: `Established ~> CleanAck` violated. The refcount algebra stays
sound — the damage is pure churn. -/
def reaugmentFalseCloseBugSystem : System CIState CIAction :=
  mkSystem "ReaugmentFalseCloseBug"
    [ .informerSync, .connect .one, .buggyAckReaugment .one ]

/-- The PR's disclosed limitation, kept as a formal counterexample: identity
re-derivation runs only when a DiscoveryRequest arrives. A stream that
receives nothing — quiet cluster between config pushes, or an identity so
stale nothing is ever published for it — has no ACKs, so the drift is never
observed. Expected: `StaleServing ~> FreshIdentity` violated. On a live
cluster the heal latency is bounded only by the next config push. -/
def quietStreamStuckBugSystem : System CIState CIAction :=
  mkSystem "QuietStreamStuckBug"
    [ .informerSync, .informerBlip, .connect .one ]

/-- The rejected transient-failure design: close on derivation failure.
`informerSync` is deliberately absent — the pod record's absence is
PERMANENT (wedged informer, discovery-namespace filter change), which is
exactly when this design and the PR's keep-established choice diverge.
Expected: once the stream is torn down with no view, nothing can ever
re-identify (`Disconnected ~> Established` violated) — a permanent outage
where keeping the established identity would have kept serving. -/
def blipCloseBugSystem : System CIState CIAction :=
  mkSystem "BlipCloseBug"
    [ .informerBlip, .connect .one, .ackRederive .one,
      .buggyAckBlipClose .one ]

/-- Teeth for CountsMatchStreams: release the freshly-derived identity's
refcount on a drift close, instead of the established one recorded in
ConnectedClient. Expected: safety violation of CountsMatchStreams (the
stale count leaks at 1 forever and the fresh count underflows). -/
def driftMiscountBugSystem : System CIState CIAction :=
  mkSystem "DriftMiscountBug"
    [ .informerSync, .connect .one, .buggyAckDriftMiscount .one ]

end XdsSpec.ClientIdentity
