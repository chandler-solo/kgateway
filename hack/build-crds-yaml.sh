#!/usr/bin/env bash

# This script generates a single YAML file containing all kgateway CRDs for
# easy installation without Helm.
#
# Helm has a 1MiB size limitation on its HELM_DRIVER Secret that makes it
# suboptimal for CRDs even if they are in their own chart in the templates/
# directory.

set -o errexit
set -o nounset
set -o pipefail

readonly SCRIPT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")"/.. && pwd)"
readonly CRD_DIR="${SCRIPT_ROOT}/install/helm/kgateway-crds/templates"
readonly OUTPUT_DIR="${SCRIPT_ROOT}/_output/release"
readonly OUTPUT_FILE="${OUTPUT_DIR}/kgateway-crds.yaml"
readonly YEAR=$(date +"%Y")

mkdir -p "${OUTPUT_DIR}"

# Start with the boilerplate
cat "${SCRIPT_ROOT}/hack/boilerplate/boilerplate.yaml.txt" > "${OUTPUT_FILE}"

# Replace YEAR placeholder with actual year
if [[ "$OSTYPE" == "linux-gnu"* ]]; then
    sed -i "s/YEAR/${YEAR}/g" "${OUTPUT_FILE}"
elif [[ "$OSTYPE" == "darwin"* ]]; then
    sed -i '' "s/YEAR/${YEAR}/g" "${OUTPUT_FILE}"
else
    echo "Unsupported OS: $OSTYPE"
    exit 1
fi

# Add header comment
cat << EOF >> "${OUTPUT_FILE}"
#
# kgateway CRDs install
#
# This file contains all CustomResourceDefinitions for kgateway.
# To install: kubectl apply --server-side -f kgateway-crds.yaml
#
EOF

# Append each CRD file
for file in "${CRD_DIR}"/*.yaml; do
    # Skip non-CRD files like NOTES.txt (though it's .txt so won't match)
    if [[ ! -f "$file" ]]; then
        continue
    fi

    echo "---" >> "${OUTPUT_FILE}"
    echo "#" >> "${OUTPUT_FILE}"
    echo "# Source: ${file#${SCRIPT_ROOT}/}" >> "${OUTPUT_FILE}"
    echo "#" >> "${OUTPUT_FILE}"
    cat "$file" >> "${OUTPUT_FILE}"
done

echo "Generated: ${OUTPUT_FILE}"
