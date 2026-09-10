#!/usr/bin/env sh
# Required verification gate: missing tools, skipped scenarios, lost trace
# events, or failed checks are failures. Artifacts remain available for review.
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)
if ! command -v lake >/dev/null 2>&1; then
    if [ -x "$HOME/.elan/bin/lake" ]; then
        PATH="$HOME/.elan/bin:$PATH"
        export PATH
    else
        echo "Required Lean checks cannot run: install elan/lake." >&2
        exit 1
    fi
fi
ARTIFACT_DIR=${FORMAL_ARTIFACT_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/xds-formal.XXXXXX")}
mkdir -p "$ARTIFACT_DIR"
ARTIFACT_DIR=$(CDPATH= cd -- "$ARTIFACT_DIR" && pwd)
echo "Formal artifacts: $ARTIFACT_DIR"
cd "$SCRIPT_DIR"
lake build
lake exe xdsspec check > "$ARTIFACT_DIR/model-check.log"
cat "$ARTIFACT_DIR/model-check.log"
cd "$ROOT_DIR"
# Full unit suites include digest and dependency probes omitted by the old
# TestSnapshotPerClient-only gate. JSON receipts distinguish skipped tests.
go test -tags e2e -count=1 -json ./pkg/kgateway/proxy_syncer ./devel/testing ./pkg/kgateway/translator/xdscheck ./devel/formal/gcpprobe > "$ARTIFACT_DIR/go-tests.jsonl"
go test -tags e2e -c -o "$ARTIFACT_DIR/proxy-tests" ./pkg/kgateway/proxy_syncer
cd "$ROOT_DIR/pkg/kgateway/proxy_syncer"
"$ARTIFACT_DIR/proxy-tests" -test.list '^TestSnapshotPerClient' > "$ARTIFACT_DIR/scenarios.txt"
if ! test -s "$ARTIFACT_DIR/scenarios.txt"; then
    echo "No trace scenarios discovered" >&2
    exit 1
fi
while IFS= read -r scenario; do
    case "$scenario" in TestSnapshotPerClient*) ;; *) echo "Unexpected scenario: $scenario" >&2; exit 1 ;; esac
    trace="$ARTIFACT_DIR/$scenario.jsonl"
    : > "$trace"
    XDS_TRACE_OUT="$trace" XDS_TRACE_SCENARIO="$scenario" \
        "$ARTIFACT_DIR/proxy-tests" -test.v -test.run "^$scenario$" > "$ARTIFACT_DIR/$scenario.log"
    if grep -q -- '--- SKIP:' "$ARTIFACT_DIR/$scenario.log"; then
        echo "Required trace scenario skipped: $scenario" >&2
        exit 1
    fi
    (cd "$SCRIPT_DIR" && lake exe xdsspec trace "$trace")
done < "$ARTIFACT_DIR/scenarios.txt"
rm "$ARTIFACT_DIR/proxy-tests"
echo "Required Lean, Go, and snapshot trace checks passed. Receipts: $ARTIFACT_DIR"
