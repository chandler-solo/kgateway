---
name: Release Request
about: Track the steps to cut a kgateway patch or minor release
title: "Release Request: Cut a v<MAJOR>.<MINOR>.<PATCH> Release"
labels: ["kind/release"]
---

Tracking issue for cutting the `v<MAJOR>.<MINOR>.<PATCH>` release from the `v<MAJOR>.<MINOR>.x` release branch.

## Backports
- [ ] <PR link>
- [ ] others?

## Prerequisites
- [ ] Confirm push permissions to the kgateway repo
- [ ] Confirm the `v<MAJOR>.<MINOR>.x` release branch exists and contains all required backports
- [ ] Confirm the release branch is listed in `.github/workflows/osv-scanner.yaml`

## Publish the Release
- [ ] Open the [Release workflow](https://github.com/kgateway-dev/kgateway/actions/workflows/release.yaml)
- [ ] Run with branch `v<MAJOR>.<MINOR>.x` and version `v<MAJOR>.<MINOR>.<PATCH>`
- [ ] Enable "validate release"
- [ ] Watch the workflow to completion

## Generate Release Notes
- [ ] Run `./hack/generate-release-notes.sh -p v<prev> -c v<MAJOR>.<MINOR>.<PATCH>`
- [ ] Review `_output/RELEASE_NOTES.md`
- [ ] Paste into the GitHub release description

## Verify
- [ ] Confirm the tag and assets on the [releases page](https://github.com/kgateway-dev/kgateway/releases)
- [ ] Walk through the [quickstart guide](https://kgateway.dev/docs/quickstart/) using the new version

## Update Documentation (kgateway.dev)
- [ ] Bump `assets/docs/versions/n-patch.md`
- [ ] If applicable, bump `assets/docs/versions/k8s-gw-version.md`
- [ ] Update versioned folders/conrefs for previous versions if needed
- [ ] Open and merge a PR to `kgateway-dev/kgateway.dev`

## Downstreams
- [ ] (Optional for patch) Bump kgateway version in [llm-d-infra](https://github.com/llm-d-incubation/llm-d-infra) after testing the quickstart

## Close-out
- [ ] Announce the release
- [ ] Close this issue
