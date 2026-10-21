# Publication histories through control-plane setup and real Envoy

`make formal-live-publication` checks historical `7d930bb2183b` counterexamples
and the repaired `8e75468f4d0e` stack against digest-pinned upstream v1.39.4
Envoy. It runs with one and two workers.
Original production Go files and module dependencies remain unchanged in fresh
archive copies. This does not deploy or port the stack into the research tree.

```sh
# Public upstream image, both revisions, one and two workers.
make formal-live-publication
# Public CI profile; outputs belong outside the repository.
make formal-live-publication FORMAL_LIVE_ARGS='--profile upstream-current --artifacts /tmp/xds-live-new-run'
# Narrow an investigation while preserving the default full matrix.
make formal-live-publication FORMAL_LIVE_ARGS='--profile upstream-current --subject 8e7546 --workers 2'
make formal-tlc
```

The runner executes all Go builds/tests through Make, with the `e2e` and
`formal_live` tags and race detection on live histories. Empty/reused artifact
directories, compilation errors, skips, timeouts, missing images, missing
observations and failures at unrelated assertions cannot count as reproduced
counterexamples. No generated run receipts are written into this repository.

## Composition and instrumentation

One Envoy connects through a transparent ADS relay to two actual
`setup.NewControlPlane` instances. Switching the relay target cancels the old
upstream RPC and forces the same Envoy process to reconnect. Both control planes
enable ordered ADS, NACK suppression and first-reconnect responses. Requests
pass through the real `KubeJWTAuthenticator`, unique-client identity/admission
callbacks, NACK metrics callback and watch-policy decorators. Bootstrap node
metadata supplies a deliberately untrusted role; the accepted cache key must
instead match the authenticated service account.

The Kubernetes TokenReview API is a fixture authority accepting one test token;
this is not JWT signature validation against a real API server or a deployment
authentication test. The local ADS transport is plaintext. Pod-locality lookup
is disabled, so the test has one shared cache key and one proxy, not arbitrary
pod-label drift or multiple concurrent proxies.

`bridge.go.txt` is an additional test bridge copied into the isolated
`proxy_syncer` package behind the **formal_live build tag**. `setup` otherwise
cannot call the private publisher without an import cycle. The bridge supplies
already-translated wrappers and KRT scheduling barriers. It calls the actual
`registerConnectionStartHooks`, `syncXds`, gate, timers and snapshot reclaimer;
it mirrors the relevant `ProxySyncer.Start` handler wiring. `ProxySyncer.Start`
and the Kubernetes-to-xDS translation pipeline are not themselves executed.
Original production files are compared before and after each run; the extra
bridge's exact digest is separately recorded. The historical bridge omits the
two API fields absent on 7d930; it does not add the repaired policy there.

## Required histories

| History | Historical subject | Repaired subject |
|---|---|---|
| Queued publication during reconnect | The queued old RDS reaches the wire, becomes active and is served over HTTP | The old build is skipped; the real first-publication timer publishes the latest deferred row before its KRT callback runs; coherent recovery follows |
| Carry, coherent recovery, later disappearance | The old episode age causes an immediate CDS removal and backend 503 | The backend forwards during its fresh carry interval; natural expiry later removes it and produces 503; a coherent build restores forwarding |
| Reset/adoption concurrency | Covered by the preserved earlier Go counterexample and the new split-lock bounded model | A publisher attempts entry while reset is paused inside the critical section; it cannot complete there. Admission replay is then superseded; no old timer response appears across a full budget window, and the current KRT row subsequently applies |
| Cancellation with an undrained payload | Existing older controls remain separate | Capture the pending callback before expiry; a coherent build cancels it. Invoke that cancelled callback explicitly, then wait beyond its original deadline; neither cache nor wire nor active configuration regresses |

First-publication budget is 1.2 seconds and carry bound is 1.4 seconds in this
test profile. Timers expire through `time.AfterFunc`; the cancellation case
separately invokes a **stale** callback as a fault injection. These are measured
test durations, not production deadline proofs. EDS/RDS initial fetch timeouts
remain unset and `use_eds_cache_for_ads` is enabled. The October 19 harness
also renders the actual subject chart and preserves explicit CDS/LDS `0s`, local
EDS `1s`, the local-cluster dependency and runtime layers; fixture transport,
identity, admin and upstream listener are adapted. Earlier captures used unset
CDS/LDS and omitted the local cluster, so their claim of complete bootstrap
fidelity is superseded. The [timeout suite](../timeouts/README.md) separately
checks missing parent resources, degraded activation and cached rewarming. A derived empty assignment remains valid truth.

## Evidence checks

The separate live trace schema records candidate/KRT steps, connection
generations, real authentication and admitted identities, full per-type cache
versions/resource hashes, cache readback, gRPC response attempts/results,
requests/ACKs, independent admin observations and continuous HTTP samples.
SHA-256 is an observation digest; production EDS row-contribution hashing is
unchanged. The bridge uses the stack's production EDS version constructor for
input snapshots. The ready fixture row consistently contributes 17; synthesized
empties use name hashes and absent assignments contribute nothing. The checker
independently requires published ready/carried A to use version `17`, and an
omitted assignment to use `0`. This prevents an arbitrary fixture version from
causing an extra push that masks a missing equal-version replay. Every published
ready/carried assignment must also retain the baseline assignment bytes at that
same contribution, including publications that answer no watch.

Each application phase requires active RDS **content**, an active listener using
that route configuration, the expected cluster active/warming state, two HTTP
marker observations and a real HTTP request through the dynamic cluster to a
fixture upstream listener. A carry-expiry 503 is checked together with CDS
removal; it is not treated as a generic readiness failure. Cache installation,
successful transport, nonce-correlated ACK and remote application are checked
separately. Sampling is finite; absence on the recorded response path is stronger
than inferring absence from occasional admin polls.

Every fresh real capture is subjected to checker mutations, including missing
application/authentication/warm-history/transport evidence, wrong identity,
nonce, response bytes, admin hash, HTTP result and terminal status. Changing
active route content while also updating the artifact hash must still fail.
The October 19 checker also rejects silently defaulting bootstrap CDS to 15s;
the recorded audit rejects bootstrap and application mutations.
Generated captures stay outside Git; historical source fixtures/counterexamples
remain intact.

The October 17 corrected-version run completed the upstream histories, including repaired passes
and required historical counterexamples, with race detection on arm64 Docker
Desktop. The final checker audit rejected 13 mutations on each fresh real
capture, including artificial EDS versions and changed assignment
bytes at an unchanged contribution. Exact current fixture hashes, original
production source hashes and module dependencies were verified. Exploratory
runs using opaque input EDS versions are not the acceptance evidence.

The October 19 rerun with the rendered local-cluster/parent-timeout graph again
completed the upstream histories with race detection, independent
admin/application observations and HTTP samples. The final checker audit
rejected the required capture mutations. The full run used local
Helm 4; separately rendering both frozen charts with repository-pinned Helm 3
produced byte-identical bootstrap JSON. CI now supplies the pinned Helm tool.
The original captures remain unchanged; public GitHub CI itself was not run by
this session.

`AdmissionPending.tla` adds a finite safety model of the reset/adopt/timer
interleaving. Split cleanup/adoption violates `NoEarlierPendingResponse`;
atomic cleanup/adoption and a separate proposed captured-origin policy satisfy
it at the checked bounds. This model shares no automatically verified refinement
map with Go. It separates cache, Send and application and claims neither general
revision freshness nor temporal convergence.

## Remaining scope

Run the actual Kubernetes controller's XdsWarming, XdsStarvation and
XdsIdentityRace suites on the selected product image. Add real token review/TLS,
pod-locality drift, multiple simultaneous cold/warm streams, HA replica skew,
blocked/cancelled transport, NACK retry/backoff, reference-ahead/dereference
windows, SDS rotation and revocation.
