#!/usr/bin/env bash

# Exits 0 if a release already exists for the given version (as a GitHub release or a
# git tag on origin), 1 if it does not, or another nonzero status if the check could not be
# completed. The release workflow uses this both as a fail-fast check in setup and as the final
# guard in publish, so the two cannot diverge.
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
if git ls-remote --exit-code --tags origin "refs/tags/${version}" >/dev/null; then
    exit 0
else
    git_status=$?
fi

# `git ls-remote --exit-code` returns 2 when the ref does not exist. Any other failure means the
# duplicate-release guard could not establish that publishing is safe, so fail closed.
if [[ $git_status -eq 2 ]]; then
    exit 1
fi

echo "unable to determine whether tag '${version}' exists" >&2
exit "$git_status"
