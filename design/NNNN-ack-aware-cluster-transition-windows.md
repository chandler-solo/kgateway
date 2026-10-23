# EP-NNNN: ACK-Aware Transition Windows for Referenced Cluster Discovery

- Issue: [#NNNN](https://github.com/kgateway-dev/kgateway/issues/NNNN)

## Background

`KGW_CLUSTER_DISCOVERY_MODE=REFERENCED` (#13586) sends each gateway's proxies only the clusters and endpoints their routes and listeners reference, rather than one cluster per discovered backend. A cluster now enters and leaves a proxy's CDS whenever configuration starts or stops naming it, and Envoy does not necessarily apply CDS before RDS within one snapshot. Two transition windows guard those moments:

| Window | Setting (default) | Guards | Timed from |
|---|---|---|---|
| Reference-ahead | `KGW_CLUSTER_REFERENCE_AHEAD` (2s) | A route naming a cluster the proxy doesn't have yet | The publish that first carries the new cluster. Routes, listeners and secrets stay at their published versions until it closes. |
| De-reference grace | `KGW_CLUSTER_DEREFERENCE_GRACE` (5s) | A cluster removed while a route still names it | The installation of the route update that stops naming the cluster |

Both are elapsed time. The controller installs a snapshot in its xDS cache, and the window runs from there. Sending the response, Envoy's ACK, and Envoy warming or applying the change are separate, later events that nothing observes. The settings and the architecture doc already say this: the windows narrow the race rather than close it, and each must exceed the fleet's worst-case propagation.

The publication gate is implemented in `pkg/kgateway/proxy_syncer` (`publishGate`, `reference_ahead.go`, `dereference_grace.go`). The xDS server's callbacks in `pkg/kgateway/setup` see every `DiscoveryRequest` on every stream. The watch policy (`cache_watch_policy.go`) already correlates a NACK with the response it rejects by stream, type and nonce, and `pkg/krtcollections/uniqueclients.go` maps streams to the unique client (the gate's `proxyKey`). The in-band information this proposal needs is already available in-process.

## Motivation

Elapsed-time windows fail in exactly the conditions where transitions matter most:

1. **Slow delivery.** go-control-plane sends at most one response per type per stream and waits for the next request before sending another. A proxy that is slow to ACK a CDS response, because it is CPU-starved, warming a large config, or behind on an earlier response, receives the held routes once reference-ahead's 2s pass, whether or not it has the cluster. Requests on those routes get `503` with response flag `NC` until the cluster lands.
2. **Rejected route updates.** If Envoy NACKs the route update that stopped naming a cluster, it keeps the previous route. The de-reference grace expires anyway, the cluster is removed, and the previous route returns `503 NC` until a valid route update is accepted.
3. **Rejected cluster updates.** If the CDS update adding a cluster is NACKed, reference-ahead still releases the routes that name it.
4. **Tuning cannot fix it.** Raising the windows to cover the slowest proxy adds that delay to every route change on every proxy, and still promises nothing.

Waiting for the proxy to accept the update replaces a guess with the signal the transition actually depends on.

## Goals

- Release a reference-ahead hold when every connected stream of the client has accepted the new cluster: a CDS ACK for a version containing it, and for an EDS cluster an EDS ACK for a version carrying its assignment.
- Prune a de-referenced cluster when every connected stream has accepted a route configuration (RDS, and LDS for routing carried in listeners) that no longer names it.
- Keep the existing durations as upper bounds, so a stuck, NACKing or misbehaving proxy cannot hold routes or keep clusters indefinitely.
- Add no work in steady state. Bookkeeping exists only for clients with an open transition, and is O(open transitions × connected streams).
- Make it observable: how many windows ACKs ended, next to how many opened.
- Ship behind a setting, off by default until proven, with no change to `ALL` mode.

## Non-Goals

- Confirming that Envoy has *applied* or finished *warming* an update beyond what an ACK means. See Open Questions.
- Delta xDS. kgateway serves State-of-the-World ADS, and Delta streams are out of scope here.
- Making the first-publish budget, the flip-release budget or the warm withhold bound ACK-aware. These are separate holds, and could follow the same pattern later.
- Guaranteeing in-flight requests across listener drains.

## Implementation Details

### Configuration

| Setting | Default | Meaning |
|---|---|---|
| `KGW_CLUSTER_TRANSITION_ACKS` | `false` | When `true`, under `REFERENCED`, transition windows end when every connected stream of the proxy ACKs, with the durations below as upper bounds. When `false`, they behave exactly as today. |
| `KGW_CLUSTER_REFERENCE_AHEAD` | `2s` | Unchanged when acks are off. With acks on, the longest a hold waits for ACKs. |
| `KGW_CLUSTER_DEREFERENCE_GRACE` | `5s` | Unchanged when acks are off. With acks on, the longest a cluster is kept waiting for ACKs. |

<<[UNRESOLVED defaults with acks on ]>>
With acks on, the bounds only cut short a proxy that never ACKs, so they can be longer, for example 10s and 30s, without slowing healthy proxies. Should the defaults change when acks are on, or should operators set them?
<<[/UNRESOLVED]>>

### Plugin

No plugin API changes.

### Controllers

None.

### Deployer

None.

### Translator and Proxy Syncer

**1. ACK tracking (`pkg/kgateway/setup`).** The watch policy already records, per stream and type URL, the last response sent: its nonce, its version, and the client it was set under (from the request's node role, which the unique-client callbacks have already rewritten to the client key). With ACK tracking on, a request that answers that nonce without `error_detail` is an ACK, and the policy also keeps:

- the version of each type the stream last ACKed;
- the clusters of the last ACKed cluster response, which it already hashes per response to answer re-defined clusters, so this is a reference, not a copy;
- the assignment names of the last ACKed endpoint response. These are parsed from each endpoint response, which the policy already does for re-defined clusters, and kept only while tracking is on.

The gate reads them through an interface on the xDS cache it already holds, so `NewControlPlane`'s signature doesn't change:

```go
type transitionAcks interface {
    // AckedResources reports whether every stream of client sent a typeURL
    // response last ACKed one carrying every name; cluster and endpoint types.
    AckedResources(client, typeURL string, names []string) bool
    // AckedVersions returns each such stream's last ACKed version, or "".
    AckedVersions(client, typeURL string) map[int64]string
    // NotifyAcks turns tracking on and runs fn after a stream ACKs or closes.
    NotifyAcks(fn func(client string))
}
```

A stream that has never been sent a type doesn't count. Its first response carries the current snapshot whole, and Envoy's initialization waits for clusters before listeners.

**2. Reference-ahead.** A hold for new clusters ends when every stream has ACKed a cluster response carrying all of them and, for each that takes endpoints from EDS, an endpoint response carrying its assignment. Envoy keeps an EDS cluster warming until its assignment arrives, so the cluster response alone doesn't make it usable. Checking names, not versions, means an endpoint ACK that predates the stream's request for the new assignment can't satisfy it. A hold that also waits for an unready cluster still follows the flip-release rules. Only the reference-ahead part ends early.

**3. De-reference grace.** Snapshot versions are hashes and can't be ordered, so the gate numbers each install per client. A window remembers the install that opened it. It ends when every stream's last ACKed route version, and its last ACKed listener version, belong to an install numbered no earlier than that. Every install since the window opened lacks the cluster's references, because one that named it again would have ended the window. A type whose version didn't change in that install is satisfied by the stream's earlier ACK of the same version, which is right, since that type never named the cluster. The gate keeps only the installs its open windows can still be confirmed by, with at most 64 per client.

**4. Every connected stream.** One unique client can be several Envoy pods with the same identity, all sharing a snapshot. A window ends when every stream connected for that client has satisfied it. A stream that closes stops counting, and the policy reports the close so the gate re-checks.

**5. Bounds.** The existing release and prune timers stay, as upper bounds. When a window's duration runs out first, the gate does exactly what it does with the setting off. An ACK that comes first releases the hold through the same `releaseFlipLocked`, or prunes through the same `publishBuildLocked` and hold decision as the timer. An acknowledged window is treated as expired, so during a held flip it stays recorded until the release, as an expired one does.

**6. NACKs.** A NACK isn't an ACK, so a rejected cluster, endpoint or route update leaves its window open until the bound. With NACK resend suppression (#14701), an ACK of a retried version ends it then. A proxy that rejected the route update still routes to the old cluster, so the grace keeps the cluster rather than pruning it from under the old route.

**7. Locking.** The policy runs the notification outside its lock. The gate keeps a lock-free set of clients with a window an ACK could end. A notification for any other client returns at once, and one for a listed client is checked on its own goroutine under the gate's lock, so a stream never waits on the gate. After a reference-ahead hold publishes, the gate checks once, in case the streams have already ACKed.

**8. Reconnect.** ACK state is per stream, and a connection start already resets the gate's per-client state, including the install numbering. An open window without ACK history falls back to its bound.

### Reporting

`kgateway_xds_cluster_scoping_transitions_total` gains two `transition` values:

- `reference_ahead_acked`: clusters whose hold every stream's ACK ended before the window closed;
- `dereference_acked`: de-referenced clusters every stream stopped routing to, by ACK, before the grace closed.

The existing `reference_ahead_held` and `dereference_graced` count every window opened, so the share ended by ACKs reads directly. A debug log names the client and clusters when ACKs end a window.

<<[UNRESOLVED latency histogram ]>>
A histogram of the time from opening a window to the last stream's ACK would show how far the bounds could be raised. Is it worth a per-gateway histogram series?
<<[/UNRESOLVED]>>

### Test Plan

Implemented:

- **Watch policy, driven directly** (`cache_watch_policy_acks_test.go`):
  - Two streams of one client: one ACK isn't enough.
  - A NACK isn't an ACK.
  - A stream that closes stops counting and is reported.
  - Nothing is tracked without `NotifyAcks`.
- **Watch policy over the in-process ADS stream,** default and ordered: a cluster is acknowledged by its cluster response's ACK, and its assignment by an endpoint response's ACK.
- **Gate, with a fake ACK source** (`transition_acks_test.go`):
  - A reference-ahead hold with an hour-long window ends on the cluster and endpoint ACKs, but not on the cluster ACK alone.
  - A graced cluster with an hour-long grace leaves on the ACK of the route update, but not on an ACK of the earlier routes.
  - Both windows still close at their durations when no ACK comes.
- With the setting off, every existing transition test passes unchanged.

To do:

- **e2e, REFERENCED shard:**
  - Retarget a route while a proxy's xDS stream is delayed. It should see no `503 NC` with ACKs on, and the existing behavior with them off.
  - Push a route update Envoy rejects, and check the old cluster stays until the bound.
- **Live Envoy:** confirm the ACK timing questions below.

## Alternatives

1. **Longer elapsed windows.** This is the status quo with larger settings. It's simple, but it adds the delay to every route change and still guarantees nothing.
2. **Envoy on-demand CDS.** The `on_demand` HTTP filter, backed by Delta xDS, fetches an unknown cluster on first use. It removes the need for reference-ahead, but requires Delta xDS support in kgateway and an extra round trip on the first request to each cluster. It doesn't address removals, which still need to know when the route update landed.
3. **Envoy-side signals.** Scraping `cluster_manager.warming_clusters` or the admin config dump would show warming directly, but means polling every proxy out of band. The ACK is the in-band signal the control plane already receives.
4. **ACK-aware publication for every mode.** `ALL` mode sends every cluster all the time and has no add or remove transition to wait for, so this proposal is limited to `REFERENCED`.

## Open Questions

1. **When does Envoy ACK CDS?** Does Envoy ACK a CDS response once the clusters are added to the warming set, or only after they finish warming? The design waits for the EDS ACK of an EDS cluster either way, but this decides whether a static cluster's CDS ACK alone is enough. To be verified against Envoy.
2. **LDS ACK versus listener warming.** For routing carried in listeners (TCP routes), does an LDS ACK mean the new listener is active or only warming? If only warming, should the grace for listener-named clusters also wait for a drain interval?
3. **Hung streams.** Should one stream that never ACKs hold the transition for every replica up to the bound, as proposed, or should a stream that stays silent past a short threshold stop counting?
4. **Bound defaults with acks on** (see Configuration).
5. **Extending to the other holds.** Should the flip-release and first-publish budgets later use the same ACK tracking?
