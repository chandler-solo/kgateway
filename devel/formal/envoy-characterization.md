# Direct Envoy characterization

This harness runs a scripted SotW ADS service and a real Envoy, without
kgateway translation/readiness gates or go-control-plane SnapshotCache. It
records discovery requests/responses, admin configuration/cluster state,
readiness, and HTTP observations. All upstream traffic stays inside the
container, through a static direct-response listener.

## Pinned profile

- Image: `envoyproxy/envoy:v1.39.1@sha256:57e14a549d7bd43c8d3f6d03e8cfa653e037d4b38e133acd9b54f38c524401b4`.
- Admin build: `b579d07d3ad7ee11d32b105e91a5a39ad24718d7/1.39.1/Clean/RELEASE/BoringSSL`.
- Runtime: concurrency one, hot restart disabled, default runtime, ADS initial
  fetch timeouts explicitly zero (disabled), one priority and one cluster.
- Saved `image.json` and `server-info.json` record architecture and exact flags.
  Default panic and `-disable-panic` are separate profiles.

## Reproduce

From the repository root:

```sh
docker pull envoyproxy/envoy:v1.39.1@sha256:57e14a549d7bd43c8d3f6d03e8cfa653e037d4b38e133acd9b54f38c524401b4
CGO_ENABLED=0 go run -tags e2e ./devel/formal/cmd/envoyprobe -out /tmp/envoy-default
CGO_ENABLED=0 go run -tags e2e ./devel/formal/cmd/envoyprobe -disable-panic -out /tmp/envoy-no-panic
```

The command requires Docker and a local image; missing infrastructure fails.
It allocates random loopback ports, binds a temporary ADS listener for container
access, and removes only its own container. Docker Desktop uses its built-in
host address; native Docker uses `host-gateway`. A remote Docker daemon needs
an appropriate reachable host and is not covered by this runner.

Each run retains bootstrap, image inspection, server version, wire JSONL,
config dumps, cluster dumps, and Envoy logs. Admin state establishes the phase
before traffic assertions, avoiding an apparent pass against stale endpoints.
Raw traffic status is not a universal diagnosis of the cause of a 503.

## Observed outcomes

Both profiles pass the following schedule:

| Phase | Envoy state | Traffic |
|---|---|---|
| No EDS response | Dynamic cluster warming, process readiness 503 | Startup incomplete |
| Explicit empty CLA | Cluster initialized, listener active, readiness 200 | 503 |
| Ready CLA | Ready host | 200 from fixture upstream |
| Same-name CDS change, EDS response withheld | New cluster warming, previous active cluster retained | 200 from old active state |
| Identical EDS content and version replayed | Warming completes | Old/new upstream endpoint identical |
| Sole EDS host UNHEALTHY | Readiness stays 200 in both profiles | Default panic: 200; panic disabled: 503 |
| Empty CLA after activation | Readiness stays 200 | 503 |
| Ready CLA restored | No reconnect required | 200 |

The result replaces the broad ENV-A1 usable-endpoint activation claim with
limited direct observations. `EnvoyAvailability.lean` records initialization,
selectability, and response-event distinctions; it is not a full Envoy model.
RF-013 adds health/selection configuration to the scope inventory. RF-014
requires composing same-version rewarming with the actual cache/server.

## Limits and actions

RF-004 remains open for missing CDS/reference-ahead, multi-cluster partial
NACK/application, SDS rotation/revocation, initial-fetch timeout variants,
multiple workers, controller/proxy restart, shared-key streams, and broader
transport failure. No steady-state or wall-clock guarantee follows from one
controlled schedule. RF-001's live kgateway e2e suites still need execution.
Historical mechanism links include Envoy #13009 and go-control-plane #46;
closed status alone does not establish version coverage or a deployed repair.

## Integration with the actual cache/server

`-snapshot-cache` substitutes the root go-control-plane SnapshotCache and
SotW server for the scripted server. `-ordered` selects ordered ADS in that
mode. The required runner executes both, in addition to the two direct panic
profiles.

In both cache modes, the same-name CDS connect-timeout change produces a
warming candidate and an EDS request at the already returned version. A
same-version cache republish returns successfully but does not send EDS;
admin state remains warming for the checked 400 ms stable window. An explicit
EDS version change then releases warming. The old active endpoint continues
to serve traffic during that window. Wire logs distinguish cache installation
from responses and from observed warming completion.

RF-017 records the defect/limitation and required mitigation work. The finite
observation is not a proof of infinite silence. The composed finite model
`RewarmingComposition.lean` explains the missing transition under stable
input, and the v0.14.0 source supplies the equal-version eligibility branch.
This is still not a proof of all real client retries or timer schedules.
The version change is a controlled recovery event, not a proposed universal
repair. This integration evidence advances RF-014 without closing its broader
protocol/refinement obligation.

## Reference and rejection scenario

`-scenario references` runs the scripted server with the default panic
profile and a second dynamic listener on port 10002. It characterizes how the
pinned binary treats references the control plane has not closed and
responses it partially rejects. Observed on 2026-09-11 with the image above:

| Phase | Response | Envoy outcome |
|---|---|---|
| RDS route to `ghost`, absent from CDS, beside a ready route to `a` | ACK, no NACK in a 300 ms window | Readiness 200; `/` 200 through `a`; `/ghost` 503 |
| CDS `{a with connect_timeout 3s, bad}` where `bad` is EDS without an EDS config | NACK `Error adding/updating cluster(s) bad`, request version stays `r0` | `a` active at version `r1` with connect_timeout 3s; `bad` absent; traffic 200 |
| CDS `{a, b}` with an empty CLA for `b` | ACK | Both clusters active |
| LDS `{front with stat_prefix front-r3, inline}` where `inline` has an inline route to `ghost` | NACK `Error adding/updating listener(s) inline: route: unknown cluster 'ghost'`, request version stays `r2` | `front` active with the new stat prefix while its dump `version_info` stays `r0`; `inline` held only in `error_state`; port 10002 refuses connections; `/` 200 |
| LDS `inline` repointed to `a` | ACK | Port 10002 serves 200 |

Three conclusions, each limited to this binary and configuration:

- A dangling RDS reference is accepted and degrades only that route. An
  inline (LDS) route to the same absent cluster is rejected with the whole
  listener, because Envoy validates clusters for static route configurations
  by default and not for RDS. kgateway emits RDS routes, so a missing CDS
  cluster is a per-route 503 at Envoy, not a proxy-wide failure (RF-003,
  RF-024).
- SotW rejection is not atomic. Envoy applies every valid cluster or listener
  in a response and NACKs the response for the invalid ones. The NACK
  request reports the previous accepted version while the applied resources
  carry the rejected content. The control plane's "last ACKed version"
  therefore does not describe what Envoy runs after a NACK (RF-026).
- The version shown in the config dump after a partial rejection is not
  consistent across types: the cluster shows the rejected version, the
  modified listener shows the previous version with new content. Version
  strings in admin dumps are not an application receipt.

The scripted server does not resend a rejected version on NACK; the next
phase supplies a new version. It does not test RDS or EDS partial rejection
(single-resource responses here), Delta xDS, SDS, or worker application.

### Rejection through the actual cache and server

`-scenario references -snapshot-cache` and `-ordered` route the same schedule
through the root go-control-plane SnapshotCache and SotW server. The
dangling-reference and partial-acceptance observations are unchanged. The
difference is what follows a NACK: the scripted server never resends a
rejected version, while the cache path answers each NACK, whose request
carries the previous version, with the same rejected snapshot. Measured over
a two-second window on one arm64 developer machine with Envoy at concurrency
one inside Docker:

| Rejection | Server mode | NACKs in 2 s | Responses in 2 s |
|---|---|---|---|
| CDS `{a, bad}` | default ADS | 6816 | 6816 |
| CDS `{a, bad}` | ordered ADS | 6860 | 6860 |
| LDS `{front, inline}` | default ADS | 3846 | 3846 |
| LDS `{front, inline}` | ordered ADS | 3904 | 3904 |

Each round trip re-delivers the valid siblings, which Envoy re-applies as
no-ops, and re-rejects the invalid resource. Traffic through the retained
cluster stayed 200 during the window, and a corrected snapshot ended the
recurrence in every run. These are real-Envoy recurrence measurements for
RF-012 on one machine; they are not a CPU budget or a rate that transfers to
other hardware, concurrency, or resource sizes. The scripted profile's window
stays at zero NACKs and zero responses.

### Rejection semantics by resource type

`-scenario rejection` repeats the partial-rejection question for the two
types kgateway updates most often. Two listeners, two route configurations,
and two EDS clusters start ready. An EDS response carries a valid weight
change for cluster `a` and a CLA for `b` whose port exceeds 65535; an RDS
response carries an added route for `routes-a` and a route for `routes-b`
whose regex does not compile. Observed on 2026-09-11:

| Type | Invalid sibling fails at | Valid sibling applied? | Envoy log |
|---|---|---|---|
| CDS (references scenario) | cluster construction (EDS cluster without EDS config) | yes | one `Error adding/updating cluster(s)` NACK |
| LDS (references scenario) | listener construction (inline route to an unknown cluster) | yes | one `Error adding/updating listener(s)` NACK |
| EDS | proto constraint validation on decode | no | one rejection |
| RDS | route configuration construction (regex compile) | no | one rejection logged per RDS subscription |

Partial acceptance is therefore a property of the CDS and LDS API
implementations, which add or update resources one at a time and collect the
failures, not of SotW response handling in general. EDS and RDS responses are
rejected as a whole, whether the failure is caught during decoding or during
construction, and every subscription of that type on the stream sees the
rejection. Through SnapshotCache the rejected EDS and RDS versions are resent
on every NACK like the CDS and LDS cases: about 6,600 EDS and 6,000 RDS
round trips in two seconds on the same machine. Both chains kept serving
their retained configuration throughout, and the corrected responses ended
the recurrence.

This scenario does not cover an EDS failure that passes constraint
validation but fails at application, SDS, or Delta xDS.

### Secret delivery, rotation, and removal

`-scenario secrets` drives SDS over ADS, the way kgateway's main server
delivers Secret resources. A TLS listener on port 10004 takes its certificate
from SDS secret `cert`; a plain listener and a ready EDS cluster stand beside
it. Certificates are generated per run inside the probe; keys travel only in
the SDS response and Envoy redacts them in its dump. Observed on 2026-09-11:

| Phase | Response | Envoy outcome |
|---|---|---|
| Initial `cert` (CN cert-1) | ACK | Readiness 200; handshake serves cert-1; dump shows `cert` active |
| Same name, new content (CN cert-2) | ACK | Handshake serves cert-2 without listener drain; no NACK |
| SDS response for the subscribed name carries no resource | ACK, request version advances to the empty response's version | Handshake keeps serving cert-2 for the whole 400 ms window; dump still shows `cert` active; no NACK |
| New TLS listener referencing secret `never`, which the server never sends | ACK for LDS | Listener held warming with `never` in warming secrets; no handshake on its port; readiness stays 200; other listeners unaffected |
| One SDS response with a valid rotation of `cert` (CN cert-3) and a `cert2` whose key does not match its certificate | NACK `KEY_VALUES_MISMATCH`, request version stays at the previous SDS version | The valid rotation is not applied: handshakes keep serving cert-2. SDS rejects the whole response like EDS and RDS (Envoy Gateway #9463 reproduced) |
| The `tls` listener's SDS ConfigSource bytes change (a 5 s initial fetch timeout is added) while `cert` keeps its name, content, and version | ACK | Envoy creates a second provider for `cert` (the dump lists it twice); over SotW ADS the new provider receives the secret, the listener stays active, and 16 of 16 handshakes over 8 s serve cert-3. The Envoy #47309 failure did not reproduce over SotW |

Two conclusions, limited to this binary and SotW SDS over ADS:

- Removal is not revocation. Dropping a secret from the SDS response is
  accepted and changes nothing in the data plane; the last delivered
  certificate keeps serving. A control plane that deletes a Secret resource
  in response to revocation has revoked nothing unless it also replaces the
  secret's content or removes every listener and cluster that references it
  (RF-027). This is the concrete shape of the plan's warning that retaining
  last-good state can silently preserve revoked credentials.
- A missing secret holds only its own listener. Process readiness and the
  other listeners are unaffected, which matches the per-resource warming
  seen for missing EDS, and the held listener refuses connections rather than
  serving without TLS.

Not covered: validation-context (CA) secrets, upstream client certificates,
SDS over its own gRPC service as `pkg/sds` exposes, and Delta xDS, where
Envoy #47309 and Envoy Gateway #9519 report the provider-key failure.

### Controller restart with an empty cache

`-scenario restart -snapshot-cache` (and `-ordered`) publishes a ready
configuration through the real SnapshotCache, lets Envoy warm, then stops the
ADS server and starts a fresh server with an empty cache on the same port,
the shape of a kgateway controller restart before KRT has re-derived the
client's snapshot. Observed on 2026-09-11:

| Step | Envoy behavior |
|---|---|
| Server stops, empty cache takes the port | Envoy reconnects on its own backoff and re-requests all four types with `version_info` equal to its accepted versions and an empty nonce; the empty cache answers nothing; traffic and readiness stay 200 |
| Same content republished under the same versions | No response is sent (equal versions park every watch); nothing is needed; traffic 200 |
| Changed CDS content under new versions for every type | Four responses, the cluster rewarms and activates with the new timeout; traffic 200 throughout |
| Proxy container restarted against the warm cache | The fresh Envoy requests every type with no version; the cache answers all four immediately; the cached revision activates and traffic returns 200. Docker reassigns ephemeral published ports on restart, so the probe re-resolves them |
| Server restarted while a CDS revision is warming (EDS withheld) | Envoy reconnects and re-requests EDS at its accepted version and LDS and RDS at theirs, all with empty nonces, and does not request CDS for the whole five-second window; the empty cache answers nothing; the candidate cluster stays warming; the old active cluster keeps serving 200 |
| EDS revision published for the warming cluster | Warming completes, the CDS request follows, and the next CDS revision applies; traffic 200 throughout (RF-028; Envoy #36951 and #34334 reproduced over SotW) |

Conclusions, limited to this binary and cache: a warm proxy is not harmed by
a control-plane restart whose cache is empty, provided the control plane
eventually republishes; nonces are stream-local while versions persist, as
`XdsAdsSotw.tla` assumes; and an equal-version republish after restart is
silent, which is correct for identical content and is the same mechanism
that parks a same-name rewarming (RF-017). Bumping every type's version on a
revision that changed only CDS produced three content-identical pushes
(RF-025).

A restart during warming is covered by the last two rows: CDS discovery stays paused across the reconnect, so a client whose EDS is withheld or parked at an equal version after the restart cannot receive a CDS repair until EDS is released (RF-028, RF-014).

Not covered: the kgateway first-publish gate's behavior when the
reconnected proxy's derived snapshot is deferred (RF-002, RF-003 hold the
whole first publication for a client with no cache entry).

### Initial fetch timeouts and a route ahead of its cluster

`-scenario timeouts` sets a two-second `initial_fetch_timeout` on the
bootstrap CDS and LDS sources and on every ADS config source, then withholds
dependencies. The other scenarios disable the timeout to isolate the control
plane; this one measures what Envoy does on its own. Observed on 2026-09-11:

| Step | Envoy behavior |
|---|---|
| CDS `{a}` and an RDS listener; EDS and RDS never sent | EDS times out after 2 s and `a` activates with no hosts; RDS times out 2 s later and the listener activates with no routes; readiness arrived after about 3.95 s; requests answer 404; no NACK |
| RDS `/ -> b` with `b` absent from CDS | 503 |
| CDS `{a, b}`; EDS for `b` never sent | `b` activates empty after about 2.03 s; 503 |
| EDS for `b` | 200 without a reconnect |

Conclusions, limited to this binary and configuration:

- Envoy bounds every missing dependency itself. With the default 15 s (this
  run used 2 s), a cluster whose EDS never arrives becomes active and empty
  and a listener whose RDS never arrives becomes active with no routes;
  readiness then follows. The two timeouts composed sequentially here: the
  cluster-manager phase completed before the listener phase's timer ran, so
  a proxy missing both waited the sum. This is the data-plane counterpart of
  the RF-003 classification question: Envoy already converts an
  indefinitely missing dependency into a degraded active state after a
  bound, while the research policy withholds the whole first publication
  for a cache-less client indefinitely.
- A route published ahead of its cluster (RF-010) serves 503, not 404, and
  recovers as soon as the cluster and its endpoints arrive, with no
  reconnect and no NACK.

Not covered: the default 15 s value, timeouts during a warm update rather
than initialization, and interaction with the kgateway readiness gate.

### Partial EDS during initialization

`-scenario init` replays the Envoy #21425 schedule with timeouts disabled:
CDS carries clusters `a` and `b`, the EDS response carries only `a`, CDS is
re-pushed under a new version with identical content, and then EDS carries
both. Observed on 2026-09-11: with `b` lacking its assignment the
cluster-manager init phase does not complete, LDS is not requested, and
readiness stays 503; the CDS re-push changes nothing and produces no NACK;
when the missing assignment arrives, initialization completes, LDS is
requested, and traffic serves. The indefinite block reported in 2022 does
not reproduce on v1.39.1. This also shows the ADS initialization order
directly: listeners are not requested until every primary cluster has its
endpoints or has timed out, which is why a single withheld assignment delays
the whole proxy's readiness (RF-003, RF-014).
