# Custom Kind Node Image for Faster Local Development

## Overview

This directory contains configuration for building a custom Kind node image with preloaded container images. This significantly speeds up local development by avoiding repeated image pulls during `make run`.

## Quick Start

### 1. Build the custom Kind node image

```bash
make kind-build-node-image
```

This builds a Docker image named `kgateway-kind-node:v1.34.0` (version matches `CLUSTER_NODE_VERSION`) with preloaded containers.

### 2. Use the custom image

```bash
USE_CUSTOM_KIND_IMAGE=true make run
```

Or for just creating the cluster:

```bash
USE_CUSTOM_KIND_IMAGE=true make kind-create
```

## What's Preloaded?

A bunch of stuff that, based on static analysis, we were using or could use during the development cycle. If we got it wrong, you can correct it, but you don't have to -- it just slows you down a bit when you have to pull an image over the network.

More images can be added to `hack/kind/Dockerfile.kind-node` as desired.

## How It Works

1. **Dockerfile**: `hack/kind/Dockerfile.kind-node` uses a multi-stage build:
   - **Stage 1**: Uses `skopeo` to pull container images and save them as docker-archive tarballs
   - **Stage 2**: Starts from the official `kindest/node` image, copies the tarballs, then imports them into containerd at startup using `ctr`

2. **Makefile**: New target `kind-build-node-image` builds the custom image. The `kind-create` target supports `USE_CUSTOM_KIND_IMAGE=true` to use the custom image.

3. **setup-kind.sh**: The script now checks the `USE_CUSTOM_KIND_IMAGE` environment variable and uses the appropriate image.

## Benefits

- **Faster startup**: Images are already present in the node, so `kind load docker-image` is a cache hit
- **Reduced network usage**: No need to pull the same images repeatedly
- **Consistent versions**: Ensures everyone uses the same preloaded image versions

## Maintenance

### Adding More Images

Edit `hack/kind/Dockerfile.kind-node` and add additional `skopeo copy` commands in the image-puller stage:

```dockerfile
RUN skopeo copy docker://quay.io/example/image:v1.2.3 docker-archive:/images/example-image.tar:quay.io/example/image:v1.2.3
```

Then rebuild:

```bash
make kind-build-node-image
```

Note: The images are pulled using `skopeo` in the first stage, then imported into containerd at startup in the second stage automatically.

### Updating Kind Version

When updating `CLUSTER_NODE_VERSION` in the Makefile and `setup-kind.sh`, rebuild the custom image to stay in sync:

```bash
make kind-build-node-image
```

## Architecture Notes

- The custom image uses the containerd namespace `k8s.io` which is what Kind/Kubernetes uses
- Images are pulled using `ctr` (the containerd CLI) rather than `docker pull`
- The Kind cluster uses containerd as its container runtime, not Docker
- When `kind load docker-image` is called, it checks the containerd store first (cache hit with preloaded images)

## Troubleshooting

### Image not found

If you get an error about the custom image not being found:

```bash
# Rebuild the image
make kind-build-node-image

# Verify it exists
docker images | grep kgateway-kind-node
```

### Want to force standard image

Simply omit the `USE_CUSTOM_KIND_IMAGE` variable or set it to false:

```bash
make kind-create
# or
USE_CUSTOM_KIND_IMAGE=false make run
```
