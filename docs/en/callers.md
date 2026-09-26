# Calling the release workflow

A managed repository carries one caller at `.github/workflows/release.yml` and
nothing else release-related. All release logic lives here, so a business
repository never implements versioning, asset gating, or publication itself.

```yaml
jobs:
  release:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1
    permissions: {contents: write, packages: write, pull-requests: write, id-token: write}
    secrets: inherit
```

Pin the tag. `@v1` is the published major; a commit SHA pins exactly.

## Outputs

| Output | Values | Use it for |
|---|---|---|
| `release_health` | `healthy` \| `tag-drift` \| `repair` \| `missing` | **The field to branch on.** Anything other than `healthy` means the released state does not satisfy the policy. |
| `run_release` | `1` \| `0` | Whether this run may release at all, or is a no-op. |
| `version` | e.g. `1.2.3` | What the run planned for. |
| `tag` | e.g. `v1.2.3` | The tag it planned for. |
| `tag_drift` | `true` \| `false` | The existing tag is not the current head. |
| `paths_released` | Release Please component paths | Monorepo components released in this run. |

```yaml
- id: release
  uses: ...
- if: steps.release.outputs.release_health != 'healthy'
  run: echo "::warning::release state is ${{ steps.release.outputs.release_health }}"
```

These are additive. The four `release_health` strings are a **frozen
production contract**: the workflow branches on `!= 'healthy'` in eight places
and 14 repositories call it, so a value may be added but never renamed or
removed. See [health](health.md) for why the vocabulary is closed, and for how
health differs from the action a caller should take.

Every output is forwarded from the `build-plan` job. A workflow output that
names a job output which does not exist resolves to the **empty string** for the
caller, with no error anywhere — so
`test_every_forwarded_output_exists_on_its_job` checks the references
structurally rather than trusting them.

## Inputs

| Input | Default | Meaning |
|---|---|---|
| `version` | `""` | Override the version to plan for. |
| `dry_run` | `false` | Plan and build, publish nothing. |
| `force` | `false` | Re-run diagnostics for an already-healthy version. |
| `repair` | `false` | Repair an incomplete release of the **same** version. |
| `stage` | `all` | Which stages to run. |

## Secrets

`NPM_TOKEN`, `ANDROID_KEYSTORE_B64` and `ANDROID_KEYSTORE_PROPERTIES` are all
optional: a missing secret disables the corresponding publication rather than
failing the run.

### `RELEASE_PLEASE_TOKEN`: who authors the version PR

This secret does not change what is published; it decides the **author** of the
release pull request. Leaving it out does not fail the run, but it leaves behind
a permanent false failure:

- without it, release-please opens the PR with the built-in `GITHUB_TOKEN`, so
  the author is `github-actions[bot]`;
- GitHub **holds** every run such a PR triggers (`action_required`, waiting for a
  human approval). That decision happens before any job exists, so no job-level
  `if:` can prevent it;
- the version PR therefore carries a check that can never turn green, and when
  the PR is merged that run is finalised as `failure` — one failure notification
  per release, for a CI that never actually ran.

A fine-grained personal access token is enough, with exactly three permissions:
**Contents: Read and write** (branches and commits), **Pull requests: Read and
write** (create and update the release PR) and **Issues: Read and write**
(labels). Store it as an Actions secret in every caller repository, named
exactly `RELEASE_PLEASE_TOKEN`.

Passing it in: callers that use `secrets: inherit` need no change; **a caller
that lists `secrets:` explicitly must add the line itself**
(`RELEASE_PLEASE_TOKEN: ${{ secrets.RELEASE_PLEASE_TOKEN }}`), otherwise the
secret never reaches this reusable workflow.

A pull request's author is immutable, so the effect starts with the **next newly
created** release PR; bot PRs that are already open keep the old path. If a PAT
is not available, the caller-side fallback is "approve and skip": a workflow
triggered by `workflow_run` / `push` / `schedule` approves the held runs while
every job that can run on a pull request carries an author guard
(`github.event_name != 'pull_request' || github.event.pull_request.user.login != 'github-actions[bot]'`),
so an approved run resolves as `skipped` instead of a false failure.

## Caller-side registry publication and ACK ordering

The provider acknowledgement inside `finalize` is the last step of the release
transaction. When a required registry is published by the caller's own
independent job (for example PyPI trusted publishing), that ACK runs before the
registry job and the health gate correctly refuses with
`REGISTRY_INCOMPLETE` -- the gate is working, not the pipeline. Such
repositories append a thin caller job after the registry job that reuses
`reusable-acknowledge.yml`:

```yaml
acknowledge:
  needs: [release, publish-pypi]
  if: >-
    always() &&
    needs.release.result == 'success' &&
    needs.release.outputs.run_release == '1' &&
    (needs.publish-pypi.result == 'success' || needs.publish-pypi.result == 'skipped') &&
    github.event_name != 'pull_request' &&
    !(github.event_name == 'workflow_dispatch' && github.event.inputs.dry_run == 'true')
  uses: redtidev1918/releasegraph/.github/workflows/reusable-acknowledge.yml@<pinned-ref>
  with:
    version: ${{ needs.release.outputs.version }}
  secrets: inherit
```

The workflow builds its engine from `job.workflow_sha`, so it executes exactly
the caller's pinned commit, re-runs the same idempotent
`provider reconcile --apply`, and retries for a bounded time while the registry
index propagates. It only succeeds on an actual ACK record, or when
`ProviderState == "TAGGED"` and `health == "HEALTHY"`. `provider reconcile`
itself exits successfully even when the ACK is refused, so a caller job must
never trust the exit code alone. Repositories without an independent registry
job do not need it.

After a repository rename, the OIDC publisher, fleet manifest, and snapshot
rules live in [Registry publishing and repository renames](en/registry-publishers.md).

## The Release body

The text on the Release page is generated by ReleaseGraph; a caller supplies
nothing for it. `feat`, `fix` and breaking changes are classified and rewritten
as user phrasing; `chore`, `ci`, `governance`, dependency-bot noise and version
bumps never reach the page. A special release can override a single sentence with
a `release-note:` trailer, or replace the whole body with
`.github/release-notes/<version>.md`. The language follows the repository's
primary README. See [Release notes](release-notes.md).

## What a caller must not do

A business repository does not run `gh release create`, `git tag -f`,
`git push --force`, or any hand-written API mutation to fix its own release.
Those bypass every exactly-once invariant the platform enforces. If a primitive
is missing, it belongs here as a new primitive with its own tests and dry run.
