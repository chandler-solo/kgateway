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
