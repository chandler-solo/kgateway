# Overview

This directory contains the Helm charts to deploy the kgateway project via [Helm](https://helm.sh/docs/helm/helm_install/).

## Directory Structure

- `kgateway-crds/`: Contains the Custom Resource Definitions (CRDs) chart
  - This chart must be installed before the main kgateway chart
  - Generated from API definitions in `api/v1alpha1`

- `kgateway/`: Contains the core control plane chart
  - Deploys the control plane components that extend the Kubernetes Gateway API
  - Supports both Envoy and agentgateway data planes
  - Can be used directly or as a subchart

- `kgateway-envoy/`: Wrapper chart for Envoy-only deployments
  - Embeds `kgateway` as a subchart
  - Pre-configured with Envoy enabled and agentgateway disabled
  - Ideal for traditional API gateway and ingress use cases

- `kgateway-agentgateway/`: Wrapper chart for agentgateway-only deployments
  - Embeds `kgateway` as a subchart
  - Pre-configured with agentgateway enabled and Envoy disabled
  - Ideal for agent connectivity across MCP servers, A2A agents, and REST APIs

## Installation

### Option 1: Full Control (Both Data Planes)

Use the core `kgateway` chart when you need both Envoy and agentgateway:

```bash
# Install CRDs first
helm install kgateway-crds ./kgateway-crds

# Install the control plane with both data planes
helm install kgateway ./kgateway
```

### Option 2: Envoy Only

Use `kgateway-envoy` for traditional API gateway deployments:

```bash
# Install CRDs first
helm install kgateway-crds ./kgateway-crds

# Install with Envoy data plane
helm install kgateway ./kgateway-envoy
```

### Option 3: agentgateway Only

Use `kgateway-agentgateway` for agent connectivity use cases:

```bash
# Install CRDs first
helm install kgateway-crds ./kgateway-crds

# Install with agentgateway data plane
helm install kgateway ./kgateway-agentgateway
```

## Chart Dependencies

The wrapper charts (`kgateway-envoy` and `kgateway-agentgateway`) embed the core `kgateway` chart as a subchart.

### Automatic Dependency Management

The Makefile automatically tracks changes to the core `kgateway` chart and runs `helm dependency update` when needed. This means:

- **`make lint-kgateway-charts`** and **`make package-kgateway-charts`** automatically update dependencies if the core chart has changed
- Subsequent runs skip the dependency update if nothing has changed (using stamp files in `_output/stamps/`)
- You rarely need to run dependency commands manually

### Manual Dependency Commands

For cases where you need manual control:

| Command | When to Use |
|---------|-------------|
| `make helm-dependency-build` | Rebuild from `Chart.lock` after cloning or `git pull`. Fast and reproducible. |
| `make helm-dependency-update` | Force regenerate `Chart.lock`. Use after changing `Chart.yaml` versions. |

```bash
# After cloning the repo or git pull
make helm-dependency-build

# Force update (rarely needed - Makefile handles this automatically)
make helm-dependency-update
```

### Using Published Charts

When installing from a Helm repository (not from source), dependencies are already bundled:

```bash
helm install kgateway oci://cr.kgateway.dev/kgateway-dev/charts/kgateway-envoy
```

## Development

### Makefile Targets

| Target | Description |
|--------|-------------|
| `make lint-kgateway-charts` | Lint all charts (auto-updates deps if needed) |
| `make package-kgateway-charts` | Package all charts (auto-updates deps if needed) |
| `make release-charts` | Push all charts to OCI registry |
| `make helm-dependency-build` | Build dependencies from `Chart.lock` |
| `make helm-dependency-update` | Force update `Chart.lock` and rebuild dependencies |

For detailed configuration options, please refer to the `README.md` and `values.yaml` files in each chart directory.
