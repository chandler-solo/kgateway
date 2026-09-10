# go-control-plane characterization

Run `CGO_ENABLED=0 go test -tags e2e -count=1 -v ./devel/formal/gcpprobe`.
These tests import the **root go.mod dependency**, currently v0.14.0. They do
not copy or patch cache/server source. The earlier local probes supplied the
watch scenarios; this harness has its own response helpers and stream, joins
its goroutines, and does not need upstream test files or a vet exception.

Passing a defect characterization means the defect is reproduced. On upgrade,
a changed observation must be explained and mapped to a repaired expectation;
do not simply delete or relax the assertion to keep the suite green.

| Test | Observation | Model or open action |
|---|---|---|
| `TestParkedNamedWatchIsDiscardedOnDeclinedResponse` | Decline deletes waiter; realignment alone does not deliver; a new request recovers | `GcpWatch.lean`, RF-007 |
| `TestDeclinedNewRequestRegistersNoWatch` | Immediate decline leaves no waiter | `GcpWatch.lean`, RF-007 |
| `TestEqualVersionRespondsForNewlySubscribedResource` | Same version can answer unreturned B | `GcpWatch.lean`, reopened GCP-A1 |
| `TestWildcardWatchIsNeverDeclined` | This legacy wildcard fixture bypasses named superset check | Wildcard/unsubscribe history still open |
| `TestInstallationSurvivesPartialResponseFailure` | CDS answered, EDS blocked/canceled, new cache remains, EDS waiter retained | RF-007, multi-stage installation model open |
| `TestFullImmediateChannelBlocksOtherNode` | Full immediate channel holds cache-wide lock; draining releases both operations | RF-011, wrapper queue-capacity audit open |
| `TestRepeatedNackResendsSameVersionAndCorrectionRecovers` | 32 matching NACKs resend rejected version; changed snapshot recovers | RF-012, damping/isolation policy open |

Synchronous cache calls permit immediate queue inspection for non-delivery;
no sleep is needed for declined-watch silence. The lock probe uses a source
entry signal followed by a bounded 100 ms absence observation, then drains
and joins. It is a characterization, not a proof of all scheduler behavior.
The server probe deliberately makes no CPU-rate or real-Envoy claim.

The finite named-watch model has A always present/requested, optional B,
three revision values, response ownership, and returned-B history. It explores
both v0.14.0 discard and a proposed retention policy. The latter preserves
waiter accounting but is not an implementation fix or full progress guarantee.
Retention cannot make an incompatible snapshot eligible; new revision/valid
subscription and eventual execution are separate obligations. Model versions
wrap in the finite domain; RF-008 still governs production digest semantics.
