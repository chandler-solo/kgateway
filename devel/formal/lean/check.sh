#!/usr/bin/env sh
# Build the Lean xDS spec (re-checking every proof), run the explicit-state
# model checker against the safe and bug configurations, and conformance-
# check a freshly recorded implementation trace against the spec.
#
# Requires elan (https://leanprover-community.github.io/get_started.html);
# the toolchain version is pinned by lean-toolchain in this directory.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)

if ! command -v lake >/dev/null 2>&1; then
    if [ -x "$HOME/.elan/bin/lake" ]; then
        PATH="$HOME/.elan/bin:$PATH"
        export PATH
    else
        echo "Skipping Lean formal checks: lake was not found on PATH."
        echo "Install elan: curl -sSfL https://elan.lean-lang.org/elan-init.sh | sh -s -- -y"
        exit 0
    fi
fi

cd "$SCRIPT_DIR"

echo "Building Lean spec and re-checking proofs..."
lake build

echo "Running the xdsspec model checker..."
lake exe xdsspec check

echo "Recording an implementation trace from the proxy_syncer tests..."
TRACE_FILE=$(mktemp -t xds-trace.XXXXXX)
trap 'rm -f "$TRACE_FILE"' EXIT
(
    cd "$ROOT_DIR"
    XDS_TRACE_OUT="$TRACE_FILE" go test -tags e2e -count=1 \
        -run 'TestSnapshotPerClient' ./pkg/kgateway/proxy_syncer/
)

echo "Conformance-checking the trace against the spec..."
lake exe xdsspec trace "$TRACE_FILE"
