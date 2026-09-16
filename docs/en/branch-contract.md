# Production-operation branch contract

The contract turns one team habit into a declarative, testable, auditable rule:

```text
Feature branches MAY stack.

A production-operation branch MUST target the configured production base
directly and MUST descend from the current HEAD of that base.

Production-operation branches MUST NOT depend on unmerged feature branches.

When the production base advances, an open production-operation branch MUST be
refreshed/recreated against the new production base before merge.
```

## Development vs production-operation branches

Stacked development is legal and stays untouched:

```text
main
  \
   feat/provider
        \
         feat/provider-tests
              \
               feat/provider-docs
```

A production-operation branch (matching `chore/cutover-*`, `ops/*`,
`release/*`, `hotfix/*`, or patterns a repository declares) is not a
development continuation. It expresses a **production state transition** — for
example `EXECUTION_PROVIDER=github → fly` — so its parent state must be the
current production base, never an unmerged feature state.

## The invariants

For a pull request whose head branch matches a production-operation pattern:

1. **Base target** — `PR base == configured production base`.
2. **Ancestry** — `git merge-base(head, base) == git rev-parse(base)`.
3. **Scope** (optional, repo policy) — changed files stay inside the
   operation's declared `allowedPaths`.

The decision is made on **git topology only** (`merge-base` / `rev-parse`).
Commit messages, PR titles, patch-id or content similarity are never consulted.

## Why squash merges break stacked cutovers

```text
A---S        main   (S = squash of feature B+C)
 \
  B---C      feat/provider
       \
        D    chore/cutover-...
```

`content(S) ≈ content(B+C)`, but the commit identity differs:
`merge-base(D, main) = A`, not `S`. The contract therefore rejects `D`, even
though the code looks identical. A production transition must be explicitly
based on a production commit — not coincidentally equal to one.

The same rule makes an open production PR turn red when main advances (a new
commit lands on main after the PR opened): the branch no longer descends from
the current production-base HEAD and must be refreshed before merge. This is
intentional.

## Policy schema

Declared in the repository `.release-policy.yml`:

```yaml
repository:
  git:
    productionOperations:
      base: default        # "default" resolves the repository default branch;
      branches:            # or an explicit ref: main, master, prod, ...
        - "chore/cutover-*"
        - "ops/*"
        - "release/*"
        - "hotfix/*"
      requireLatestBase: true   # must be true; there is no opt-out
      operations:          # optional change-scope declarations
        cutover:
          branches:
            - "chore/cutover-*"
          allowedPaths:    # glob patterns; "**" crosses directories
            - "fly/*.toml"
            - ".github/workflows/**"
```

`allowedPaths` is a repo-level optional capability; nothing is hardcoded in
ReleaseGraph core. Unknown fields and invalid globs are config errors.

## CI integration

ReleaseGraph ships a canonical reusable gate:
[`.github/workflows/reusable-branch-contract.yml`](../.github/workflows/reusable-branch-contract.yml).
Consumer repositories keep only a thin caller, pinned to an immutable ref:

```yaml
name: Branch contract
on: [pull_request]
permissions:
  contents: read
jobs:
  branch-contract:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-branch-contract.yml@0b0c28990abac59aa5ae1d2c50bf95ce06d26a8d # ReleaseGraph v1.4.11
```

Human-readable version tags may be used in comments, but production callers must pin the reusable workflow by a full 40-character commit SHA.

Behaviour:

- feature PRs are skipped (zero cost, no ancestry churn);
- a violating production-operation PR **hard fails**, with the diff proof
  (changed files, commit count, base/head/merge-base SHAs) attached;
- the gate never rebases, force-pushes, or mutates the production branch.

GitHub branch protection remains responsible for required checks, merge
permissions and review requirements; the branch contract is complementary and
decides ancestry, which rulesets cannot express.

### Immutable consumer pins

Pin the caller by full commit SHA. The gate builds its engine from
`job.workflow_sha` — the reusable workflow's own commit, never `github.sha` —
and asserts at runtime that the checked-out engine HEAD equals it (mismatch
fails closed). Workflow definition and engine implementation are therefore one
immutable unit: `caller @ SHA-X → workflow @ SHA-X → engine @ SHA-X`.

## Local CLI

CI and local runs share the same core evaluator:

```bash
releasegraph branch-contract check --head chore/cutover-provider-fly --base main
```

Optional convenience (never enforcement):

```bash
releasegraph branch-contract new chore/cutover-provider-fly
# fetch production base -> verify clean worktree -> create from exact remote HEAD
```

## Failure recovery

When the gate fails, rebuild the transition; do not rebase by default:

1. `git fetch origin main`
2. create a fresh branch from the exact base HEAD
   (`releasegraph branch-contract new chore/cutover-<topic>`)
3. cherry-pick or reapply **only** the intended production transition
4. verify the diff
5. replace or update the pull request

A cutover branch should be tiny; recreating it is cheaper and safer than
replaying history.
