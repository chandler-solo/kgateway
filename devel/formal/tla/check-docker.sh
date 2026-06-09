#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if ! command -v docker >/dev/null 2>&1; then
    echo "Docker is required to run TLC in a container, but docker was not found on PATH." >&2
    exit 1
fi

JAVA_IMAGE=${TLC_JAVA_IMAGE:-eclipse-temurin:21-jre}
CURL_IMAGE=${TLC_CURL_IMAGE:-curlimages/curl:8.9.1}
TLA2TOOLS_URL=${TLA2TOOLS_URL:-https://github.com/tlaplus/tlaplus/releases/latest/download/tla2tools.jar}
CACHE_DIR=${TLA2TOOLS_CACHE_DIR:-/tmp/kgateway-tla2tools}
TLC_METADIR=${TLC_METADIR:-/tmp/tlc-states}

run_tlc() {
    jar_path=$1
    config=$2
    model=$3
    model_metadir=$TLC_METADIR/${model%.tla}

    if [ -n "${TLC_WORKERS:-}" ]; then
        docker run --rm \
            -v "$SCRIPT_DIR:/model:ro" \
            -v "$jar_path:/tla2tools.jar:ro" \
            -w /model \
            "$JAVA_IMAGE" \
            java -XX:+UseParallelGC -jar /tla2tools.jar -metadir "$model_metadir" -workers "$TLC_WORKERS" -config "$config" "$model"
    else
        docker run --rm \
            -v "$SCRIPT_DIR:/model:ro" \
            -v "$jar_path:/tla2tools.jar:ro" \
            -w /model \
            "$JAVA_IMAGE" \
            java -XX:+UseParallelGC -jar /tla2tools.jar -metadir "$model_metadir" -config "$config" "$model"
    fi
}

run_all_models() {
    jar_path=$1
    run_tlc "$jar_path" XdsAdsSotw.cfg XdsAdsSotw.tla
    run_tlc "$jar_path" XdsReconnectRace13868.cfg XdsReconnectRace13868.tla
    run_tlc "$jar_path" XdsPerClientPublication.cfg XdsPerClientPublication.tla
    run_tlc "$jar_path" XdsEnvoyWarming.cfg XdsEnvoyWarming.tla
    run_tlc "$jar_path" XdsNamedEdsWatch.cfg XdsNamedEdsWatch.tla
    run_tlc "$jar_path" XdsEdsSubset.cfg XdsEdsSubset.tla
}

if [ -n "${TLA2TOOLS_JAR:-}" ]; then
    JAR_DIR=$(CDPATH= cd -- "$(dirname -- "$TLA2TOOLS_JAR")" && pwd)
    JAR_PATH=$JAR_DIR/$(basename -- "$TLA2TOOLS_JAR")
    if [ ! -f "$JAR_PATH" ]; then
        echo "TLA2TOOLS_JAR points to a missing file: $TLA2TOOLS_JAR" >&2
        exit 1
    fi
    run_all_models "$JAR_PATH"
    exit 0
fi

mkdir -p "$CACHE_DIR"
JAR_PATH=$CACHE_DIR/tla2tools.jar

if [ ! -f "$JAR_PATH" ]; then
    echo "Downloading tla2tools.jar to $JAR_PATH using $CURL_IMAGE..."
    docker run --rm \
        -v "$CACHE_DIR:/work" \
        "$CURL_IMAGE" \
        -fsSL "$TLA2TOOLS_URL" -o /work/tla2tools.jar
fi

run_all_models "$JAR_PATH"
