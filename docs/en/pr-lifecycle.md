# Pull-request lifecycle contract

The contract turns one team habit into a declarative, testable, auditable rule:

```text
An open pull request is a merge candidate, not a work tracker.

Every open pull request belongs to exactly one lifecycle state.

A pull request that is no longer a merge candidate leaves the merge queue, and
its engineering context is preserved before it does.
```

This is the third governance dimension, alongside **release health** and the
**branch contract**. Release health answers *is the published artifact
trustworthy*; the branch contract answers *is this branch based on production*;
the lifecycle contract answers *is this still a merge candidate*. Like the
branch contract, a lifecycle finding is isolated to the pull request it is about
— it never marks a whole repository `BROKEN`.

## Isolated per pull request

Governance state and repository state are different things. A repository whose
only open pull request is parked is not a broken repository; it is a repository
with one parked pull request.

| Dimension | Question | Blast radius of a finding |
|---|---|---|
| Release health | Is the published artifact trustworthy? | The release |
| Branch contract | Is this branch based on production? | The pull request |
| PR lifecycle | Is this still a merge candidate? | The pull request |

## The states

| State | Meaning | Leaves the queue |
|---|---|---|
| `RELEASE` | A managed publication pull request (release-please, dependabot, or another exempt actor/branch). It is a *publish* queue. | never |
| `KEEP_OPEN` | A human added an exempt label. The only escape hatch. | never |
| `ACTIVE` | Still moving: checks running, or touched inside the window. | no |
| `MERGE_READY` | Green and mergeable; waiting for a human. | no |
| `OBSOLETE` | Nothing left to merge: the commits are already in the base. | yes |
| `PARKED` | Paused: still a draft, or conflicting, and inactive past the window. | yes |
| `ATTENTION` | Needs a human decision, but is not safe to act on automatically. | no |

`ATTENTION` exists so that incomplete evidence degrades to *ask a human* rather
than to *close the pull request*. A failing pull request that has been untouched
for a month is exactly the case where automation must not guess.

### The rule chain is the contract

Rules are applied in this order, and the order is normative:

1. exempt **branch** → `RELEASE`
2. exempt **author** → `RELEASE`
3. exempt **label** → `KEEP_OPEN`
4. checks still running → `ACTIVE`
5. inside the inactivity window → `ACTIVE`
6. zero commits beyond the base, or zero changed files → `OBSOLETE`
7. green and mergeable → `MERGE_READY`
8. still a draft, or conflicting → `PARKED`
9. otherwise → `ATTENTION`

An exemption outranks every other rule, "still moving" outranks "done", and
"already landed" outranks "ready to merge" — a pull request with nothing left to
merge is not a merge candidate at all.

Rule 6 has two shapes on purpose. A squash merge can leave `ahead_by > 0` while
the resulting diff is empty: the commit identities differ even though the
content already landed. Comparing **changed files** as well as commit counts is
what catches it.

## The three invariants

ReleaseGraph NEVER:

1. **merges a normal pull request.** Merging is a human decision. There is no
   merge action in the closed set.
2. **force-pushes or rebases.** History is never rewritten.
3. **deletes a pull request branch.** A pull request that leaves the queue
   leaves its branch behind, so the work stays reachable.

The permitted mutations are exactly three, and each is the narrowest endpoint
that does the job:

| Action | Endpoint |
|---|---|
| `ensure_issue` | create an issue (or reuse the existing one) |
| `comment` | comment on the pull request |
| `close_pull_request` | close the pull request |

Adding an action is a deliberate, reviewed change: the closed set is asserted by
test, so a "merge" or "delete_branch" action cannot appear by accident.

## Why archived before closed

An open pull request is often the only durable record of *why* work was paused.
Closing it and calling the pause "stale" destroys that context.

So a `PARKED` pull request is always archived into an issue **first**, and only
then closed. The issue records the original pull request, the branch, the last
commit, the base at the time, the classification reason and how long it was
inactive, plus a "how to resume" section. The pull request itself gets a comment
pointing at the archive.

The archive is idempotent: the issue body carries a greppable marker
(`<!-- releasegraph:parked-pr:<n> -->`), so a second run reuses the issue rather
than creating a duplicate.

`OBSOLETE` is closed without an archive issue, because there is nothing to
resume: the change already landed. It still gets an explanatory comment, so the
close is understandable after the fact.

Policy validation enforces the pairing: `archiveParkedToIssue` must be `true`
whenever `closeParked` is `true`. Closing parked work without archiving it is a
config error, not an option.

## Policy schema

Declared in the repository's `.release-policy.yml`:

```yaml
repository:
  pullRequests:
    lifecycle:
      parkedAfterDays: 7        # inactivity window; 1..365
      archiveParkedToIssue: true # must be true while closeParked is true
      closeParked: true          # parked work leaves the queue once archived
      deleteBranch: false        # must be false; there is no opt-out
      exempt:
        branches:                # managed elsewhere (glob patterns)
          - "release-please--*"
        actors:                  # managed elsewhere (case-insensitive)
          - "github-actions[bot]"
          - "dependabot[bot]"
        labels:                  # the human escape hatch
          - "keep-open"
```

Defaults when a field is omitted:

| Field | Default |
|---|---|
| `parkedAfterDays` | `7` |
| `archiveParkedToIssue` | `true` |
| `closeParked` | `true` |
| `deleteBranch` | `false` (and may not be set to `true`) |
| `enabled` | `true` — declaring the block enables it |

The dimension is **opt-in**: a repository that does not declare
`pullRequests.lifecycle` is classified for reporting only, and nothing is ever
acted on. That is why a read-only fleet audit is safe to run everywhere.

Declaring `enabled: false` keeps the declaration for auditing while disabling
enforcement.

### Why 7 days, and why not "stale"

The window is deliberately short. A merge queue that is 30 days deep is not a
queue. But the window is not what decides: nothing is closed merely for being
old. A pull request is only ever closed when it is **provably out of the queue**
— either the change already landed (`OBSOLETE`) or the work was explicitly
paused by being a draft or conflicting (`PARKED`). Age alone yields `ATTENTION`,
which asks a human.

### `keep-open` is the escape hatch

Label the pull request `keep-open` and the contract never touches it. This is
intentional and permanent: the classification is advisory, and a human must
always be able to say "no, this one stays".

If a repository wants additional retained labels, it lists them under
`exempt.labels`. It cannot make the contract act on a labeled pull request.

## Local CLI

```bash
# Read-only: report every open pull request's state, mutate nothing.
releasegraph pr-lifecycle audit

# One repository instead of the fleet manifest.
releasegraph pr-lifecycle audit --repo owner/name

# See what would happen, without happening.
releasegraph pr-lifecycle apply --dry-run --limit 2

# Enforce, bounded to two pull requests leaving the queue per repository.
releasegraph pr-lifecycle apply --limit 2

# Feed the dashboard: write the machine-readable report as a sidecar.
# (--report, not --output: --output means "output format" for every command.)
releasegraph pr-lifecycle audit --format json --report pr-lifecycle.json
```

`--limit` bounds the blast radius of one pass. `--dry-run` enumerates the
mutations without performing them, including the archive issues that would be
created.

Exit codes follow the usual contract: a scope violation is `NeedsReview`, a
transient provider failure is `Retry`, a policy error is `Blocked`.

## CI integration

One scheduled workflow enforces the contract fleet-wide:
[`.github/workflows/pr-lifecycle.yml`](../.github/workflows/pr-lifecycle.yml).
No repository installs a `stale.yml` of its own: one policy, one classifier, one
audit trail.

- the **schedule** enforces, bounded by `limit`;
- a **manual run** (workflow dispatch) audits by default and acts only when
  `apply` is set to `true`.

The schedule's `limit` **starts at 1**. The contract is allowed to close pull
requests, so its first passes are treated as a canary: at most one pull request
leaves the queue per repository per day, which a human can check one by one —
archive issue created, comment correct, pull request closed, branch still there —
before the limit is raised to its steady-state value.

## Permissions

The lifecycle pass runs under its own GitHub App installation token, created
per run in the workflow with the narrowest permissions that work:

| Permission | Level | Why |
|---|---|---|
| Metadata | read | always read-only for an app token |
| Contents | read | read `.release-policy.yml`; compare head against base |
| Pull requests | write | comment on and close a pull request |
| Issues | write | create and reuse archive issues |
| Actions | read | workflow runs and jobs |
| **Checks** | **read** | a commit's **check runs** live in the Checks API |

`checks: read` is not the same as `actions: read`. Without it, every pull request
would appear to have no CI at all, so a green pull request could never be
recognized as `MERGE_READY` and would eventually age into `PARKED`. The two
permissions are separate because the two APIs are separate.

There is deliberately **no** `contents: write`, no `administration` and no
`secrets`: the pass cannot delete a branch, rewrite history or change
repository settings. The dashboard's own audit runs with an even narrower,
fully read-only token.

## Fleet dashboard

`releasegraph fleet audit` reports the lifecycle dimension next to the others:

```text
Repository                Release       Branch Contract   PR Lifecycle
owner/name                v1.4.11       enforced          3/5 (-2)
```

`3/5 (-2)` reads as: five open, three still in the merge queue, two about to
leave. An open count alone would read as a backlog; the column is deliberately
the queue, not the count.

A repository that has declared no contract shows `—`, exactly like an absent
branch contract: an undeclared dimension is not a finding. The classification is
produced by `releasegraph pr-lifecycle audit --report pr-lifecycle.json`, and
the rendered field is owned by `release_infra`, so the verdicts are never
rendered twice.

## Recovery

If a pull request was closed in error:

1. the branch still exists — nothing was deleted;
2. if it was `PARKED`, the archive issue is still open and holds the context;
3. reopen the pull request or open a new one from the branch;
4. add the `keep-open` label if the classification should not apply again.

Nothing about the contract is destructive, so recovery is a reopen rather than a
restore.
