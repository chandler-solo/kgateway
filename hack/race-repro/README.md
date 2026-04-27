# race-repro

Standalone reproducer for the kgateway xDS race in
`pkg/kgateway/proxy_syncer/perclient.go::snapshotPerClient`.

## What it does

The control-plane race: when a kgateway controller pod restarts, Envoy
reconnects under a new Uniquely Connected Client (UCC). The per-client
cluster transformation can race with the snapshot transformation, producing
a partial CDS that omits clusters referenced by the active listener/route.
Affected requests get response flag `NC` ("no cluster") and HTTP 500/503,
depending on the Envoy response path.

This program:

1. Drives sustained HTTP traffic at the gateway data plane (default 50
   concurrent workers, no keepalive) for a configurable duration.
2. Repeatedly hard-deletes the controller pod(s) (`--grace-period=0 --force`)
   on a configurable interval, using `kubectl` already on `$PATH`.
3. Records every non-2xx response and every transport-level error, with
   timestamps and bodies, and prints periodic + final summaries.

It bypasses the e2e test framework entirely: it talks to whatever URL you
give it and uses raw `kubectl` to bounce the controller. So it can be aimed
at any cluster (kind, EKS, GKE, etc.) in which a kgateway gateway and
controller already exist.

## Prerequisites

You need an already-installed kgateway (kind or any other cluster). The data
plane must be reachable — typically via `kubectl port-forward` to the
gateway service:

```bash
# In one terminal: forward the gateway service to 127.0.0.1:8080.
# Note: --address 127.0.0.1 is required on some macOS setups; without it,
# kubectl port-forward may bind in a way that responds with 404s from envoy.
kubectl port-forward -n default --address 127.0.0.1 svc/gw 8080:8080
```

For a vanilla kind setup with the e2e installer this also satisfies the
default values (`--controller-namespace=zero-downtime`,
`--controller-label=kgateway=kgateway`, `--host=example.com`).

If your gateway isn't named `gw` or your route isn't matching `example.com`,
override `--url` and `--host`.

## Usage

```bash
# Default settings: 50 concurrent workers, 5 minutes, restart every 4s.
go run ./hack/race-repro

# More aggressive: 200 workers, 10 minutes, restart every 2s, fail fast.
go run ./hack/race-repro \
    --concurrency=200 \
    --duration=10m \
    --restart-interval=2s \
    --stop-on-error

# Different cluster topology:
go run ./hack/race-repro \
    --url=https://example.acme.dev/ping \
    --host=ping.example.com \
    --controller-namespace=kgateway-system \
    --controller-label=app.kubernetes.io/name=kgateway

# Sanity check without restarts — confirms baseline traffic stays clean.
go run ./hack/race-repro --skip-delete --duration=30s

# Rotate across many hostnames so traffic samples every generated HTTPRoute.
go run ./hack/race-repro \
    --host-template='repro-%d.example.com' \
    --host-count=200 \
    --qps=1000 \
    --concurrency=50
```

## Flags

| Flag                       | Default                    | Purpose                                                            |
| -------------------------- | -------------------------- | ------------------------------------------------------------------ |
| `--url`                    | `http://127.0.0.1:8080`    | Target URL (typically a `kubectl port-forward` address)            |
| `--host`                   | `example.com`              | Host header                                                        |
| `--hosts`                  | empty                      | Comma-separated Host headers to rotate through                     |
| `--host-template`          | empty                      | `fmt` template for generated Host headers, such as `repro-%d.example.com` |
| `--host-count`             | `0`                        | Number of generated Host headers to rotate through                 |
| `--concurrency`            | `50`                       | Number of concurrent HTTP workers                                  |
| `--qps`                    | `0`                        | Optional aggregate request rate cap across workers                 |
| `--duration`               | `5m`                       | Total run duration                                                 |
| `--restart-interval`       | `4s`                       | Time between controller pod hard-deletes                           |
| `--controller-namespace`   | `zero-downtime`            | Namespace where the kgateway controller runs                       |
| `--controller-label`       | `kgateway=kgateway`        | Label selector for the controller pod(s)                           |
| `--request-timeout`        | `3s`                       | Per-request timeout                                                |
| `--stats-every`            | `2s`                       | Periodic stats interval                                            |
| `--keepalive`              | `false`                    | Re-enable HTTP keepalive (default off — disabled to widen window)  |
| `--stop-on-error`          | `false`                    | Stop immediately on first non-2xx                                  |
| `--skip-delete`            | `false`                    | Skip controller deletes (baseline run)                             |
| `--kubectl`                | `kubectl`                  | Path to kubectl binary                                             |

## Output

Periodic line every `--stats-every`:

```
requests=23145 ok=23145 transport=0 timeouts=0
[restart 5] 14:23:11.582 (102ms elapsed; cumulative: requests=23510 ok=23510 transport=0 timeouts=0)
```

A non-empty `transport=`, `timeouts=`, or any `5xx=N` block (e.g. `503=12`)
indicates the race fired. The tail of the run prints up to 32 sampled
errors with timestamp, status code, and response body — the body is
typically empty for an Envoy NC, which is the strongest in-band signal.

Process exit code is `1` if any error was observed, `0` otherwise — useful
for CI loops.

## Confirming response flag NC

The HTTP client usually sees `500` or `503` with an empty body. To confirm
the upstream flag is actually `NC` (and not e.g. `UH`/`UF`), turn on Envoy access
logging on the gateway and tail it during the run. With kgateway you can do
this via an `HTTPListenerPolicy` with `accessLog` set, or by attaching to
the Envoy admin stats endpoint and watching `cluster_not_found`.

## v2.2.1 production-image repro

This reproduced locally on kind with the published v2.2.1 images:

- Controller: `cr.kgateway.dev/kgateway-dev/kgateway:v2.2.1`
- Data plane: `cr.kgateway.dev/kgateway-dev/envoy-wrapper:v2.2.1`
- Istio integration enabled, with `DestinationRule` CRDs installed
- Two Gateways with 3 and 6 Envoy pods
- About 200 HTTPRoutes, 200 backend Services, and 200 DestinationRules
- Traffic rotated across all generated route hosts at 5k aggregate qps

Install the published chart and enable the per-client DestinationRule path:

```bash
helm upgrade --install kgateway-crds \
  oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-crds \
  --version v2.2.1 \
  --namespace zero-downtime \
  --create-namespace \
  --reset-values

kubectl apply -f pkg/kgateway/setup/testdata/istio_crds_setup/crds.yaml

helm upgrade --install kgateway \
  oci://cr.kgateway.dev/kgateway-dev/charts/kgateway \
  --version v2.2.1 \
  --namespace zero-downtime \
  --create-namespace \
  --reset-values \
  --set controller.replicaCount=1 \
  --set controller.extraEnv.KGW_ENABLE_ISTIO_INTEGRATION=true
```

Generate 200 host-specific routes and 200 matching DestinationRules. The
route hostnames must match the traffic generator's `--host-template`, and
each DestinationRule host must match the generated Service FQDN:

```bash
(
for i in $(seq 1 200); do cat <<EOF
---
apiVersion: v1
kind: Service
metadata:
  name: repro-svc-$i
  namespace: default
spec:
  selector:
    app.kubernetes.io/name: nginx
  ports:
  - name: http
    port: 8080
    targetPort: http-web-svc
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: repro-route-$i
  namespace: default
spec:
  parentRefs:
  - name: gw
  hostnames:
  - repro-$i.example.com
  rules:
  - backendRefs:
    - name: repro-svc-$i
      port: 8080
---
apiVersion: networking.istio.io/v1alpha3
kind: DestinationRule
metadata:
  name: repro-dr-$i
  namespace: default
spec:
  host: repro-svc-$i.default.svc.cluster.local
  trafficPolicy:
    outlierDetection:
      consecutive5xxErrors: 7
      interval: 5m
      baseEjectionTime: 15m
    loadBalancer:
      localityLbSetting:
        failoverPriority:
        - topology.kubernetes.io/region
        - topology.kubernetes.io/zone
        - topology.istio.io/subzone
EOF
done
) | kubectl apply -f -
```

For the two-Gateway 5k qps run, repeat the HTTPRoute portion for `gw2`
using hostnames `repro2-$i.example.com`. The same generated Services and
DestinationRules can be shared by both Gateways.

In kind, all gateway pods share the same labels/locality, so they collapse
to a single unique xDS client. To exercise the production-shaped per-client
path, make the gateway pod labels distinct before the controller reconnect:

```bash
i=0
for p in $(kubectl get pods -n default -l gateway.networking.k8s.io/gateway-name=gw -o name); do
  i=$((i+1))
  kubectl label -n default "$p" race-repro-client=gw-$i --overwrite
done
```

Run traffic inside the cluster against the Gateway Service IPs rather than
DNS names. This avoids CoreDNS becoming the failure source at high qps while
still preserving Host-header routing:

Attach an NC-only access log policy first so Envoy confirms the response
flag:

```yaml
apiVersion: gateway.kgateway.dev/v1alpha1
kind: ListenerPolicy
metadata:
  name: nc-access-log-gw
  namespace: default
spec:
  targetRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: gw
  default:
    httpSettings:
      accessLog:
      - fileSink:
          path: /dev/stdout
          stringFormat: "NC_ACCESS_LOG gateway=gw host=%REQ(:AUTHORITY)% code=%RESPONSE_CODE% flags=%RESPONSE_FLAGS% cluster=%UPSTREAM_CLUSTER% route=%ROUTE_NAME%\\n"
        filter:
          responseFlagFilter:
            flags:
            - NC
```

```bash
kubectl exec -n race-repro race-runner -- /tmp/race-repro \
  --url=http://<gw-service-cluster-ip>:8080 \
  --host-template='repro-%d.example.com' \
  --host-count=200 \
  --concurrency=100 \
  --qps=2500 \
  --duration=90s \
  --keepalive \
  --skip-delete
```

While the two traffic commands are running, hard-delete the controller pod:

```bash
kubectl delete pod -n zero-downtime -l kgateway=kgateway \
  --grace-period=0 \
  --force \
  --wait=false
```

In the confirmed run, hard-deleting the running controller pod during
traffic produced empty-body 500s and Envoy logged:

```text
NC_ACCESS_LOG gateway=gw2 host=repro2-11.example.com code=500 flags=NC cluster=-
```

## Two-controller-replica run

The customer report is from a deployment with two controller replicas. To
reproduce that topology and observe behavior:

```bash
kubectl scale -n zero-downtime deployment/kgateway --replicas=2
kubectl rollout status -n zero-downtime deployment/kgateway

go run ./hack/race-repro --duration=5m --restart-interval=4s
```

The default `kubectl delete pod -l kgateway=kgateway` deletes both replicas
each tick, so the loop continues to exercise the cold-start race even with
multiple replicas. If you want to delete only one replica per tick, scope
the label selector or the pod name explicitly.

## Why this exists separately from the e2e test

The framework-driven test uses `hey` running in an in-cluster pod and
reports an error only if `hey` itself emits its "Error distribution"
section. Brief 5xx bursts during a restart can be lost in the report
formatting. This standalone tool counts every request, samples error
bodies, and runs from outside the cluster, which makes failure modes
easier to observe and easier to share with customers.
