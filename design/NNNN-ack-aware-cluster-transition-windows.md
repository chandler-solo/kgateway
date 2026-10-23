# EP-NNNN: ACK-Aware Transition Windows for Referenced Cluster Discovery

- Issue: [#NNNN](https://github.com/kgateway-dev/kgateway/issues/NNNN)
- Status: provisional. A prototype exists on `chandler/13586-ack-aware-windows`; it is parked until the live Envoy validation below supports its central assumption.

## Background

`KGW_CLUSTER_DISCOVERY_MODE=REFERENCED` (#13586) sends each gateway's proxies only the clusters and endpoints their routes and listeners reference, rather than one cluster per discovered backend. A cluster now enters and leaves a proxy's CDS whenever configuration starts or stops naming it, and Envoy does not necessarily apply CDS before RDS within one snapshot. Two transition windows guard those moments:

| Window | Setting (default) | Guards | Timed from |
|---|---|---|---|
| Reference-ahead | `KGW_CLUSTER_REFERENCE_AHEAD` (2s) | A route naming a cluster the proxy doesn't have yet | The publish that first carries the new cluster. Routes, listeners and secrets stay at their published versions until it closes. |
| De-reference grace | `KGW_CLUSTER_DEREFERENCE_GRACE` (5s) | A cluster removed while a route still names it | The installation of the route update that stops naming the cluster |

Both are elapsed time. The controller installs a snapshot in its xDS cache, and the window runs from there. Sending the response, Envoy's ACK, and Envoy warming or applying the change are separate, later events that nothing observes. The windows narrow the race rather than close it, and each must exceed the fleet's worst-case propagation.

The publication gate is in `pkg/kgateway/proxy_syncer` (`publishGate`, `reference_ahead.go`, `dereference_grace.go`). The xDS server's callbacks in `pkg/kgateway/setup` see every `DiscoveryRequest` on every stream. The watch policy (`cache_watch_policy.go`) already correlates a NACK with the response it rejects by stream, type and nonce, and the unique-client callbacks map streams to the client key the gate uses.

## Motivation

Elapsed-time windows fail in exactly the conditions where transitions matter most:

1. **Slow delivery.** go-control-plane sends at most one response per type per stream and waits for the next request before sending another. A proxy slow to ACK a cluster response receives the held routes once reference-ahead's 2s pass, whether or not it has the cluster, and requests on those routes get `503` with response flag `NC`.
2. **Rejected route updates.** If Envoy NACKs the route update that stopped naming a cluster, it keeps the previous route. The grace expires anyway, the cluster is removed, and the previous route returns `503 NC`.
3. **Rejected cluster updates.** If the cluster update adding a cluster is NACKed, reference-ahead still releases the routes that name it.

Ending windows on the proxy's acknowledgement does two things, and only the second improves reliability:

- **It shortens windows for healthy proxies.** Removing the fixed 2s wait when a route gains a destination speeds configuration convergence; removing a cluster earlier saves only transient retention.
- **It lets the bounds grow.** A slow or NACKing proxy still meets the same deadlines unless the durations are raised. Once early ends are trustworthy, longer bounds protect slow proxies without delaying healthy ones.

It also addresses the transition problem that originally motivated emitting every cluster, which matters for making `REFERENCED` broadly usable.

## Goals

- End a reference-ahead hold when every connected stream of the client has the new clusters ready, by evidence tied to the current transition.
- End a de-reference window when every connected stream has applied a route configuration that no longer names the cluster.
- End a window early only where Envoy's warm-up depends on nothing but updates whose ACKs the gate sees. Otherwise the window keeps its duration, exactly as with the setting off.
- Keep the durations as upper bounds, so a stuck, NACKing or misbehaving proxy cannot hold routes or keep clusters indefinitely.
- Add no work in steady state, and make it observable: how many windows ACKs ended, next to how many opened.
- Ship behind a setting, off by default, with no change to `ALL` mode.

## Non-Goals

- Proving readiness beyond what ACKs show. Where an ACK does not imply ready, the window keeps its duration.
- Delta xDS. kgateway serves State-of-the-World ADS.
- Making the first-publish budget, the flip-release budget or the warm withhold bound ACK-aware.
- Guaranteeing in-flight requests across listener drains.
- Surviving controller failover. Window state is in-memory today, and stays so here.

## Implementation Details

### Configuration

| Setting | Default | Meaning |
|---|---|---|
| `KGW_CLUSTER_TRANSITION_ACKS` | `false` | When `true`, under `REFERENCED`, a window whose readiness ACKs can show ends when every connected stream of the proxy ACKs, with the durations below as upper bounds. When `false`, windows behave exactly as today. |
| `KGW_CLUSTER_REFERENCE_AHEAD` | `2s` | With ACKs on, the longest a hold waits. |
| `KGW_CLUSTER_DEREFERENCE_GRACE` | `5s` | With ACKs on, the longest a de-referenced cluster is kept. |

<<[UNRESOLVED defaults with ACKs on ]>>
The reliability benefit needs longer bounds, for example 10s and 30s. Should the defaults change when ACKs are on, or should operators set them? This waits on the validation below.
<<[/UNRESOLVED]>>

### Plugin

No plugin API changes.

### Controllers

None.

### Deployer

None.

### Translator and Proxy Syncer

**1. ACK tracking (`pkg/kgateway/setup`).** The watch policy already records, per stream and type URL, the last response sent: its nonce, its version, and the client it was set under. A request answering that nonce without `error_detail` is an ACK. With tracking on, the policy also keeps, per stream:

- the version of each type it last ACKed;
- the clusters of the last ACKed cluster response, by reference to the per-response hash map it already builds;
- the assignment names of the last ACKed endpoint response.

It indexes streams by client, and reports ACKs and stream closes outside its lock. The gate reads this through an interface on the xDS cache it already holds, so `NewControlPlane` does not change:

```go
type transitionAcks interface {
    AckedResources(client, typeURL string, names []string) bool
    AckedVersions(client, typeURL string) map[int64]string
    NotifyAcks(fn func(client string))
}
```

A stream that has never been sent a type does not count: its first response carries the current snapshot whole.

**2. Evidence tied to the transition.** Snapshot versions are hashes and can't be ordered, so the gate numbers each client's installs and records each install's versions. Every window remembers the install that opened it. Evidence must come from that install or a later one.

The review found why this matters. After A → B, A is removed. If that removal is still in flight when configuration returns to A, the stream's last ACKed responses still carry A. A check by name alone accepted those ACKs and released the routes, even though the pending removal could delete A before its re-add arrived. With the versions-since requirement, ACKs from before A's window cannot release it.

**3. Reference-ahead.** A hold ends when every stream's last ACKed cluster response came from an install made since the latest of its clusters' windows opened, and carries every new cluster. For an EDS cluster, the same must hold for an endpoint response carrying its assignment.

It ends early only for clusters whose warm-up depends on nothing an ACK can't show. Envoy keeps a cluster warming until its initial discovery (DNS, EDS) and, if configured, its first active health check pass complete, and a cluster with TLS from SDS also waits for its secrets ([cluster warming](https://www.envoyproxy.io/docs/envoy/latest/intro/arch_overview/upstream/cluster_manager.html#cluster-warming)). An ACK means accepted, not applied ([ACK semantics](https://www.envoyproxy.io/docs/envoy/latest/api-docs/xds_protocol.html#ack-and-nack-semantics-summary)).

| Cluster | Early end |
|---|---|
| EDS, no health checks, no SDS secrets | On the cluster and endpoint ACKs |
| STATIC with socket-address endpoints, no health checks, no SDS secrets | On the cluster ACK |
| STRICT_DNS, LOGICAL_DNS, custom cluster types | Never; keeps its window |
| Any cluster with `health_checks` | Never |
| TLS with SDS secrets, transport socket matches, or a transport socket that may wrap one | Never |
| STATIC reaching a pipe or internal address (a tunnel's listener) | Never |

A hold that also waits for an unready cluster still follows the flip-release rules. Only the reference-ahead part ends early.

**4. De-reference grace.** A window ends when every stream's last ACKed route version, and its last ACKed listener version, belong to an install made since the window opened. Every install since then lacks the cluster's references, because one that named it again would have ended the window.

It ends early only for clusters the earlier configuration named in route configurations alone. A cluster named from a listener, such as a TCP route or an inline route configuration, keeps its full grace: an LDS ACK comes before the new listener finishes warming, and the old listener keeps accepting connections that need the cluster until then.

**5. Every connected stream.** One client can be several Envoy pods sharing a snapshot. A window ends when every stream connected for that client to *this controller* has satisfied it. A stream that closes stops counting, and the policy reports the close so the gate rechecks.

**6. Bounds.** The existing release and prune timers stay, as upper bounds. An ACK that comes first releases the hold through the same `releaseFlipLocked`, or prunes through the same `publishBuildLocked` and hold decision. An acknowledged window is treated as expired. The grace timer records the newest build even when no window remains, as #14877 does, so neither a queued timer nor an ACK-triggered prune can re-publish an older build.

**7. Missed notifications.** Notifications go only to clients with a registered window, through a lock-free set. A window registers after its install, so ACKs can arrive before registration. The review found this for de-reference windows. Both paths therefore check once right after a window opens, in addition to every later ACK.

**8. NACKs.** A NACK is not an ACK, so a rejected update leaves its window open until the bound. With NACK resend suppression (#14701), an ACK of a retried version ends it then.

**9. Failover and reconnect.** ACK state, window state and install numbering are per controller and in memory. A proxy that reconnects to another replica is a new connection there: its first publish is the whole current snapshot, with no window. A transition in flight at failover completes with ordered ADS delivery only, as without the windows. The elapsed-time windows behave the same today.

### Reporting

`kgateway_xds_cluster_scoping_transitions_total` gains two `transition` values:

- `reference_ahead_acked`: clusters whose hold every stream's ACK ended before the window closed.
- `dereference_acked`: de-referenced clusters every stream stopped routing to, by ACK, before the grace closed.

The existing `reference_ahead_held` and `dereference_graced` count every window opened.

<<[UNRESOLVED latency histogram ]>>
A histogram of the time from opening a window to the last stream's ACK would show how far the bounds could be raised. Is it worth a per-gateway histogram series?
<<[/UNRESOLVED]>>

### Test Plan

**Unit tests in the prototype.**

- Watch policy (`cache_watch_policy_acks_test.go`):
  - Two streams of one client: one ACK isn't enough.
  - A NACK isn't an ACK.
  - A stream that closes stops counting.
  - Nothing is recorded without `NotifyAcks`.
  - ACKs over the in-process ADS stream, in default and ordered mode.
- Gate (`transition_acks_test.go`):
  - A hold ends on the cluster and endpoint ACKs, but not on the cluster ACK alone.
  - ACKs from before a cluster's removal and re-add do not release its hold (the review's first finding).
  - A grace ends on the route update's ACK, but not on an ACK of the earlier routes.
  - ACKs that arrive during installation still end the window (the review's second finding).
  - Both windows close at their bounds without ACKs.
  - DNS, health-checked, SDS and pipe clusters keep their windows even with everything ACKed; EDS and socket-address STATIC clusters end early.
  - A listener-named cluster keeps its grace.

Each test fails without the code it covers.

**Live Envoy validation.** This is the gate for un-parking. Run Envoy against the in-process control plane, or a kind cluster with kgateway, and for each early-ending class record the time of the ACK against the time the change is active on worker threads. Observe it through `/clusters`, `cluster_manager.warming_clusters` and `listener_manager.total_listeners_warming`, and through a traffic probe on the affected route:

1. A retarget onto a new EDS cluster without health checks, and onto a socket-address STATIC cluster: no `503 NC` after release, across repeated runs, with and without CPU pressure on Envoy.
2. A de-reference of a route-only cluster: no `503 NC` on the old route after the early prune.
3. Controls: the same retargets with ACKs off, and with the excluded classes (DNS, health checks, SDS), which must show the windows still running.
4. Confirm or refute that after an ACK, the main thread's updates reach workers in order, so a later route update or removal cannot overtake the change it depends on.

If any early-ending class shows a `503 NC` attributable to ending early, that class moves to "never", or the design does.

## Alternatives

1. **Longer elapsed windows.** This is the status quo with larger settings. It's simple, but the delay applies to every route change and still guarantees nothing.
2. **Envoy on-demand CDS.** The `on_demand` filter, backed by Delta xDS, fetches an unknown cluster on first use. It removes reference-ahead, but needs Delta xDS support and an extra round trip on first use, and doesn't address removals.
3. **Envoy-side signals.** Scraping `/clusters` or warming stats would show readiness directly, but means polling every proxy out of band.
4. **Do nothing.** The memory, configuration-size and observability gains already come from the merged scoping work. Earlier removal saves only transient retention and gives up some protection against rapid route flapping. The prototype adds about 1,000 lines (stream history, install history, timers, concurrency) resting on an assumption not yet validated. This is why the prototype is parked.

## Open Questions

1. Does the live validation support early ends for EDS and socket-address STATIC clusters?
2. After an ACK, do updates reach Envoy's workers in order, so nothing a later update depends on can be overtaken?
3. What should the bounds default to with ACKs on? (See Configuration.)
4. Should a stream that never ACKs hold every replica's window to the bound, or stop counting after a shorter threshold?
5. Should windows survive controller failover? That needs either versions that are the same across replicas, or shared state.
6. Should the flip-release and first-publish budgets later use the same ACK tracking?
