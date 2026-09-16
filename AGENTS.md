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
5. **Never move an existing release tag.** A tag pointing at the wrong commit is
   `TAG_CONFLICT` → stop and ask a human.
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
    issue *before* it does. Never delete its branch, never rewrite its history,
    and never merge on a human's behalf; `keep-open` is the only escape hatch and
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
`FLEET_CREDENTIAL_REQUIRED`; it never falls back to a repository token. See
`docs/USAGE-MODEL.md` for the full model and `releasegraph doctor` for a
readiness report.

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
