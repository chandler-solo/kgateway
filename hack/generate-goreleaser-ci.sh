#!/usr/bin/env bash
# Generate single-arch goreleaser config for CI from the multi-arch source.
#
# Usage:
#   GOARCH=amd64 ./hack/generate-goreleaser-ci.sh
#   GOARCH=arm64 ./hack/generate-goreleaser-ci.sh
#
# This script transforms .goreleaser.yaml (multi-arch) into a single-arch config
# by filtering builds and docker images to the specified GOARCH.
# It expands YAML anchors/aliases, too.

set -euo pipefail

readonly ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE}")"/.. && pwd)"
GOARCH="${GOARCH:-amd64}"
SOURCE_FILE=".goreleaser.yaml"
OUTPUT_FILE=".goreleaser.ci-${GOARCH}.yaml"

case "$GOARCH" in
  amd64) RUST_BUILD_ARCH="x86_64" ;;
  arm64) RUST_BUILD_ARCH="aarch64" ;;
  *) echo "Unsupported GOARCH: $GOARCH" >&2; exit 1 ;;
esac

function yq() {
    go tool -modfile "$ROOT_DIR"/tools/go.mod yq "$@"
}

echo "Generating ${OUTPUT_FILE} for GOARCH=${GOARCH}..."

# expand all anchors/aliases
EXPANDED=$(yq eval 'explode(.)' "$SOURCE_FILE")

echo "$EXPANDED" | yq eval "
  .builds[].goarch = [\"${GOARCH}\"] |

  .dockers = [.dockers[] | select(.goarch == \"${GOARCH}\")] |

  .dockers[].build_flag_templates += [\"--load\"] |

  # docker manifests are not needed for a single arch
  del(.docker_manifests) |

  .changelog.disable = true |

  .release.disable = true
" > "$OUTPUT_FILE"

echo "Generated ${OUTPUT_FILE} successfully."
