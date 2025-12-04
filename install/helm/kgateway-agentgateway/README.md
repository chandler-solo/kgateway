# kgateway-agentgateway Helm Chart

TODO(chandler): DLC: will we use cr.agentgateway.dev for hosting charts as OCI artifacts?

This chart deploys kgateway pre-configured for agentgateway data plane deployments. It embeds the core `kgateway` chart as a subchart with agentgateway enabled and Envoy disabled.

## Overview

Use this chart when you want to deploy kgateway to manage agent connectivity across MCP servers, A2A agents, and REST APIs using agentgateway as the data plane. This chart simplifies installation by:

- Enabling agentgateway integration by default
- Disabling Envoy integration
- Exposing only the configuration options relevant to agentgateway deployments

## Prerequisites

- Kubernetes 1.25+
- Helm 3.x
- kgateway CRDs installed (via `kgateway-crds` chart)

## Installation

```bash
# Install CRDs first (if not already installed)
helm install kgateway-crds ../kgateway-crds

# Install kgateway with agentgateway data plane
helm install kgateway ./kgateway-agentgateway
```

## Configuration

All configuration is passed through to the embedded `kgateway` subchart under the `kgateway` key.

### Key Configuration Options

| Parameter | Description | Default |
|-----------|-------------|---------|
| `kgateway.controller.replicaCount` | Number of controller replicas | `1` |
| `kgateway.controller.logLevel` | Controller log level | `info` |
| `kgateway.inferenceExtension.enabled` | Enable Gateway API Inference Extension | `false` |
| `kgateway.discoveryNamespaceSelectors` | Namespace selectors for config discovery | `[]` |
| `kgateway.validation.level` | Validation level (standard/strict) | `standard` |

### Image Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `kgateway.image.registry` | Default image registry | `cr.kgateway.dev/kgateway-dev` |
| `kgateway.image.tag` | Default image tag | `""` (uses appVersion) |
| `kgateway.image.pullPolicy` | Default image pull policy | `IfNotPresent` |
| `kgateway.imagePullSecrets` | Image pull secrets | `[]` |

### Service Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `kgateway.controller.service.type` | Service type | `ClusterIP` |
| `kgateway.controller.service.ports.agwGrpc` | gRPC xDS port (agentgateway) | `9978` |
| `kgateway.controller.service.ports.health` | Health check port | `9093` |
| `kgateway.controller.service.ports.metrics` | Metrics port | `9092` |

### Pod Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `kgateway.serviceAccount.create` | Create service account | `true` |
| `kgateway.resources` | Resource requests/limits | `{}` |
| `kgateway.nodeSelector` | Node selector | `{}` |
| `kgateway.tolerations` | Tolerations | `[]` |
| `kgateway.affinity` | Affinity rules | `{}` |

## Fixed Settings

The following settings are fixed in this chart and cannot be overridden:

| Setting | Value | Reason |
|---------|-------|--------|
| `kgateway.envoy.enabled` | `false` | Envoy is not used in agentgateway-only deployments |
| `kgateway.agentgateway.enabled` | `true` | This chart is for agentgateway deployments |

## Inference Extension

When using agentgateway for AI workloads, you may want to enable the Gateway API Inference Extension:

```yaml
kgateway:
  inferenceExtension:
    enabled: true
```

This enables routing to AI inference workloads like LLMs running in your Kubernetes cluster.

## Example Values

```yaml
kgateway:
  controller:
    replicaCount: 2
    logLevel: debug
    resources:
      requests:
        cpu: 100m
        memory: 128Mi
      limits:
        cpu: 500m
        memory: 512Mi

  inferenceExtension:
    enabled: true

  validation:
    level: strict
```

## Upgrading

To upgrade an existing installation:

```bash
helm upgrade kgateway ./kgateway-agentgateway -f my-values.yaml
```

## Uninstalling

To uninstall the chart:

```bash
helm uninstall kgateway
```

Note: CRDs are not deleted automatically. To remove them:

```bash
helm uninstall kgateway-crds
```
