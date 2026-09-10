# go-control-plane source inventory

Profile: root module `github.com/envoyproxy/go-control-plane@v0.14.0` selected
by the research branch. Read from the Go module cache, not a moving checkout.
This is an initial branch-level inventory of the high-risk path. **It is not
complete source coverage.** Open rows must be resolved before Phase 2 exits.

## Reachable entry points

`pkg/kgateway/setup/controlplane.go` constructs ADS `NewSnapshotCache` with
`NewNodeRoleHasher`. Setup passes `EnableOrderedAds` into the server option.
The gRPC server registers ADS and individual EDS/CDS/RDS/LDS services. This
registers methods beyond the normal SotW ADS bootstrap path: per-type and
Delta/Fetch reachability must be audited against auth/bootstrap, not excluded
merely because the usual client does not call them. No SDS service registration
appears at this seam; separately audit secret delivery and the SDS binary.

## Cache transitions

| Source branch | State or concurrency effect | Evidence/disposition |
|---|---|---|
| `simple.go:SetSnapshot` assignment before fan-out | New snapshot visible after error; global cache and node locks span sends | Partial cancellation probe; multi-stage model open RF-007 |
| `respondSOTWWatches` version equality | Equal version does not answer parked watch | Explicit `publish` model guard; version alias limits RF-008 |
| `respondSOTWWatches` ADS ordering | Types sorted, error stops later fan-out | Existing ordering and new cancellation probes |
| `respondSOTWWatches` nil return | Deletes watch even if `respond` declined without sending | Parked decline probe and `GcpWatch` counterexample |
| `CreateWatch` no snapshot | Registers watch | Partial-installation fixture starts here; absence lifecycle model open |
| `CreateWatch` equal-version branch | Consults subscription/returned resources, including wildcard | Named new-B probe/model; wildcard-history matrix open |
| `CreateWatch` immediate response | Uses Background while holding global lock; decline does not register | Immediate decline and full-channel probes; RF-007/011 |
| `respond` named superset | Returns nil when snapshot names exceed nonempty request | Both entry probes; service-name root-package regression |
| `respond` channel/cancellation select | Send may precede cancellation; canceled send returns error | Controlled blocked EDS probe avoids ambiguous ready branches |
| `cancelWatch` | Cache RLock then node lock; removes watch if status still exists | Cancel/publish/ClearSnapshot race matrix open |
| `ClearSnapshot` | Deletes snapshot and status, unlike KGW collection Delete | Reconnect and in-flight cancellation ownership open |
| `snapshot.go`, `resources.go` | Resource maps, versions, mutable proto ownership | Existing endpoint digest tests; immutability/alias/payload proof open |
| `respondDeltaWatches`, heartbeats | Separate hashes, watch retention, TTL behavior | Registered/deployed reachability and shared mechanisms open |

## Server transitions

| Source branch | Effect | Evidence/disposition |
|---|---|---|
| `sotw/v3/xds.go:process` | Per-type fresh capacity-one channels, reflect selection | Existing quiet/ACK-skew probes; busy scheduling coverage open |
| `sotw/v3/ads.go:processADS` | Shared queue sized by known type count, reused across types | Ordered mode adopted as configurable; queue-capacity proof open RF-011 |
| `ads.go:processAllExcept` | Drains queued responses, drops current type before resubscription | Supersession/queued stale response generation probes open |
| Both request loops, stale nonce | Callback precedes nonce test; stale request ignores changed names | Direct stale-name/racing-subscription probe open |
| Both loops, matching request | Cancels old waiter and updates subscription before CreateWatch | Lost-watch probes cover cache only; composition open |
| `server.go:send` | Builds response, allocates nonce, updates returned resources before Send | Failed-Send/partial remote application model open |
| NACK request | No separate rejected-content policy in cache eligibility | 32-cycle real-server recurrence/correction probe; RF-012 |
| `StreamHandler` Recv goroutine | Context/request-channel ownership and close | Probe joins on cancellation; close/error races open |
| `shutdown` callbacks | Closes watches, then invokes stream closed | Existing callback serialization/error-path tests |
| Stream subscription implementation | Legacy wildcard and returned-state mutation | Full unsubscribe/re-add/alias matrix open |
| v3 adapter and Delta/Fetch servers | Additional registered entry paths | Trace service dispatch and auth constraints before exclusion |

## Next source audit obligations

- Enumerate every relevant function and early return in these files, including
  send conversion errors, callback failures, no-node/no-type requests, cache
  disappearance, and multiple streams sharing a role key. Current rows group
  branches and must not be counted as exhaustive function dispositions.
- Inventory LinearCache, MuxCache, custom wrappers, and root resource reference
  helpers for actual use and transferable historical mechanisms.
- Link protocol/source branches to the frozen historical corpus and generate
  deterministic cross-layer schedules. The named-watch model deliberately
  does not model nonce, queue, partial install, or Envoy worker state yet.
