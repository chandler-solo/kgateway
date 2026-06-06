# Formal methods MVP for xDS correctness

## Motivation

This directory is an MVP for applying formal methods to kgateway's xDS correctness story. It does not claim that Envoy or kgateway is formally verified. Instead, it establishes a concrete, runnable verification seam that future IR -> xDS work can use:

- A TLA+ / TLC model checks an abstract ADS/SotW publication state machine.
- A Go validator checks concrete Envoy LDS/RDS/CDS/EDS snapshot dependency invariants.
- Tests and scripts make the seam repeatable for future translator validation.

## Scope

### TLA+ model

The TLA+ model covers protocol and state-machine behavior at a small finite-model level:

- Per-resource-type versions for LDS, RDS, CDS, and EDS.
- Per-stream response nonces and reconnect behavior.
- ACK, NACK, and stale nonce handling.
- Dependency-closed publication sequencing across listener, route, cluster, and endpoint resources.

### Go validator

The Go package at `pkg/kgateway/translator/xdscheck` checks concrete Envoy xDS snapshots:

- Resource names are unique within each type.
- LDS HTTP connection manager RDS references resolve to emitted route configurations.
- Inline HCM route configurations are checked recursively.
- Route cluster and weighted cluster references resolve to emitted clusters.
- EDS clusters resolve to emitted ClusterLoadAssignments by `service_name`, or by cluster name when `service_name` is empty.
- Basic SDS references from checked TLS transport sockets and OAuth2 HTTP filters resolve to emitted secrets.
- Unsupported dynamic constructs produce warning findings rather than panic.

### Future proof systems

Lean, Dafny, F*, and Coq compiler proofs are future work, not part of this MVP. A later proof effort can target a smaller semantic compiler from Gateway/Policy IR into an abstract xDS snapshot, then use this validator as a concrete check at the protobuf boundary.

## Non-goals

- No proof of Envoy internals.
- No proof of Kubernetes watch semantics.
- No proof of all Envoy proto validation annotations.
- No production behavior change.
- No verification of every Envoy proto field.
- No vendored TLA+ tools jar or downloaded binary.

## Architecture

```mermaid
flowchart LR
    Policy[Policy CRDs] --> PolicyIR[Policy IR]
    Gateway[Gateway and HTTPRoute] --> GatewayIR[Gateway IR with policies attached]
    PolicyIR --> GatewayIR
    GatewayIR --> XDS[Envoy xDS snapshot]
    XDS --> GoCheck[xdscheck Go invariants]
    Desired[Abstract desired snapshot] --> TLA[TLA+ ADS/SotW model]
    TLA --> ProtocolSafety[Publication safety invariants]
    GoCheck --> SnapshotSafety[Concrete snapshot closure findings]
```

## How to run

Run the Go validator tests:

```bash
go test ./pkg/kgateway/translator/xdscheck
```

Run the full formal MVP check script:

```bash
devel/formal/check.sh
```

Run the focused translator integration test:

```bash
go test ./pkg/kgateway/translator/gateway -run '^(TestTranslatedRedirectSnapshotPassesXDSCheck|TestTranslatedBackendSnapshotPassesXDSCheck|TestTranslatedOAuth2SnapshotPassesXDSCheck)$'
```

Run TLC directly when a TLA+ tools jar is available:

```bash
TLA2TOOLS_JAR=/path/to/tla2tools.jar devel/formal/tla/check.sh
```

Run TLC through Docker when host Java or a local jar is not available:

```bash
devel/formal/tla/check-docker.sh
```

The TLA+ script also looks for:

- `devel/formal/tla/tla2tools.jar`
- `tools/tla2tools.jar`

It prints install instructions if the jar is missing and does not download or vendor the jar.
The Docker script downloads the jar into a temporary host cache and mounts it into a Java container.

## Expected output

The Go test command should end with output like:

```text
ok  	github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator/xdscheck
```

When the TLA+ jar is installed, `devel/formal/tla/check.sh` should run TLC against `XdsAdsSotw.cfg` and report that the configured invariants hold. When the jar is not installed, `devel/formal/check.sh` runs the Go tests and skips TLC with explicit instructions.

## Definition of MVP correctness

- TLC checks the stated safety invariants in finite models of ADS/SotW publication.
- The TLA+ model is small enough to run locally and to produce counterexamples if safety guards such as dependency-closed send or matching nonce ACK handling are deliberately broken.
- Go tests cover valid and invalid xDS snapshots directly constructed from Envoy v3 protos.
- Warning findings are used for dynamic or unsupported constructs that cannot be verified statically by this MVP.

## Integration seam

The current checked-in integrations are:

- `TestTranslatedRedirectSnapshotPassesXDSCheck`, which runs an existing redirect-only HTTP Gateway fixture through the kgateway translator and checks the emitted LDS/RDS snapshot with `xdscheck`.
- `TestTranslatedBackendSnapshotPassesXDSCheck`, which runs an existing backend-producing HTTP Gateway fixture and checks emitted LDS/RDS/CDS/EDS resources with `xdscheck`.
- `TestTranslatedOAuth2SnapshotPassesXDSCheck`, which runs an existing OAuth2 policy fixture and checks emitted LDS/RDS/CDS/EDS/SDS resources, including OAuth2 HTTP filter secret references.

The SDS checker currently covers standard downstream and upstream TLS transport socket secret references, plus Envoy OAuth2 HTTP filter token and HMAC secret references. Existing HTTPS translator fixtures use inline certificate material, so TLS transport socket SDS coverage is exercised by focused `xdscheck` unit tests rather than a translator fixture.

The intended future translator-test seam is:

- Run kgateway IR -> xDS translation as existing tests already do.
- Convert the emitted listeners, routes, clusters, endpoints, and secrets into `xdscheck.Snapshot`.
- Call `xdscheck.CheckSnapshot`.
- Fail tests on error-severity findings.
- Allow warning-severity findings for dynamic or unverifiable constructs such as `cluster_header`.

This keeps the MVP non-invasive while making it straightforward to attach concrete xDS invariant checks after translation.

## Files

- `devel/formal/README.md`: overview, scope, commands, integration seam, and future work.
- `devel/formal/invariants.md`: invariant families for snapshot closure, publication safety, and dynamic out-of-scope cases.
- `devel/formal/check.sh`: developer runner for Go tests and optional TLC.
- `devel/formal/tla/XdsAdsSotw.tla`: abstract ADS/SotW publication model.
- `devel/formal/tla/XdsAdsSotw.cfg`: TLC configuration for the model.
- `devel/formal/tla/README.md`: model explanation and TLC usage.
- `devel/formal/tla/check.sh`: TLC runner.
- `devel/formal/tla/check-docker.sh`: Docker-based TLC runner that keeps downloaded tools outside the repository.
- `pkg/kgateway/translator/xdscheck`: concrete Envoy snapshot invariant checker and unit tests.

## Future work

1. Add SDS validation for additional recognized HTTP filter typed configs if kgateway emits them.
2. Add a delta xDS model.
3. Add a Lean, Dafny, F*, or Coq model for Gateway semantic IR -> abstract xDS snapshot compilation.
4. Generate random Gateway, HTTPRoute, and Policy inputs and check xDS invariants property-style.
5. Model Envoy warming behavior for LDS/RDS and CDS/EDS dependencies.
