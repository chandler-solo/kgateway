#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/../../.." && pwd)

if ! command -v java >/dev/null 2>&1; then
    echo "Java is required to run TLC, but java was not found on PATH." >&2
    echo "Install Java 11 or newer, then rerun $0." >&2
    exit 1
fi

if [ -n "${TLA2TOOLS_JAR:-}" ]; then
    JAR=$TLA2TOOLS_JAR
elif [ -f "$SCRIPT_DIR/tla2tools.jar" ]; then
    JAR=$SCRIPT_DIR/tla2tools.jar
elif [ -f "$ROOT_DIR/tools/tla2tools.jar" ]; then
    JAR=$ROOT_DIR/tools/tla2tools.jar
else
    echo "Could not find tla2tools.jar." >&2
    echo "Download it from https://github.com/tlaplus/tlaplus/releases and then either:" >&2
    echo "  - place it at devel/formal/tla/tla2tools.jar" >&2
    echo "  - place it at tools/tla2tools.jar" >&2
    echo "  - set TLA2TOOLS_JAR=/path/to/tla2tools.jar" >&2
    exit 1
fi

if [ ! -f "$JAR" ]; then
    echo "TLA2TOOLS_JAR points to a missing file: $JAR" >&2
    exit 1
fi

cd "$SCRIPT_DIR"
java -jar "$JAR" -config XdsAdsSotw.cfg XdsAdsSotw.tla
java -jar "$JAR" -config XdsReconnectRace13868.cfg XdsReconnectRace13868.tla
java -jar "$JAR" -config XdsPerClientPublication.cfg XdsPerClientPublication.tla
java -jar "$JAR" -config XdsPerClientConvergence.cfg XdsPerClientConvergence.tla
java -jar "$JAR" -config XdsEnvoyWarming.cfg XdsEnvoyWarming.tla
java -jar "$JAR" -config XdsNamedEdsWatch.cfg XdsNamedEdsWatch.tla
java -jar "$JAR" -config XdsEdsSubset.cfg XdsEdsSubset.tla
