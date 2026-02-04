#!/bin/bash

# Wrapper script for goreleaser that uses Helm templating for configuration
# This allows flexible selection of build targets, architectures, and images.
#
# Usage:
#   ./hack/goreleaser.sh [options] [-- goreleaser-args]
#
# Options:
#   --arch ARCH           Architecture to build (arm64, amd64, or both). Default: host arch
#   --images IMAGES       Comma-separated list of images to build (controller,sds,envoyinit,agentgateway)
#                         Default: all images
#   --mode MODE           Build mode: local or release. Default: local
#   --manifests           Enable docker manifest creation (multi-arch)
#   --caching             Enable registry-based docker build caching
#   --dry-run             Generate config only, don't run goreleaser
#   --values FILE         Additional Helm values file
#   --set KEY=VALUE       Set individual Helm values (can be repeated)
#   -h, --help            Show this help message
#
# Concurrency:
#   This script is safe to run concurrently. Each invocation uses unique
#   temporary files and a unique goreleaser dist directory.
#
# Examples:
#   # Build only controller for local arch
#   ./hack/goreleaser.sh --images controller -- --snapshot --clean
#
#   # Build all images for arm64
#   ./hack/goreleaser.sh --arch arm64 -- --snapshot --clean
#
#   # Build controller and sds for amd64 with caching
#   ./hack/goreleaser.sh --arch amd64 --images controller,sds --caching -- --snapshot --clean
#
#   # Full release build with manifests
#   ./hack/goreleaser.sh --arch arm64,amd64 --mode release --manifests
#
#   # Pass additional args to goreleaser
#   ./hack/goreleaser.sh --images controller -- --snapshot --clean

set -o errexit
set -o nounset
set -o pipefail

readonly ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly CHART_DIR="${ROOT_DIR}/.goreleaser"

# Default values
ARCH=""
IMAGES=""
MODE="local"
MANIFESTS="false"
CACHING="false"
DRY_RUN="false"
VALUES_FILES=()
SET_VALUES=()
GORELEASER_ARGS=()

# Create unique temporary files/directories for concurrent execution safety
# Use a combination of PID and random suffix to ensure uniqueness
UNIQUE_ID="$$-$(date +%s)-${RANDOM}"
CONFIG_FILE="${ROOT_DIR}/_output/goreleaser-${UNIQUE_ID}.yaml"
DIST_DIR="${ROOT_DIR}/dist-${UNIQUE_ID}"

# Cleanup function to remove temporary files
cleanup() {
    local exit_code=$?
    rm -f "$CONFIG_FILE" 2>/dev/null || true
    # Only remove dist dir if it exists and we created it
    if [[ -d "$DIST_DIR" ]]; then
        rm -rf "$DIST_DIR" 2>/dev/null || true
    fi
    exit $exit_code
}

# Set up trap to clean up on exit (success or failure)
trap cleanup EXIT INT TERM

usage() {
    sed -n '/^# Usage:/,/^set -o errexit/p' "$0" | grep '^#' | sed 's/^# \?//'
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --arch)
            ARCH="$2"
            shift 2
            ;;
        --images)
            IMAGES="$2"
            shift 2
            ;;
        --mode)
            MODE="$2"
            shift 2
            ;;
        --manifests)
            MANIFESTS="true"
            shift
            ;;
        --caching)
            CACHING="true"
            shift
            ;;
        --dry-run)
            DRY_RUN="true"
            shift
            ;;
        --values)
            VALUES_FILES+=("$2")
            shift 2
            ;;
        --set)
            SET_VALUES+=("$2")
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        --)
            shift
            GORELEASER_ARGS=("$@")
            break
            ;;
        *)
            echo "Unknown option: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
done

# Detect host architecture if not specified
if [[ -z "$ARCH" ]]; then
    case "$(uname -m)" in
        x86_64)  ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        arm64)   ARCH="arm64" ;;
        *)
            echo "Unable to detect architecture, please specify --arch" >&2
            exit 1
            ;;
    esac
fi

# Ensure output directory exists
mkdir -p "${ROOT_DIR}/_output"

# Build Helm values
HELM_ARGS=()

# Add values files
for f in "${VALUES_FILES[@]+"${VALUES_FILES[@]}"}"; do
    HELM_ARGS+=("-f" "$f")
done

# Convert comma-separated arch to YAML list
IFS=',' read -ra ARCH_LIST <<< "$ARCH"

# Build set arguments for architectures
# Helm --set syntax for arrays: architectures={arm64,amd64}
HELM_ARGS+=("--set" "architectures={$(IFS=','; echo "${ARCH_LIST[*]}")}")

# Handle images selection
if [[ -n "$IMAGES" ]]; then
    # First disable all images
    HELM_ARGS+=("--set" "images.controller=false")
    HELM_ARGS+=("--set" "images.sds=false")
    HELM_ARGS+=("--set" "images.envoyinit=false")
    HELM_ARGS+=("--set" "images.agentgateway=false")

    # Then enable requested ones
    IFS=',' read -ra IMAGE_LIST <<< "$IMAGES"
    for img in "${IMAGE_LIST[@]}"; do
        case "$img" in
            controller|sds|envoyinit|agentgateway)
                HELM_ARGS+=("--set" "images.$img=true")
                ;;
            *)
                echo "Unknown image: $img" >&2
                echo "Valid images: controller, sds, envoyinit, agentgateway" >&2
                exit 1
                ;;
        esac
    done
fi

# Set mode
HELM_ARGS+=("--set" "mode=$MODE")

# Set manifests
HELM_ARGS+=("--set" "createManifests=$MANIFESTS")

# Set caching
HELM_ARGS+=("--set" "caching.enabled=$CACHING")

# Set unique dist directory for concurrent execution safety
HELM_ARGS+=("--set" "dist=$DIST_DIR")

# Add any additional --set values
for sv in "${SET_VALUES[@]+"${SET_VALUES[@]}"}"; do
    HELM_ARGS+=("--set" "$sv")
done

# Generate the goreleaser config using Helm
echo "Generating goreleaser config..."
echo "  Architecture(s): ${ARCH_LIST[*]}"
echo "  Images: ${IMAGES:-all}"
echo "  Mode: $MODE"
echo "  Manifests: $MANIFESTS"
echo "  Caching: $CACHING"
echo "  Config: $CONFIG_FILE"
echo "  Dist: $DIST_DIR"

helm template goreleaser-config "$CHART_DIR" "${HELM_ARGS[@]}" > "$CONFIG_FILE"

if [[ "$DRY_RUN" == "true" ]]; then
    echo "Dry run mode - not running goreleaser"
    echo "---"
    cat "$CONFIG_FILE"
    exit 0
fi

# Ensure buildx builder exists (required for registry cache)
# We use BUILDX_BUILDER env var instead of 'docker buildx use' to avoid changing global state
docker buildx inspect goreleaser >/dev/null 2>&1 || docker buildx create --name goreleaser --driver docker-container

# Run goreleaser with the generated config
# BUILDX_BUILDER tells docker buildx which builder to use without changing the global default
# The dist directory is set in the config file for concurrent execution safety
echo "Running goreleaser..."
GORELEASER="${GORELEASER:-go tool -modfile=tools/go.mod goreleaser}"
BUILDX_BUILDER=goreleaser $GORELEASER release --config "$CONFIG_FILE" "${GORELEASER_ARGS[@]+"${GORELEASER_ARGS[@]}"}"
