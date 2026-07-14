#!/usr/bin/env bash

# Exits 0 if a release already exists for the given version (as a GitHub release or a
# git tag on origin), 1 if it does not. The release workflow uses this both as a fail-fast
# check in setup and as the final guard in publish, so the two cannot diverge.
#
# Requires GH_TOKEN in the environment for `gh release view`.
set -o errexit
set -o pipefail
set -o nounset

if [[ $# -ne 1 ]]; then
    echo "usage: $(basename "$0") <version>" >&2
    exit 2
fi

version="$1"

if gh release view "$version" >/dev/null 2>&1; then
    exit 0
fi
if git ls-remote --exit-code --tags origin "refs/tags/${version}" >/dev/null 2>&1; then
    exit 0
fi
exit 1
