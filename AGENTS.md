# Agent guide — ReleaseGraph

ReleaseGraph is the release **transaction authority** for the whole fleet. Two
rules decide almost everything below: *where does this run?* and *what may it
touch?*

## Where do I run what?

| I want to… | Go to | Run |
|---|---|---|
| develop / release a business repo | that repository | nothing — `push`. The repo calls ReleaseGraph itself. |
| inspect release infrastructure | this repository | `releasegraph provider inspect --all --manifest fleet.yaml` |
| repair one repository's release | this repository | `releasegraph provider repair --repo owner/name --version X` (plan first, then `--apply`) |
| audit open pull requests | this repository | `releasegraph pr-lifecycle audit` |
| publish a new ReleaseGraph version | this repository, at production-base HEAD | update the reviewed policy version, then `python -m release_infra.cli prepare-tag --version X` (plan, then `--apply`). The tag workflow builds and publishes. |
| roll out a new ReleaseGraph version | this repository | `releasegraph rollout plan --version vX.Y.Z` |

You never need to run ReleaseGraph inside a business repository: a business
repository only carries `.release-policy.yml`, `.github/workflows/release.yml`
and its own build/test configuration.

## Non-negotiable rules

1. **Never mutate release state by hand if ReleaseGraph has a primitive.**
   No `gh release create`, `git tag -f`, `git push --force`, ad-hoc label edits or
   hand-written API mutations to "fix" a release. If a primitive is missing, add
   the primitive to ReleaseGraph, test it, dry-run it, then use it.
2. **Never work in a shared checkout.** Every modifying session uses its own
   worktree: `scripts/agent-worktree create <task-id>`.
3. **Inspect before mutate.** `provider inspect` → `provider reconcile`/`repair`
   plan → then `--apply`.
4. **Dry-run before fleet mutation.** Fleet commands plan by default; `--apply`
   is an explicit act.
5. **Never move an existing release tag.** On `TAG_CONFLICT`, stop writes to that
   release and verify both the peeled tag commit and the evidence for the
   expected commit. A manifest-alignment PR is not automatically a release PR.
   If attribution is wrong, fix and test ReleaseGraph, then inspect again without
   changing the tag. Ask a human only when a verified conflict requires a
   historical acceptance decision; continue independent work meanwhile.
6. **Never fabricate historical GitHub Releases** to satisfy a version provider.
   Use an explicit waiver (`releasegraph provider waive`) when a human decides a
   historical version is accepted as-is.
7. **Never infer managed repositories from owner discovery.** `fleet.yaml` is the
   authority; `fleet discover` only lists candidates.
8. **Repository scope touches only its bound repository.** Cross-repository work
   requires fleet scope and `RELEASEGRAPH_FLEET_TOKEN`.
9. **Do not put release logic in business repositories.** It belongs here.
10. **Provider state is derived, never authoritative.** Real GitHub/registry state
    decides health; release-please labels are acknowledgement only.
11. **Feature branches may stack. Branches that perform a production state
    transition must not.** A cutover/release/hotfix/ops branch must be created
    from the current configured production-base HEAD and must target that base
    directly. Never build one on an unmerged feature branch; when the base has
    advanced, recreate the branch from the latest base before merge.
    AGENTS.md is guidance — the reusable branch-contract workflow is the
    enforcement (see `docs/branch-contract.md`).
    Reusable governance workflows must never internally resolve their engine
    from a mutable channel such as v1/main; use the reusable workflow's own
    immutable workflow SHA (`job.workflow_sha`).
12. **An open pull request is a merge queue, not a backlog.** Four objects have
    four jobs and must never stand in for one another:
    **Issue = backlog, branch = workspace, pull request = merge queue, release
    pull request = publish queue.** A pull request that is no longer a merge
    candidate leaves the queue, and its engineering context is archived into an
    issue *before* it does. Never delete its branch or rewrite its history.
    Lifecycle automation never merges. An interactive agent may merge PRs within
    the user's explicitly requested or delegated task after inspecting the diff,
    verifying applicable CI, and checking the current head and branch contract.
    Existing authorization persists; do not ask again for each routine merge.
    General permission to continue does not authorize unrelated releases or
    production deployments. `keep-open` is the only lifecycle escape hatch and
    it is permanent.
    AGENTS.md is guidance — the scheduled
    `.github/workflows/pr-lifecycle.yml` is the enforcement (see
    `docs/pr-lifecycle.md`).

## Scope and credentials

```
repository scope  bound to GITHUB_REPOSITORY, uses GITHUB_TOKEN
fleet scope       control plane only, uses RELEASEGRAPH_FLEET_TOKEN
```

A fleet operation without `RELEASEGRAPH_FLEET_TOKEN` fails immediately with
`FLEET_CREDENTIAL_REQUIRED`; it never falls back to a repository token.
That error describes the current process, not necessarily the control plane:

1. Check secret/variable names and workflow configuration without printing values.
2. If the control repository already has the fleet secret or GitHub App, use its
   existing workflows: `provider-watchdog.yml` for fleet inspection,
   `provider-operations.yml` for a repository inspect/plan, and `fleet-rollout.yml`
   for rollout planning. Dispatch with `apply=false` first and wait for results.
3. Keep installation tokens inside the runner. Do not extract Actions secrets,
   persist tokens in files, or relabel the ordinary `gh` login as a fleet token.
4. Report credential readiness separately from release health: a successful
   workflow can still report conflicts or missing releases. Apply only the
   reviewed primitive within the user's authorized scope.

See `docs/authentication.md`, `docs/historical-provider-operations.md`, and
`releasegraph doctor` for configuration and readiness.

## Continue until the authorized task is handled

Inspect current state instead of repeating historical work. Choose routine
implementation details, repair attributable engine bugs, run the required
checks, and open reviewable PRs without another approval round. When the user
delegates the next step, carry those task-scoped fixes through CI and merge.
Ask only for missing information or a decision with materially different
outcomes that existing authorization does not cover.

Do not use a historical waiver to conceal an engine diagnostic bug. Do not
invent a release to satisfy a stale version fallback. Preserve existing tags,
published artifacts, branches, and history. Report source fixes, merges,
published versions, and deployed versions as separate outcomes.

## Before you push

```bash
go vet ./... && go test ./...
python3 -m unittest discover -s tests -q
git diff --exit-code -- dist/release/RELEASE-METADATA.json
```

Known technical debt: the full Python suite rewrites the tracked
`dist/release/RELEASE-METADATA.json` (test timestamps). If the last check
fails, restore the file (`git checkout -- dist/release/RELEASE-METADATA.json`)
instead of committing the dirty artifact; never use `git add -A` blindly.

Commits are reviewable vertical slices, one concern each.
