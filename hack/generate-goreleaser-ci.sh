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
OUTPUT_DIR="_output"
OUTPUT_FILE="${OUTPUT_DIR}/.goreleaser.ci-${GOARCH}.yaml"

mkdir -p "$OUTPUT_DIR"

function yq() {
    go tool -modfile "$ROOT_DIR"/tools/go.mod yq "$@"
}

echo "Generating ${OUTPUT_FILE} for GOARCH=${GOARCH}..."

# expand all anchors/aliases
EXPANDED=$(yq eval 'explode(.)' "$SOURCE_FILE")

echo "$EXPANDED" | yq eval "
  .builds[].goarch = [\"${GOARCH}\"] |

  .dockers = [.dockers[] | select(.goarch == \"${GOARCH}\")] |

  # Remove --cache-to (not supported with --load), keep --cache-from, add --load
  .dockers = [.dockers[] | .build_flag_templates = ([.build_flag_templates[] | select(test(\"^--cache-to\") | not)] + [\"--load\"])] |

  # docker manifests are not needed for a single arch
  del(.docker_manifests) |

  .changelog.disable = true |

  .release.disable = true
" > "$OUTPUT_FILE"

echo "Generated ${OUTPUT_FILE} successfully."
