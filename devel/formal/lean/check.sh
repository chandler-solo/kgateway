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
git rev-parse HEAD > "$ARTIFACT_DIR/source-head.txt"
git status --short > "$ARTIFACT_DIR/source-status.txt"
git diff HEAD > "$ARTIFACT_DIR/source-working.diff"
python3 - "$ARTIFACT_DIR/source-untracked-sha256.json" <<'PY'
import hashlib
import json
from pathlib import Path
import subprocess
import sys

# HEAD plus the tracked diff does not identify newly added research files.
paths = subprocess.check_output([
    "git", "ls-files", "--others", "--exclude-standard", "-z", "--",
    "devel/formal", "devel/testing", "pkg/kgateway/proxy_syncer",
    "test/e2e/features/xds_warming", ".github/workflows", "Makefile",
]).decode().split("\0")
digests = {name: hashlib.sha256(Path(name).read_bytes()).hexdigest()
           for name in paths if name and Path(name).is_file()}
Path(sys.argv[1]).write_text(json.dumps(digests, indent=2, sort_keys=True) + "\n")
PY
(go version; cd "$SCRIPT_DIR"; lake env lean --version) > "$ARTIFACT_DIR/tool-versions.txt"
shasum -a 256 go.mod go.sum > "$ARTIFACT_DIR/dependency-digests.txt"
# Full unit suites include digest and dependency probes omitted by the old
# TestSnapshotPerClient-only gate. JSON receipts distinguish skipped tests.
FORMAL_LIFECYCLE_OUT="$ARTIFACT_DIR/lifecycle" go test -tags e2e -count=1 -json ./pkg/kgateway/proxy_syncer ./devel/testing ./pkg/kgateway/translator/xdscheck ./devel/formal/gcpprobe ./devel/formal/lifecycle ./devel/formal/cmd/checkreceipts ./devel/formal/cmd/corpusexport ./pkg/sds/server ./pkg/krtcollections ./pkg/kgateway/endpoints > "$ARTIFACT_DIR/go-tests.jsonl"
go run -tags e2e ./devel/formal/cmd/checkreceipts "$ROOT_DIR" "$ARTIFACT_DIR/go-tests.jsonl"
go run -tags e2e ./devel/formal/cmd/checklifecycle "$ARTIFACT_DIR/lifecycle/"*.jsonl
test "$(find "$ARTIFACT_DIR/lifecycle" -name '*.jsonl' | wc -l | tr -d ' ')" -eq 4
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
cd "$SCRIPT_DIR"
# Minimized observations from the October 16 combined run. Preserve the
# legitimate overlap and the intentionally colliding fixture contributions.
lake exe xdsspec trace "$ROOT_DIR/devel/formal/regressions/fixtures/research-overlapping-decisions.jsonl"
collision_status=0
lake exe xdsspec trace "$ROOT_DIR/devel/formal/regressions/fixtures/research-toy-xor-collision.jsonl" > "$ARTIFACT_DIR/toy-xor-collision.log" 2>&1 || collision_status=$?
if [ "$collision_status" -ne 1 ] || ! grep -q '\[version-reuse\]' "$ARTIFACT_DIR/toy-xor-collision.log"; then
    echo "Expected toy XOR collision was not detected" >&2
    cat "$ARTIFACT_DIR/toy-xor-collision.log" >&2
    exit 1
fi
echo "PASS expected version-reuse counterexample (toy row contributions)"
echo "Required Lean, Go, and snapshot trace checks passed. Receipts: $ARTIFACT_DIR"
