# Named EDS watch model

## Question

Issue [kgateway-dev/kgateway#14184](https://github.com/kgateway-dev/kgateway/issues/14184) includes go-control-plane warnings where ADS did not answer named `ClusterLoadAssignment` requests. The focused question here is narrower than full Envoy correctness:

When Envoy sends a named ADS/SotW EDS request, is the current per-client EDS snapshot both version-new and name-compatible with that request?

## Source Behavior

The model follows go-control-plane `v0.14.0` `SnapshotCache` behavior in `pkg/cache/v3/simple.go`:

- `CreateWatch` responds immediately when the snapshot version differs from the request version, or when a newly subscribed resource is available.
- In ADS mode, `respond` first checks that every resource in the snapshot response is named by the request.
- If a snapshot resource is outside the request names, `respond` logs `ADS mode: not responding...` and sends no response.
- `createResponse` then filters the returned resources to the requested names.

The important distinction is that filtering the response is not enough. The ADS guard runs before the response is created, so the whole snapshot resource set for that type must be compatible with the named request.

## Model

The model is `devel/formal/tla/XdsNamedEdsWatch.tla`.

It abstracts:

- Two EDS resource names, `cluster-a` and `cluster-b`.
- Envoy's current named EDS request.
- The cache's EDS resource set.
- The cache EDS version and Envoy's last accepted EDS version.
- Whether the request opens a watch, receives a response, or is suppressed by the ADS guard.

The model intentionally ignores endpoint payload details. For issue 14184, the relevant shape is the resource-name set and the per-type version, not the addresses inside a `ClusterLoadAssignment`.

## Safe Invariants

`XdsNamedEdsWatch.cfg` checks:

- `ResponseOnlyForRequestedNames`: a sent response contains only requested EDS resources.
- `ResourceSetChangeRequiresVersionChange`: if the EDS resource set differs from Envoy's last accepted EDS resources, the EDS version must differ too.
- `ChangedSnapshotRequestRespondable`: if the cache has a new EDS version, the cache EDS resources must be a subset of Envoy's named request.
- `ChangedRespondableSnapshotCanSend`: if the snapshot is version-new and name-compatible, TLC can take the response action.
- `NoSuppressedChangedResponse`: a version-new EDS response must not end in the ADS suppressed state.

## Counterexamples

`XdsNamedEdsWatchStaleExtraBug.cfg` demonstrates the stale extra EDS resource shape:

- Envoy previously accepted EDS for `cluster-a` and `cluster-b`.
- CDS shrinks so Envoy requests only `cluster-a`.
- The cache publishes EDS version `v2`, but the EDS snapshot still contains `cluster-b`.
- `ChangedSnapshotRequestRespondable` fails because the version-new snapshot cannot pass the ADS named-request guard.

`XdsNamedEdsWatchVersionReuseBug.cfg` demonstrates the version reuse shape:

- Envoy previously accepted EDS version `v1` containing `cluster-a` and `cluster-b`.
- The cache filters EDS to only `cluster-a`.
- The cache incorrectly reuses EDS version `v1`.
- `ResourceSetChangeRequiresVersionChange` fails because Envoy has no version-new SotW state to accept.

## Result

The model supports this issue-focused statement:

> A per-client EDS snapshot is publishable through go-control-plane ADS named watches only when the EDS resource set is filtered to Envoy's requested EDS names and the EDS version changes whenever that resource set changes.

It does not prove all go-control-plane behavior. It is a small executable model of the named-watch response seam that kgateway must satisfy when updating per-client snapshots.

## How to Run

Run the passing model:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar \
  -config XdsNamedEdsWatch.cfg \
  XdsNamedEdsWatch.tla
```

Run the intentional counterexamples:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar \
  -config XdsNamedEdsWatchStaleExtraBug.cfg \
  XdsNamedEdsWatch.tla

java -jar /path/to/tla2tools.jar \
  -config XdsNamedEdsWatchVersionReuseBug.cfg \
  XdsNamedEdsWatch.tla
```
