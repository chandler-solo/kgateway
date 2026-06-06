#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)

cd "$ROOT_DIR"

echo "Running xdscheck Go tests..."
go test ./pkg/kgateway/translator/xdscheck

echo "Running xdscheck translator integration test..."
go test ./pkg/kgateway/translator/gateway -run '^(TestTranslatedRedirectSnapshotPassesXDSCheck|TestTranslatedBackendSnapshotPassesXDSCheck)$'

if command -v java >/dev/null 2>&1; then
    if [ -n "${TLA2TOOLS_JAR:-}" ] || [ -f "$SCRIPT_DIR/tla/tla2tools.jar" ] || [ -f "$ROOT_DIR/tools/tla2tools.jar" ]; then
        echo "Running TLA+ TLC check..."
        "$SCRIPT_DIR/tla/check.sh"
    else
        echo "Skipping TLA+ TLC check: tla2tools.jar was not found."
        echo "Download it from https://github.com/tlaplus/tlaplus/releases and set TLA2TOOLS_JAR=/path/to/tla2tools.jar."
    fi
else
    echo "Skipping TLA+ TLC check: Java was not found on PATH."
fi
