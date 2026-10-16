# Held publication across a reconnect

This profile connects **actual stack gate decisions, the pinned SnapshotCache
and SotW server, and independent Envoy observations** for one controlled
schedule. `make formal-composed-reconnect` succeeds only when the **rollback
counterexample is reproduced**, subsequent recovery is observed, and TLC accepts
the observed milestone projection. It does not mark the stack correct.

## Subjects and contract

The production subject is stack 14817 at
`215ebe8e645080748dbe37727ab2f6f2faa33de6`, copied from a fresh immutable GitHub
archive. It is separate from research base
`16b4907989e359c38a0c876a65beceb515876cdc` and downstream source
`1bc4cde8b4edc780135c98d2268f43289978373c` with dependency `acd13c6f53d1`.
No deployed controller is identified or changed. The runner checks the two
known pristine test-build failures, records the existing exact-hash test-only
adapters, and injects `held_reconnect_test.go.txt`. It verifies that the subject's
production Go sources did not change. All Go execution goes through Make.

The proposed contract for this **one proxy, one key** schedule is:

1. A new connection's first-publication hold must also govern admission from
   that controller's retained cache. While A withholds the rebuilt candidate,
   its older entry must not replace the newer configuration this proxy already
   applied from B.
2. Logical first-budget expiry selects the latest pending candidate. An
   underived CLA is omitted for the controlled warm client. A derived empty CLA
   is valid truth and is published on coherent recovery.
3. A callback from the resolved timer episode must not overwrite the later
   coherent publication.

“Older” here refers to the deliberately constructed resource contents and input
history, not numeric ordering of opaque xDS versions. This is not a general
policy for multiple proxies sharing an authenticated cache key.

## Executed schedule and evidence

The test calls `ProxyTranslator.syncXds`, `publishGate.clientDeparted` and the
production timer callback. Real GCP cache/server instances serve controllers A
and B behind one test ADS address. Cancelling B's **server context** forces the
same Envoy process to reconnect to A. Warm-state classification is supplied by
`stubPriorXDS(true)`; the controller's actual identity callbacks are excluded.

| Phase | Gate/cache observation | Independent Envoy observation |
|---|---|---|
| Initial | A retains `old-a` after departure; B installs `accepted-b` | Active RDS and two HTTP probes report `accepted-b` |
| Reconnect/hold | A holds C; new stream reports B's version without a nonce; A's old RDS send is intercepted before transport | Envoy keeps B's configuration |
| Overlap | D replaces C in the same pending timer episode; cached RDS remains `old-a` | Admin and traffic still report B while Send is blocked |
| Release old Send | Real gRPC Send returns successfully; matching RDS ACK is recorded | Active route bytes/version and HTTP both become `old-a` while D remains held |
| Logical expiry | Stop the one-hour real timer and invoke its exact callback; D installs, underived CLA is omitted | Active route and traffic become D |
| Coherent recovery | Install `recovered` with a derived empty CLA | Active route and traffic become `recovered` |
| Stale callback | Reinvoke the old callback after coherent publication canceled its pending entry | Cache, active route and traffic remain `recovered` |

The observation helper brackets each admin dump with HTTP requests. The checker
parses active RDS **contents** and versions, active/non-warming listener and
cluster state, and both HTTP bodies. The traffic marker is a direct-response
route; `/backend` references the EDS cluster, whose endpoints are empty. This
does not test successful upstream traffic or promise absence of 503s.

The October 16 run reproduced the schedule on all three immutable images in
[`runtime/profiles.json`](../runtime/profiles.json): upstream, enterprise
standard, and enterprise FIPS, all on arm64 with one worker. Each produced 120
events, six admin/traffic observations and a 13-state model projection. See the
[receipt index](../receipts/2026-10-16-held-reconnect.json). This extends the
earlier cache-boundary finding to observed application for the controlled
composition. Enterprise binaries do **not** turn this into downstream controller
or FIPS certification evidence.

## Trace and model boundary

The separate composed schema records client key, controller, stream generation,
candidate and timer episode, complete per-type version tuples, resource byte
digests, request and subscription generation, response nonce, watch API calls,
Send attempts/results, ACKs, and independently fetched admin/traffic evidence.
SHA-256 here is a **test observation digest**, not a replacement for RF-025's
zero-marshal production row-contribution XOR version rule.

`check.py` requires the exact scenario and terminal receipt, validates causal
ordering and resource/nonce provenance, verifies every admin artifact digest,
and rejects missing or unknown evidence. Seventeen mutation tests use the saved
real upstream capture under `fixtures/`; they include missing Send, missing
application observation, wrong nonce/subscription, mismatched content, warming
instead of active state, and a stale callback that changes the cache.

[`HeldReconnect.tla`](../tla/HeldReconnect.tla) defines `Step(s,t,action)` once.
Both bounded exploration and the generated observation replay call that same
relation. The replay is checked three ways on each binary profile:

- All observed milestone transitions satisfy `Step`.
- `ObservedNoRollback` fails at the recorded application of `old-a`.
- A deliberately corrupted transition fails `ReplayOK`, rather than counting as
  the intended rollback counterexample.

The projector is an **unverified, existential interval adapter**, not a proved
Go refinement map. Publication can finish after its response is sent. The
checker uses the matching publication attempt, successful result and cache
readback to witness installation; the projection may place that installation
before a Send whose record precedes the post-call gate sample. Evidence indices
are retained so that this ordering choice is reviewable. The projection omits
microsteps, extra resource types and ACKs; it never substitutes an ACK for an
application observation. It does not expose or claim internal enqueue times.

The finite model has one key/proxy, two connection generations, two held
candidates and one coherent recovery. Its proposed fenced configuration checks
20 reachable states and eventual recovery **assuming weak fairness** of new
input, reconnect, timer execution, Send, independent application, recovery input
and stale-callback execution. The old-cache and stale-timer configurations are
required invariant counterexamples. The latter is a hypothetical mutation: this
particular drained first-budget callback passed its implementation check. It does not
supersede the stack's distinct failing flip-release timer regression.

## Reproduction and remaining obligations

```sh
make formal-composed-harness
make formal-composed-reconnect
# Public CI runs only the upstream image; local default includes both enterprise images.
make formal-composed-reconnect FORMAL_COMPOSED_ARGS='--profile upstream'
make formal-tlc
```

Every run uses a fresh artifact directory and preserves source/build identities,
exact test adapters, Docker image/platform metadata, Go receipts, JSONL trace,
bootstrap, full admin dumps, Envoy logs, projections and TLC logs. A skip,
unexpected failure, parser error, missing observation or wrong invariant fails
the runner. Required tools and enterprise registry access cannot silently skip.

Open obligations, in priority order:

1. Implement a separately reviewed stack admission/generation policy, retain
   this negative baseline, and add a candidate mode with a deterministic
   admission barrier that can observe **no stale response** without waiting for
   the old-response delay hook. Require unchanged compilation and all eleven
   existing desired-property regressions as well.
2. Replace the warm-state stub and test handoff with actual `NewControlPlane`
   callbacks, authentication/role hashing, watch-policy decorators, snapshot
   reclaim, and simultaneous cold/warm streams sharing a key. The current raw
   unordered GCP profile bypasses those setup components.
3. Exercise canceled/partial `SetSnapshot`, silent clients, superseded
   subscriptions, queued stale responses, multiple workers/keys and NACKs in the
   composed runtime. The existing separate probes remain required; their
   coverage cannot be imported into this schedule by assertion.
   Also cancel a first-budget callback **before** it drains its payload. The
   present stale invocation follows successful expiry, so it does not isolate
   the pending-entry identity guard from the empty-wrapper guard.
4. Audit actual timer scheduling and KRT input delivery. Direct callback expiry
   is not a wall-clock bound. This scenario uses an existing active cluster and
   the unset fetch timeout; it makes no claim about the distinct same-name
   rewarming/EDS-cache 15-second observation.
5. Complete downstream state wiring in a product candidate, then run the actual
   kgw-ent controller, peering, enterprise filters, ancillary dependencies and
   standalone SDS. Repeat with real data traffic and secret revocation. Models
   and direct enterprise binaries cannot discharge these obligations.
