These are proposed upstream fixes for the two remaining bugs reviewed at
`kgateway-dev/kgateway` revision `7d930bb2183b90343f9613f03dcd345973fc9f38`
(stack #14817, ending in PR #14721). Apply them in numeric order. Both include
upstream-native regression tests. No Git or GitHub state was changed, and no
production source in the research checkout was edited.

| Patch | Behavior fixed | Implementation evidence |
| --- | --- | --- |
| `0001-reconcile-current-row-under-publication-lock.patch` | An obsolete queued KRT payload could install after reconnect admission replayed a newer deferred row, answering the new connection's watch with old configuration. | Read the current collection row while holding the publication lock. Keep reconnect cleanup, generation adoption, current-row selection and publication in that same critical section. Reconcile delayed deletes and updates against current row existence. Retain the independent install-time generation fence. |
| `0002-reset-carry-episodes-on-coherent-recovery.patch` | A cluster that recovered and disappeared again could inherit its previous carry deadline and be removed immediately. | Clear missing-cluster episode ages on both coherent paths, including a coherent reference-ahead hold. Preserve the ages of clusters that remain absent during partial recovery. |

The first fix orders selection and publication; it does not establish that KRT's
current row is the newest desired Kubernetes configuration, or that a coherent
row is newer than configuration Envoy received from another controller replica.
Explicit-wrapper `syncXds` remains available to existing low-level gate tests;
production collection callbacks use `syncXdsEvents`.

Validation was executed on fresh archive copies with these exact patches applied
using `patch --batch --fuzz=0 -p1`. Applied Go files matched the authored files
byte for byte. `validation.json` records hashes, counts, source identities and
limitations; `evidence/` retains the logs and machine-readable results.

| Check | Result | Evidence level |
| --- | --- | --- |
| New regression tests on pristine upstream | Both reviewed bugs reproduced at their designated assertions; delayed-update/delete cases also failed as expected; partial-recovery control passed. | Go implementation/cache boundary. A recorded test-only adapter exposes the original update/delete callback to the new tests; production files remain unchanged. |
| Complete suites for `proxy_syncer`, `setup`, `krtcollections`, and `endpoints` | 403 top-level tests / 1,114 tests and subtests passed, no skips, with `-race`. Includes six new top-level regression tests. | Go implementation and package integration. |
| All 16 pinned review histories | Passed in three independent `-race -count=1` runs. | Go/KRT/admission/real SnapshotCache, ending at the response channel. |
| Scoped repository lint/format checks | Zero issues in `pkg/kgateway/proxy_syncer`. Run through a Make target using repository lint configuration. | Static checks. |
| Required TLA+ suite | All 57 configurations produced their expected success or designated counterexample; wide ADS remains deferred. | Bounded model checking, including temporal properties under each model's explicit fairness assumptions. |

The unchanged `CarryEpisodesReset` model checks the recovery-reset policy (82
states). The unchanged `QueuedPublicationCapturedOrigin` model checks a different
repair policy: capturing the enqueue generation. It is **not** a model of this
patch's authoritative-row lookup and locking implementation. The queued-history
Go tests and source/lock-order inspection support patch 1; no model-to-Go
refinement theorem is claimed. KRT `GetKey` takes its collection read lock, and
its handler processor uses a separate unbounded notification queue; collection
updates do not synchronously execute the publication callback under that lock.

All historical profiles, fixture hashes, counterexamples and research models
remain intact. The pinned queued-history fixture copies the old producer body;
`queued-history-adapter.json` changes only its two producer calls in scratch to
invoke the new production entry point. Its warm-client classification, queued
event schedule, watch and assertions remain unchanged. `validate-candidate.py`
checks the fixture hash, applies that adapter, verifies all 16 exact test
outcomes, and verifies production Go source hashes did not change during testing.

To apply the series to an ordinary scratch source directory at the pinned base:

```bash
patch --batch --fuzz=0 -p1 -i /absolute/path/0001-reconcile-current-row-under-publication-lock.patch
patch --batch --fuzz=0 -p1 -i /absolute/path/0002-reset-carry-episodes-on-coherent-recovery.patch
```

From that patched source, rerun the complete selected xDS suites:

```bash
CGO_ENABLED=1 make go-test TEST_TAG=e2e \
  TEST_PKG='./pkg/kgateway/proxy_syncer ./pkg/kgateway/setup ./pkg/krtcollections ./pkg/kgateway/endpoints' \
  GO_TEST_ARGS='-timeout=15m -count=1 -race' \
  GOTESTSUM_ARGS='--format=short --jsonfile=/tmp/xds-patched-suite.jsonl'
```

From the research checkout, replay the review histories with the explicitly
recorded producer-entry adapter. The artifacts directory must be new and outside
the candidate source:

```bash
PYTHONDONTWRITEBYTECODE=1 python3 -B \
  devel/formal/patches/stack-14817-7d930/validate-candidate.py \
  --source /absolute/path/to/patched-upstream \
  --artifacts /absolute/path/to/new-scratch-results --repeat 3
```

These checks do not constitute full-repository unit/e2e/image validation or
proof of xDS correctness. No live Envoy was run against these patches; cache
installation and response-channel receipt do not establish wire delivery, ACK,
or applied configuration. Lean was not rerun for this patch-only change; its prior
research receipt is not relabeled as evidence about the proposed upstream code.

During patch development, broad tests found shared metric-counter contamination
from the new tests; their cleanup was corrected. Scoped lint found test formatting
and an import alias that were corrected. The final patch hashes correspond to
the fresh, passing suite, not those intermediate attempts.
