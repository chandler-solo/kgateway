# kgateway Helm Chart

This is the core kgateway control plane Helm chart. It deploys the kgateway controller which implements the Kubernetes Gateway API for both Envoy and agentgateway data planes.

## Overview

The kgateway control plane is a dual control plane that can manage:
- **Envoy data plane**: Traditional API gateway and ingress use cases
- **agentgateway data plane**: Agent connectivity across MCP servers, A2A agents, and REST APIs

This chart is designed to be used directly or as a subchart embedded in the following wrapper charts:
- `kgateway-envoy`: Pre-configured for Envoy-only deployments
- `kgateway-agentgateway`: Pre-configured for agentgateway-only deployments

## Installation

### Direct Installation

Don't. Use one of the wrapper charts.

### Using Wrapper Charts

For simplified installation with a specific data plane:

```bash
# Install CRDs first (assumes Gateway API is installed):
helm install kgateway-crds ./kgateway-crds

# For Envoy-only deployments:
helm install kgateway ./kgateway-envoy

# Or for agentgateway-only deployments:
helm install kgateway ./kgateway-agentgateway
```

## Configuration

### Data Plane Selection

| Parameter | Description | Default |
|-----------|-------------|---------|
| `envoy.enabled` | Enable Envoy data plane integration | `true` |
| `agentgateway.enabled` | Enable agentgateway data plane integration | `true` |

At least one data plane must be enabled.

### Key Configuration Options

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controller.replicaCount` | Number of controller replicas | `1` |
| `controller.logLevel` | Controller log level | `info` |
| `controller.image.registry` | Image registry | `""` (uses default) |
| `controller.image.repository` | Image repository | `kgateway` |
| `controller.image.tag` | Image tag | `""` (uses appVersion) |
| `inferenceExtension.enabled` | Enable Gateway API Inference Extension | `false` |
| `discoveryNamespaceSelectors` | Namespace selectors for config discovery | `[]` |
| `validation.level` | Validation level (standard/strict) | `standard` |

### Image Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.registry` | Default image registry | `cr.kgateway.dev/kgateway-dev` |
| `image.tag` | Default image tag | `""` (uses appVersion) |
| `image.pullPolicy` | Default image pull policy | `IfNotPresent` |
| `imagePullSecrets` | Image pull secrets | `[]` |

### Service Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controller.service.type` | Service type | `ClusterIP` |
| `controller.service.ports.grpc` | gRPC xDS port (Envoy) | `9977` |
| `controller.service.ports.agwGrpc` | gRPC xDS port (agentgateway) | `9978` |
| `controller.service.ports.health` | Health check port | `9093` |
| `controller.service.ports.metrics` | Metrics port | `9092` |

### TLS Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controller.xds.tls.enabled` | Enable TLS for xDS servers | `false` |

When TLS is enabled, create a Secret named `kgateway-xds-cert` with `tls.crt`, `tls.key`, and `ca.crt`.

### Pod Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `serviceAccount.create` | Create service account | `true` |
| `serviceAccount.name` | Service account name | `""` (generated) |
| `deploymentAnnotations` | Deployment annotations | `{}` |
| `podAnnotations` | Pod annotations | `{prometheus.io/scrape: "true"}` |
| `podSecurityContext` | Pod security context | `{}` |
| `securityContext` | Container security context | `{}` |
| `resources` | Resource requests/limits | `{}` |
| `nodeSelector` | Node selector | `{}` |
| `tolerations` | Tolerations | `[]` |
| `affinity` | Affinity rules | `{}` |

## Using as a Subchart

This chart can be embedded as a subchart in wrapper charts. Add it as a dependency in your `Chart.yaml`:

```yaml
dependencies:
  - name: kgateway
    version: "v0.0.1"
    repository: "file://../kgateway"
```

Then configure it in your `values.yaml`:

```yaml
kgateway:
  envoy:
    enabled: true
  agentgateway:
    enabled: false
```

For detailed configuration options, see the `values.yaml` file.
