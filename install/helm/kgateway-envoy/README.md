# kgateway-envoy Helm Chart

This chart deploys kgateway pre-configured for Envoy data plane deployments. It embeds the core `kgateway` chart as a subchart with Envoy enabled and agentgateway disabled.

## Overview

Use this chart when you want to deploy kgateway as a traditional API gateway or ingress controller using Envoy as the data plane. This chart simplifies installation by:

- Enabling Envoy integration by default
- Disabling agentgateway integration
- Exposing only the configuration options relevant to Envoy deployments

## Prerequisites

- Kubernetes 1.25+
- Helm 3.x
- kgateway CRDs installed (via `kgateway-crds` chart)

## Installation

```bash
# Install CRDs first (if not already installed)
helm install kgateway-crds ../kgateway-crds

# Install kgateway with Envoy data plane
helm install kgateway ./kgateway-envoy
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
| `kgateway.waypoint.enabled` | Enable Istio waypoint integration | `false` |

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
| `kgateway.controller.service.ports.grpc` | gRPC xDS port | `9977` |
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
| `kgateway.envoy.enabled` | `true` | This chart is for Envoy deployments |
| `kgateway.agentgateway.enabled` | `false` | agentgateway is not used in Envoy-only deployments |

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
helm upgrade kgateway ./kgateway-envoy -f my-values.yaml
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
