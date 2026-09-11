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
"$ARTIFACT_DIR/envoyprobe" -scenario references -out "$ARTIFACT_DIR/references"
"$ARTIFACT_DIR/envoyprobe" -scenario references -snapshot-cache -out "$ARTIFACT_DIR/references-cache"
"$ARTIFACT_DIR/envoyprobe" -scenario references -snapshot-cache -ordered -out "$ARTIFACT_DIR/references-ordered-cache"
"$ARTIFACT_DIR/envoyprobe" -scenario rejection -out "$ARTIFACT_DIR/rejection"
"$ARTIFACT_DIR/envoyprobe" -scenario rejection -snapshot-cache -out "$ARTIFACT_DIR/rejection-cache"
"$ARTIFACT_DIR/envoyprobe" -scenario secrets -out "$ARTIFACT_DIR/secrets"
"$ARTIFACT_DIR/envoyprobe" -scenario restart -snapshot-cache -out "$ARTIFACT_DIR/restart-cache"
"$ARTIFACT_DIR/envoyprobe" -scenario restart -snapshot-cache -ordered -out "$ARTIFACT_DIR/restart-ordered-cache"
# RF-028 bootstrap profiles: the EDS cache flag with the timeout disabled keeps the pause;
# kgateway's shape (flag on, EDS/RDS initial_fetch_timeout unset = 15 s) completes from the cache.
"$ARTIFACT_DIR/envoyprobe" -scenario restart -snapshot-cache -eds-cache -out "$ARTIFACT_DIR/restart-cache-eds-cache-disabled-timeout"
"$ARTIFACT_DIR/envoyprobe" -scenario restart -snapshot-cache -eds-cache -resource-fetch-timeout -1s -out "$ARTIFACT_DIR/restart-cache-kgateway-bootstrap"
"$ARTIFACT_DIR/envoyprobe" -scenario timeouts -out "$ARTIFACT_DIR/timeouts"
"$ARTIFACT_DIR/envoyprobe" -scenario init -out "$ARTIFACT_DIR/init"
rm "$ARTIFACT_DIR/envoyprobe"
echo "Direct Envoy receipts: $ARTIFACT_DIR"
