# go-control-plane characterization

Run `CGO_ENABLED=0 go test -tags e2e -count=1 -v ./devel/formal/gcpprobe`.
These tests import the **root go.mod dependency**, currently
`v0.14.1-0.20260702184136-1cd1226616f5` (upstream `1cd122661`, #1498, which
includes the #1356 cache response rewrite; adopted by upstream kgateway
#14654 and merged here on 2026-09-12). Before that merge the pin was v0.14.0.
They do not copy or patch cache/server source. The earlier local probes supplied the
watch scenarios; this harness has its own response helpers and stream, joins
its goroutines, and does not need upstream test files or a vet exception.

Passing a defect characterization means the defect is reproduced. On upgrade,
a changed observation must be explained and mapped to a repaired expectation;
do not simply delete or relax the assertion to keep the suite green.

| Test | Observation | Model or open action |
|---|---|---|
| `TestParkedNamedWatchIsRetainedOnDeclinedResponse` | Decline retains the waiter; the next aligned snapshot is delivered without a new request (repaired since the pin moved; v0.14.0 deleted the waiter) | `GcpWatch.lean` retention policy, RF-007, GCP-A5 |
| `TestDeclinedNewRequestRegistersNoWatch` | Immediate decline still leaves no waiter on the current pin (upstream TODO) | `GcpWatch.lean`, RF-007 |
| `TestEqualVersionRespondsForNewlySubscribedResource` | Same version can answer unreturned B | `GcpWatch.lean`, reopened GCP-A1 |
| `TestWildcardWatchIsNeverDeclined` | This legacy wildcard fixture bypasses named superset check | Wildcard/unsubscribe history still open |
| `TestInstallationSurvivesPartialResponseFailure` | CDS answered, EDS blocked/canceled, new cache remains, EDS waiter retained | RF-007, multi-stage installation model open |
| `TestFullImmediateChannelBlocksOtherNode` | Full immediate channel holds cache-wide lock; draining releases both operations | RF-011, wrapper queue-capacity audit open |
| `TestRepeatedNackResendsSameVersionAndCorrectionRecovers` | 32 matching NACKs resend rejected version; changed snapshot recovers | RF-012, damping/isolation policy open |
| `TestStaleNonceDropsSubscriptionChangeAndWatch` | Both ADS loops observe stale requests in callbacks but discard subscription changes; a current nonce recovers | RF-022, real-client schedule and fairness remain open |
| `TestQueuedSupersededResponseIsDroppedOnSubscriptionChange` | A parked named watch answered during its own supersession is queued but never sent, in both ADS modes; the cache cannot answer after cancel returns | RF-018: #1498 schedule unobserved with SnapshotCache; wrappers and other caches open |
| `TestResubscribeAtEqualVersionIsAnsweredOnPin` | Unsubscribe then resubscribe at the unchanged version is answered with a fresh nonce (corpus #431 repaired in the pin) | Held-out corpus mechanism; no model change needed |
| `TestClearSnapshotOrphansParkedWatchOnPin` | ClearSnapshot drops the node status while the parked watch stays registered; a later SetSnapshot answers nothing; a new request recovers (corpus #505) | RF-007 installation model lacks clear; kgateway ClearSnapshot usage audit open |
| `TestUnrequestedBootstrapClusterCLAWithholdsEDSForOlderProxy` | A CLA for a cluster the proxy never requests makes the superset check decline its whole EDS response across revisions; the filtered snapshot answers (corpus kgateway #14471, version skew) | RF-007 upstream decline; kgateway filters CLAs to requestable clusters |

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

The subscription audit adds `TestUnsubscribeAllReceivesNothingFromCache`
(immediate and parked paths) and `TestUnsubscribeAllLeaksNothingOnWire`
(default and ordered real server). Empty names after a named subscription are
not legacy wildcard. v0.14.0 sent resources after a version change; the
current pin answers an unsubscribed watch on neither path, parks no watch for
it, and answers a later resubscribe with the new content. RF-018 and
`GcpSubscription.lean` record the mechanism and its repair.

## Profile diff against post-pin revisions

`replay-profile.sh <revision>` copies these probes into a scratch module that
pins another go-control-plane root revision with the branch's Envoy proto and
gRPC versions, and runs them. It changes nothing in this repository and is not
part of the required gate. A probe that fails there has observed a behavior
change relative to v0.14.0; explain it and record the repaired expectation.

Replayed on 2026-09-11 against a `~/git3/go-control-plane` checkout at
`0e94dc446` (2026-09-04). No root-module tag newer than v0.14.0 exists there,
so none of the changes below is in a released root module; kgateway could only
adopt them through a pseudo-version.

| Revision | Changed observations | Unchanged observations |
|---|---|---|
| `bf9b60b56` (#1356, 2026-01-19, cache response rewrite) | `TestUnsubscribeAllStillReceivesResourcesFromCache`: no response to an unsubscribed watch on either entry path. `TestUnsubscribeAllResourceLeaksOnWire`: no watch parks for the empty-names request in either ADS mode, so no resource leaks. `TestParkedNamedWatchIsDiscardedOnDeclinedResponse`: the declined parked watch is retained (1 open), the GCP-A5 repaired expectation. | Request-entry decline still registers no watch; equal-version response to an unreturned name; partial installation; stale-nonce discard; full-channel lock; repeated NACK resend. |
| `1cd122661` (#1498, 2026-07-02, send-time subscription filter) | Same set as #1356; #1498 adds no further change to these schedules. | Same as above. |

Attribution: the only cache/server changes between v0.14.0 and `bf9b60b56`
are #1356 and a Go toolchain bump, so the three repaired observations belong
to #1356. #1498 filters queued responses against the current subscription at
send time; the probes here do not construct the queued-supersession schedule
it targets, so its effect is unobserved, not absent. On 2026-09-12 the branch
merged upstream main, whose #14654 moved the pin to `1cd122661`; the three
probes above were inverted to the repaired expectations in the same change,
and the unchanged-observation column still holds on the new pin.
