#!/usr/bin/env bash

set -euo pipefail

# Verifies that the kgateway controller never published a per-client xDS
# snapshot that failed go-control-plane's Snapshot.Consistent() invariant.
#
# Intended to run AFTER a conformance (or any cluster-based) test run against
# an install that has KGW_XDS_SNAPSHOT_CONSISTENCY_CHECK=true set on the
# controller. The publication paths maintain the invariant by construction, so
# any nonzero kgateway_xds_snapshot_perclient_inconsistent_snapshots_total
# sample is a kgateway bug.
#
# Environment variables:
#   INSTALL_NAMESPACE - namespace of the kgateway install (default: kgateway-system)
#   KUBE_CONTEXT      - optional kubectl context
#
# Exit codes:
#   0 - no inconsistent snapshots recorded (or check not enabled: warns and passes,
#       so this script is safe to chain after installs that predate the setting)
#   1 - at least one inconsistent snapshot was recorded
#   2 - could not reach the controller metrics endpoint

INSTALL_NAMESPACE="${INSTALL_NAMESPACE:-kgateway-system}"
KUBECTL=(kubectl)
if [[ -n "${KUBE_CONTEXT:-}" ]]; then
    KUBECTL+=(--context "${KUBE_CONTEXT}")
fi

METRIC="kgateway_xds_snapshot_perclient_inconsistent_snapshots_total"

check_enabled() {
    "${KUBECTL[@]}" -n "${INSTALL_NAMESPACE}" get deploy kgateway \
        -o jsonpath='{.spec.template.spec.containers[*].env[?(@.name=="KGW_XDS_SNAPSHOT_CONSISTENCY_CHECK")].value}' 2>/dev/null || true
}

if [[ "$(check_enabled)" != "true" ]]; then
    echo "WARNING: KGW_XDS_SNAPSHOT_CONSISTENCY_CHECK is not enabled on deploy/kgateway in ${INSTALL_NAMESPACE};" >&2
    echo "         the consistency counter was never recorded, so there is nothing to verify. Passing." >&2
    exit 0
fi

# Scrape the controller metrics endpoint via a short-lived port-forward.
PORT=19092
"${KUBECTL[@]}" -n "${INSTALL_NAMESPACE}" port-forward deploy/kgateway "${PORT}:9092" >/dev/null 2>&1 &
PF_PID=$!
trap 'kill "${PF_PID}" 2>/dev/null || true' EXIT

METRICS=""
for _ in $(seq 1 20); do
    if METRICS=$(curl -sf "localhost:${PORT}/metrics" 2>/dev/null); then
        break
    fi
    sleep 0.5
done

if [[ -z "${METRICS}" ]]; then
    echo "ERROR: could not scrape controller metrics from deploy/kgateway in ${INSTALL_NAMESPACE}" >&2
    exit 2
fi

# The counter only materializes on the first violation; absence passes.
BAD=$(echo "${METRICS}" | awk -v m="${METRIC}" '$0 ~ "^"m && $NF != "0" {print}')
if [[ -n "${BAD}" ]]; then
    echo "ERROR: the controller published per-client xDS snapshots that failed Snapshot.Consistent()." >&2
    echo "       This is a kgateway bug; please report it with the controller's error logs." >&2
    echo "${BAD}" >&2
    exit 1
fi

echo "OK: no inconsistent per-client xDS snapshots were published."
