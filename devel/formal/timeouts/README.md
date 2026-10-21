# Source-specific initial-fetch timeouts

October 19, 2026 extension. This work specifies each subscription's initialization
policy and checks selected implementation histories. It does not change production
timeout defaults. The assurance case remains open.

## Frozen profile and source evidence

[profiles.json](profiles.json) pins the proposed upstream stack at
`8e75468f4d0e055deb9b8f34e4254831f011e2a3`, the Helm template, two translation
sites, relevant Envoy source files, two upstream image digests and both worker counts.
The research branch does not adopt that stack's production policy.
This is not a deployed controller test.

| Subscription | ConfigSource setting | Effective policy | Source evidence |
|---|---|---|---|
| Bootstrap CDS | explicit `0s` | no initial-fetch timer | `pkg/kgateway/helm/envoy/templates/configmap.yaml` |
| Bootstrap LDS | explicit `0s` | no initial-fetch timer | same template |
| Bootstrap local cluster EDS | explicit `1s` | finite initialization wait | same template; local cluster starts before CDS |
| Kubernetes backend EDS | unset | Envoy default `15s` | `pkg/kgateway/extensions2/plugins/kubernetes/k8s.go` |
| Generated RDS | unset | Envoy default `15s` | `pkg/kgateway/translator/irtranslator/fc.go` |

The bootstrap enables `envoy.restart_features.use_eds_cache_for_ads`. A parent's
`0s` does not propagate to EDS or RDS. An unset field and an explicit `15s` have
the same duration in this profile but remain distinct configuration values.
The runner verifies the three upstream source hashes before and after execution;
the Envoy C++ hashes are a reviewed source inventory, not an automated binary
provenance or C++ refinement check.

The pinned Envoy `GrpcSubscriptionImpl` arms a finite timer when the subscription
starts, before registering its watch. A connection failure does not take the
update-rejection path: it leaves this timer running. Timeout and rejected-update
callbacks disarm it and notify the resource-specific callback. EDS timeout can
reuse a cached assignment by EDS service name; without one it permits cluster
initialization to complete without endpoints. RDS provider reuse and its failure
callback are separate paths. These are **source-inspection conclusions** for the
listed sources, not universal claims about every xDS transport or resource type.

## Specification and proof evidence

[InitialFetchTimeouts.tla](../tla/InitialFetchTimeouts.tla) represents one local
EDS/CDS/EDS/LDS/RDS initialization chain, independent remaining-time counters,
received and accepted data, cached initialization, one connection failure,
rejection, and one provider replacement with an old queued callback. It separates
initialization from data receipt and readiness from complete configuration.

Fifteen required TLC configurations check safety and conditional eventual
initialization, plus expected counterexamples for missing disabled subscriptions,
stale or early expiry, expiry of disabled timers, invented delivery, wrong cache
identity, readiness implying completeness, cache use implying freshness, and a
single global fifteen-second bound. The elapsed clock saturates at 35 only for
that last diagnostic; independent remaining timers still progress. Weak fairness
of the named actions, including logical time advancement, is an explicit model
assumption. The representative topology, single replacement and finite data
bounds do not cover arbitrary systems. The model's sequential chain abstracts
real concurrent initialization targets.

[InitialFetchTimeouts.lean](../lean/XdsSpec/InitialFetchTimeouts.lean) proves seven
abstract guard/provenance properties over unbounded natural timestamps, provider
generations and arbitrary names/payloads: expiry needs the owning provider and a
due deadline, returns the specified initialized state, preserves receive/accept
counts, cannot expire a disabled or completed subscription, and only selects a
matching cached identity. These are **abstract safety proofs**. There is no
verified refinement from Envoy, no temporal theorem and no scheduler bound.

## Direct runtime histories

The runner renders the actual pinned Helm chart, retains its local EDS cluster,
cluster-manager dependency, parent ConfigSources and runtime cache flag, and
adapts identity, ADS transport/authentication, admin access and the fixture
upstream listener for the probe. It preserves raw and executed bootstraps and
checks the independently reported bootstrap in every admin observation. CI
exposes Helm through the repository's pinned `go tool helm`; local runs may use
a compatible `helm` on PATH, whose version is recorded.

| Case | Required observation |
|---|---|
| `missing-cds` | No CDS response and no readiness across a 16-second silence window; later publication restores forwarding |
| `missing-lds` | No LDS response and no readiness across that window; later publication restores forwarding |
| `missing-eds` | Default EDS expiry increments its counter; cluster activates empty, HTTP 503; later EDS restores 200 |
| `missing-rds` | Default RDS expiry increments its counter; listener activates without the route, HTTP 404; later RDS restores 200 |
| `explicit-empty` | A delivered empty CLA activates without dynamic EDS expiry, HTTP 503; populated CLA restores 200 |
| `cached-rewarm` | Changed CDS warms beside the old active cluster, then activates using cached EDS; timeout/cache counters increase, forwarding stays 200, and no fresh EDS response is sent |
| `disabled-eds` | Only generated EDS becomes explicit zero; no readiness across 16 seconds; later EDS restores 200 |
| `local-cycle` | Only local EDS becomes explicit zero; named local watch is declined on the unrepaired immediate-watch path, CDS is never requested, startup remains blocked across 16 seconds |

The last two are deliberately nonproduction controls. In particular, the blocked
local-cycle control does not establish a new deployed bug. Cache installation
cannot recover a watch that the immediate-decline path dropped. Parked-watch
retention and unsubscribe repairs do not repair that separate entry path.

Each history uses the real pinned SnapshotCache/server, ordered ADS, a real
Envoy binary and one or two workers. Translated resources are supplied. Only
this direct fixture derives versions from proto bytes to keep unchanged EDS
unchanged during rewarming; it does not claim to execute the production
zero-marshal version algorithm. The separate live publication fixture retains
its production version constructor and consistency checks.

The checker correlates callback, Send attempt/result, versions/nonces and ACKs;
then separately checks active/warming admin configuration, assignment hosts,
readiness, timeout/cache counters and HTTP. A callback is not delivery, a
successful Send is not application, and an ACK is not applied configuration.
Mutations of each fresh capture include semantically changed bootstrap, admin
state and timer counters with recomputed hashes, as well as missing transport,
wrong nonce and HTTP evidence. Failures, missing observations and driver timeouts
cannot count as expected counterexamples. Original captures are preserved.

Two-worker runs verify configured concurrency and sampled forwarding; they do
not independently observe each worker's application barrier.

Runtime evidence covers finite histories. A sixteen-second silence observation
does not prove an infinite wait; disabled-timer source semantics and the model
provide separate evidence. Request receipt does not identify the internal timer
arm instant. A nominal duration, sequential model, or observed elapsed time is
not a hard end-to-end deadline under arbitrary dispatcher or network delay.

## Running the checks

```sh
make formal-timeout-harness
make formal-timeout-lean
make formal-tlc
# Full matrix: two digests, two worker counts, eight histories (32 runs).
make formal-timeout-profiles FORMAL_TIMEOUT_ARGS='--artifacts /tmp/xds-timeouts-new'
# Public CI subset: sixteen histories.
make formal-timeout-profiles FORMAL_TIMEOUT_ARGS='--profile upstream-current --artifacts /tmp/xds-timeouts-public-new'
# Narrow a failure without changing the default matrix.
make formal-timeout-profiles FORMAL_TIMEOUT_ARGS='--profile upstream-current --workers 2 --case cached-rewarm --jobs 1'
```

Artifacts must be fresh and outside the repository.
Receipts, logs, source archives and generated captures
are execution artifacts, not source fixtures to stage or commit.

## October 19 execution

The upstream acceptance histories completed on arm64 Docker Desktop,
with independent admin/counter/HTTP observations and rejected mutations
of the fresh real captures. It used repository-pinned Helm 3 (`v3.21` reported by
the tool) and Go 1.26.8. Both upstream digests and both worker counts were exercised.
The separate corrected upstream live setup histories passed their required
repaired and historical-counterexample outcomes.
Public GitHub CI was configured locally; it was not executed in GitHub by this
session.

All 75 required TLC outcomes matched, including designated temporal and safety
counterexamples; the original wide ADS configuration remains deferred. The full
Lean/Go/snapshot/lifecycle validation passed after adding the probe package to
its required test-receipt inventory. Four race-enabled probe tests, ten Python
timeout harness tests, five live harness tests, the assumption-ledger checks,
scoped Go lint (including the injected live helpers) and workflow lint passed.
The sixteen legacy direct Envoy profiles and four upstream runtime/image
profiles were also rerun successfully. This is evidence for the named profiles,
not universal implementation correctness or temporal liveness.

## Remaining obligations

1. Instrument real subscription start/arm/cancel/expiry and provider ownership;
   cover cancellation queued at expiry, replacement, reconnect, transport failure,
   rejected updates and cached RDS in the runtime harness. The model represents
   several of these paths; that does not discharge implementation obligations.
2. Cover multiple resources/providers, EDS service-name sharing, overlapping
   initialization, workers, SDS/TLS dependencies, health/selection and stale cache
   contents. The current successful backend has one selectable fixture endpoint.
3. Compose actual controller translation and downstream extension configuration
   with these timers and the publisher's first-publication/carry timers. Extend
   the existing live setup harness without conflating those independent budgets.
4. State an explicit scheduler/network/service assumption before attempting a
   quantitative end-to-end bound; prove temporal progress separately from safety.
5. Establish an implementation correspondence argument and version-change checks
   for each assumption. No model or finite test matrix proves xDS correctness.
