# EP-13849: Adopt Renovate for LTS Branches

- Issue: [#13849](https://github.com/kgateway-dev/kgateway/issues/13849)

## Background

kgateway maintains dependency automation across `main` and active `vX.Y.x` release branches. Today that is handled with repeated Dependabot configuration blocks per branch and per ecosystem, which creates duplication and drift risk every time a new release branch is added or an old one is retired.

This EP proposes adopting Renovate for `main` and all active `vX.Y.x` release branches. The initial rollout uses Mend Renovate Community Cloud, keeps configuration in a single `.github/renovate.json` file on the default branch, gates update PRs behind the Dependency Dashboard, explicitly disables Renovate vulnerability alert PRs, and limits scope to the ecosystems already managed today: Go modules, Dockerfiles, and GitHub Actions.

The current kgateway release process creates release branches from `main` using the `vX.Y.x` naming convention. In the current maintainer workflow that means `main`, `v2.2.x`, and `v2.1.x` are active today, and future minor releases will add more branches that follow the same pattern. The repository structure also matches the expected dependency surfaces for this rollout, including `.github`, root `go.mod`, root `go.sum`, and multiple Dockerfile locations.

## Motivation

kgateway needs dependency automation that follows multiple active release streams without requiring branch-by-branch configuration duplication, avoids surprise PR floods across LTS branches, remains practical for a public CNCF repository, and stays aligned with the repository's existing dependency scope.

Native Dependabot is workable, but it scales poorly for this branch model because every new release line requires more repeated YAML. Renovate is a better fit because it can target multiple base branches from one config that lives on the default branch.

## Goals

- Replace repeated branch-specific Dependabot configuration with a single Renovate config.
- Cover `main` and all active `vX.Y.x` release branches.
- Keep the initial rollout conservative: no automatic PR flood, no automerge, and no surprise security PRs from Renovate.
- Preserve familiar labels: `dependencies` and `area/ci`.
- Keep scope limited to the ecosystems already being managed today: Go modules, Dockerfiles, and GitHub Actions.

## Non-Goals

- Full automerge at launch.
- Expanding update coverage to every supported manager in the repository.
- Changing the repository's release branch naming convention.
- Using separate Renovate config files per release branch.
- Replacing GitHub's own security alert UI.
- Self-hosting Renovate from day one.

## Implementation Details

### Hosted model and repository scope

The initial deployment should use Mend Renovate Community Cloud with a single `.github/renovate.json` file committed on the default branch. The config should:

- target the default branch and all `vX.Y.x` release branches,
- limit managers to `gomod`, `dockerfile`, and `github-actions`,
- require Dependency Dashboard approval before update PRs are created,
- disable Renovate vulnerability alert PRs during rollout,
- apply conservative PR and branch concurrency limits,
- group non-major updates by manager to keep reviews readable.

This keeps the migration operationally simple while matching the repository's current dependency-management scope.

### Proposed repository config

Create `.github/renovate.json` with:

```jsonc
{
  "$schema": "https://docs.renovatebot.com/renovate-schema.json",

  "extends": ["config:recommended"],

  // Process the default branch plus all active release lines that follow kgateway's naming convention.
  "baseBranchPatterns": ["$default", "/^v\\d+\\.\\d+\\.x$/"],

  // Match the ecosystems currently managed in the repo.
  "enabledManagers": ["gomod", "dockerfile", "github-actions"],

  // Override config:recommended so Dockerfiles under test/ remain eligible.
  "ignorePaths": [
    "**/node_modules/**",
    "**/bower_components/**",
    "**/vendor/**",
    "**/examples/**",
    "**/fixtures/**"
  ],

  "dependencyDashboardTitle": "Renovate Dashboard",
  "dependencyDashboardApproval": true,

  // Keep rollout consistent with a no-surprise-PR policy.
  "vulnerabilityAlerts": {
    "enabled": false
  },

  "labels": ["dependencies", "area/ci"],

  // Conservative rollout limits.
  "prConcurrentLimit": 3,
  "branchConcurrentLimit": 3,

  "packageRules": [
    {
      "matchManagers": ["gomod"],
      "matchUpdateTypes": ["minor", "patch"],
      "groupName": "gomod non-major updates"
    },
    {
      "matchManagers": ["dockerfile"],
      "matchUpdateTypes": ["minor", "patch", "digest"],
      "groupName": "docker non-major updates"
    },
    {
      "matchManagers": ["github-actions"],
      "matchUpdateTypes": ["minor", "patch", "digest"],
      "groupName": "github-actions non-major updates"
    }
  ]
}
```

### Configuration rationale

#### Branch selection

Use `"$default"` instead of hard-coding `main` so the config remains resilient if the default branch name changes later. The regex `/^v\\d+\\.\\d+\\.x$/` matches the release branch naming used by kgateway and allows newly created release branches to be picked up without editing the config.

#### Manager scope

`enabledManagers` should be limited to `gomod`, `dockerfile`, and `github-actions` for the initial rollout so Renovate behavior stays aligned with the current Dependabot coverage.

#### Preserve Dockerfiles under `test/`

This is the most important repo-specific adjustment. The `config:recommended` preset is a good default, but it ignores several directories by default, including test directories. kgateway currently relies on Dockerfiles under `test/...`, so the initial config must override `ignorePaths` to keep `test/` and `tests/` in scope while still ignoring `vendor`, `examples`, and `fixtures`.

#### Approval gating and security PR behavior

`dependencyDashboardApproval: true` is the right migration posture because it keeps maintainers in control while Renovate is introduced across multiple active branches. `vulnerabilityAlerts.enabled: false` keeps the rollout consistent with the no-surprise-PR objective because Renovate security PRs can bypass normal schedule and concurrency controls.

#### Concurrency and grouping

The initial limits should be low to avoid flooding maintainers with work. Starting with `prConcurrentLimit: 3` and `branchConcurrentLimit: 3` is conservative without preventing evaluation. Grouping non-major updates by manager keeps reviews readable by avoiding mixed PRs that span Go modules, Dockerfiles, and GitHub Actions in a single change.

### Operational behavior

#### When a new release branch is created

When kgateway creates a new branch like `v2.3.x`, no Renovate config change is required as long as the branch follows the existing naming convention. The operational check after branch creation is:

1. confirm the branch exists in GitHub,
2. wait for the next Mend job or trigger one manually,
3. confirm the Dependency Dashboard shows updates for the new branch.

#### When an old release branch is dropped

This proposal assumes dropped release branches are deleted. If a retired branch remains in the repository for archival reasons, the regex will still match it. In that case there are two acceptable responses:

1. delete the branch once unsupported, or
2. temporarily switch `baseBranchPatterns` from the regex to an explicit branch list until the stale branch is removed.

#### When config changes are needed later

If configuration changes are needed after rollout, the repository can use Renovate's documented `renovate/reconfigure` workflow so maintainers can preview the effect of the new config in a PR before merging it.

### Rollout plan

#### Phase 0: prep

Owner: repository maintainers

1. Install the Mend Renovate GitHub app for `kgateway-dev/kgateway` only.
2. Decide whether unsupported release branches are deleted on retirement.
3. Confirm the repository labels `dependencies` and `area/ci` exist.
4. Decide whether GitHub and Dependabot alerts remain enabled on the default branch outside of Renovate.

Exit criteria:

- the app is installed on the repository,
- maintainers can access logs in the Mend developer portal,
- label names are confirmed.

#### Phase 1: onboarding

Owner: repository maintainers

1. Add `.github/renovate.json` using the config above.
2. Merge the onboarding and config PR on the default branch.
3. Do not remove Dependabot immediately on the same day; let Renovate produce its first dashboard and initial update set.

Exit criteria:

- Renovate is enabled,
- the Dependency Dashboard exists,
- no unexpected PR flood occurs.

#### Phase 2: validation

Owner: repository maintainers and release owners  
Duration: first 3 to 7 days after onboarding

Validation checklist:

- Renovate discovers updates on `main`, `v2.2.x`, and `v2.1.x`.
- the root `go.mod` is tracked on all active branches.
- GitHub Actions updates are visible.
- Dockerfile updates appear for current branch-specific directories, including paths under `test/`.
- labels are applied correctly.
- no Renovate vulnerability alert PRs appear automatically.
- PR counts stay within configured limits once approvals begin.

If dashboard noise is higher than expected:

- add more `ignorePaths`, or
- switch to a stricter `includePaths` model in a follow-up PR.

#### Phase 3: cutover

Owner: repository maintainers  
Duration: after one stable release cycle

1. Remove or simplify the old Dependabot update configuration.
2. Keep Renovate as the only dependency PR bot.
3. Decide whether to keep approval gating everywhere or relax it on `main` only.

Recommended end state after stabilization:

- `main`: optionally loosen approval for patch, minor, and digest updates,
- `vX.Y.x` release branches: continue to require dashboard approval.

A likely steady-state follow-up rule looks like this:

```jsonc
{
  "packageRules": [
    {
      "matchBaseBranches": ["main"],
      "matchUpdateTypes": ["minor", "patch", "digest"],
      "dependencyDashboardApproval": false
    },
    {
      "matchBaseBranches": ["/^v\\d+\\.\\d+\\.x$/"],
      "dependencyDashboardApproval": true
    }
  ]
}
```

#### Phase 4: optimization

Optional, after rollout is stable

Potential improvements:

- apply `minimumReleaseAge` selectively to reduce churn from brand-new releases,
- add targeted ignore rules if low-value Dockerfiles create noise,
- consider enabling Renovate vulnerability PRs later if the team wants them,
- consider `config:best-practices` only after the base rollout is stable, because it adds extra behaviors like Docker and GitHub Action digest pinning.

### Rollback plan

If rollout is noisy or disruptive:

1. disable or uninstall the Mend Renovate app for the repository,
2. close open Renovate PRs,
3. restore or keep the previous Dependabot update config,
4. revisit scope using either stricter `includePaths` or an explicit branch list.

Because the rollout uses no automerge and no automatic vulnerability PRs, rollback risk is low.

### Risks and mitigations

#### Risk: too many Docker update candidates

Because Renovate auto-discovers matching files for enabled managers, it may find Dockerfiles that Dependabot never covered.

Mitigation:

- approval-gated rollout,
- PR and branch limits,
- follow-up `ignorePaths` or `includePaths` tuning.

#### Risk: release-branch regex matches retired branches

Mitigation:

- delete retired branches,
- or switch temporarily to an explicit branch list.

#### Risk: `config:recommended` hides dependencies under `test/`

Mitigation:

- explicit `ignorePaths` override in the initial config.

#### Risk: security PRs bypass normal limits

Mitigation:

- explicitly set `"vulnerabilityAlerts": { "enabled": false }` for rollout.

#### Risk: hosted-job latency

Mend Renovate Community Cloud runs active repositories on a shared schedule. That is acceptable for one repository, but it is not real-time.

Mitigation:

- use manual job triggering during onboarding and release-week validation.

### Success criteria

The migration is successful if, after one release cycle:

- no per-branch config edits were needed when a new `vX.Y.x` branch was created,
- maintainers reviewed updates from one dashboard instead of maintaining repeated YAML,
- release branches received update coverage without PR flooding,
- at least one representative Go, Docker, and GitHub Actions update was processed successfully,
- maintainers are comfortable turning down or selectively relaxing approval gates on `main`.

### References

1. kgateway release guide: branch naming and release flow  
   <https://github.com/kgateway-dev/kgateway/blob/main/devel/contributing/releasing.md>
2. Renovate configuration locations and default-branch config behavior  
   <https://docs.renovatebot.com/configuration-options/>
3. Renovate `baseBranchPatterns`  
   <https://docs.renovatebot.com/configuration-options/#basebranchpatterns>
4. Mend Renovate Community Cloud overview, free tier, and scheduling  
   <https://docs.renovatebot.com/mend-hosted/overview/>
5. kgateway repository root structure  
   <https://github.com/kgateway-dev/kgateway>
6. Renovate `dependencyDashboardApproval`  
   <https://docs.renovatebot.com/configuration-options/#dependencydashboardapproval>
7. Renovate Dependency Dashboard workflow guidance  
   <https://docs.renovatebot.com/key-concepts/dashboard/>
8. Renovate `vulnerabilityAlerts` behavior  
   <https://docs.renovatebot.com/configuration-options/#vulnerabilityalerts>
9. Renovate `enabledManagers`  
   <https://docs.renovatebot.com/configuration-options/#enabledmanagers>
10. Renovate `ignorePaths` and the `config:recommended` test and example ignores  
    <https://docs.renovatebot.com/configuration-options/#ignorepaths>
11. Renovate `prConcurrentLimit` and `branchConcurrentLimit`  
    <https://docs.renovatebot.com/configuration-options/#prconcurrentlimit>
12. Renovate `groupName` and `packageRules.matchBaseBranches`  
    <https://docs.renovatebot.com/configuration-options/#groupname>  
    <https://docs.renovatebot.com/configuration-options/#packagerulesmatchbasebranches>
13. Renovate onboarding and reconfiguration workflow  
    <https://docs.renovatebot.com/getting-started/installing-onboarding/>
14. Renovate `config:best-practices` preset contents  
    <https://docs.renovatebot.com/presets-config/>

### Test Plan

Validation should focus on both onboarding behavior and steady-state branch coverage.

During rollout:

- confirm Renovate creates a Dependency Dashboard after the config is merged,
- confirm updates are discovered for `main`, `v2.2.x`, and `v2.1.x`,
- confirm at least one representative Go module, Dockerfile, and GitHub Actions update is surfaced,
- confirm Dockerfiles under `test/` remain eligible for updates,
- confirm labels are applied as expected,
- confirm Renovate does not create vulnerability alert PRs automatically,
- confirm PR and branch counts stay within the configured limits.

After one release cycle:

- create or observe a new `vX.Y.x` branch and confirm no config change is needed for Renovate to begin tracking it,
- verify maintainers can process updates through the Dependency Dashboard without a PR flood,
- verify the repository is ready to remove the old Dependabot configuration.

## Alternatives

### Option A: stay on Dependabot

Pros:

- no new bot to install,
- familiar workflow.

Cons:

- repetitive per-branch YAML,
- higher drift risk between release lines,
- no clean scaling story as new `vX.Y.x` branches are added.

### Option B: self-host Renovate from day one

Pros:

- full operational control,
- easier alignment with custom organization policies if needed later.

Cons:

- unnecessary operational burden for a first rollout,
- more moving pieces than needed for one public repository.

### Option C: Mend Renovate Community Cloud

Pros:

- free for all repositories,
- GitHub-supported,
- operationally simple,
- includes logs and manual job triggering.

Cons:

- shared-service scheduling and concurrency limits on the free tier.

The recommended starting point is Option C, with self-hosting revisited later only if real constraints appear.

## Open Questions

1. Should `main` eventually allow patch and minor PRs without dashboard approval while release branches remain gated?
2. Should unsupported release branches be deleted immediately when dropped, or kept for archival purposes?
3. Does the team want Renovate to own security fix PRs later, or should GitHub alerts remain alerts-only?
4. Is the current Docker scope intentionally narrow, or should Renovate eventually discover and manage all repository Dockerfiles?
