#!/usr/bin/env sh
# Replay the go-control-plane characterization probes against a different
# root-module revision without touching this repository's go.mod.
#
#   devel/formal/gcpprobe/replay-profile.sh <go-control-plane revision> [out dir]
#
# The probes import only go-control-plane, gRPC, and the standard library, so
# they are copied into a scratch module that pins the requested revision and the
# branch's Envoy proto and gRPC versions. A probe that fails here has observed a
# behavior different from the branch pin: explain the change and map it to a
# repaired expectation in research-findings.md; do not relax the assertion.
# Network access to the Go module proxy is required. This is an optional,
# explicitly separate profile run, not part of the required gate.
set -eu
if [ $# -lt 1 ]; then
    echo "usage: $0 <go-control-plane revision> [out dir]" >&2
    exit 2
fi
REVISION=$1
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)
OUT=${2:-$(mktemp -d "${TMPDIR:-/tmp}/gcpprobe-replay.XXXXXX")}
mkdir -p "$OUT"
envoy=$(awk '$1 == "github.com/envoyproxy/go-control-plane/envoy" { print $2 }' "$ROOT_DIR/go.mod")
grpc=$(awk '$1 == "google.golang.org/grpc" { print $2 }' "$ROOT_DIR/go.mod")
test -n "$envoy" && test -n "$grpc"
cp "$SCRIPT_DIR"/*_test.go "$OUT/"
cd "$OUT"
go mod init gcpprobe.replay >/dev/null 2>&1
go get "github.com/envoyproxy/go-control-plane@$REVISION" \
    "github.com/envoyproxy/go-control-plane/envoy@$envoy" \
    "google.golang.org/grpc@$grpc" >"$OUT/go-get.log" 2>&1
go mod tidy >>"$OUT/go-get.log" 2>&1
resolved=$(awk '$1 == "github.com/envoyproxy/go-control-plane" { print $2 }' go.mod)
echo "replaying probes against go-control-plane $resolved (envoy $envoy, grpc $grpc)"
result=0
CGO_ENABLED=0 go test -count=1 -v . >"$OUT/go-test.log" 2>&1 || result=$?
grep -E '^--- (PASS|FAIL)' "$OUT/go-test.log"
echo "receipts: $OUT (exit $result)"
exit "$result"
