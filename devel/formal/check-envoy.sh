#!/usr/bin/env sh
# Required when invoked: missing Docker/image or either profile failing is fatal.
set -eu
ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
ARTIFACT_DIR=${FORMAL_ENVOY_ARTIFACT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/xds-envoy.XXXXXX")}
mkdir -p "$ARTIFACT_DIR"
ARTIFACT_DIR=$(CDPATH= cd -- "$ARTIFACT_DIR" && pwd)
cd "$ROOT_DIR"
go build -tags e2e -o "$ARTIFACT_DIR/envoyprobe" ./devel/formal/cmd/envoyprobe
"$ARTIFACT_DIR/envoyprobe" -out "$ARTIFACT_DIR/default"
"$ARTIFACT_DIR/envoyprobe" -disable-panic -out "$ARTIFACT_DIR/no-panic"
"$ARTIFACT_DIR/envoyprobe" -snapshot-cache -out "$ARTIFACT_DIR/snapshot-cache"
"$ARTIFACT_DIR/envoyprobe" -snapshot-cache -ordered -out "$ARTIFACT_DIR/ordered-cache"
rm "$ARTIFACT_DIR/envoyprobe"
echo "Direct Envoy receipts: $ARTIFACT_DIR"
