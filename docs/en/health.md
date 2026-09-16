# Release health

Health answers exactly one question: **does the released state satisfy the
policy?** It is deliberately not an instruction. The action to take is a
different axis, and confusing the two is how a fleet ends up re-releasing
something that was only ever mis-tagged.

Three layers describe this one vocabulary, and they are kept in agreement by
`tests/test_health.py`, which fails when any of them drifts:

| Layer | Definition |
|---|---|
| Python fleet inventory | `release_infra/health.py` |
| Go core | `internal/domain/health.go`, values from `internal/domain/model.go` |
| Published schema | [`schemas/health-v1.json`](../schemas/health-v1.json) |

## Values

A repository is reported as exactly one of these. There is no `UNKNOWN`: an
unreadable provider is `BROKEN`, which is a fact, not a shrug.

| Value | Meaning |
|---|---|
| `HEALTHY` | The released state satisfies the policy. Carries no reasons. |
| `DEGRADED` | A release exists but an invariant fails. The reason codes say which. |
| `NO_RELEASE` | No non-draft release for the desired version, or the repository is archived or a fork. |
| `UNMANAGED` | The repository declares no release policy, so ReleaseGraph does not manage it. |
| `BROKEN` | Provider state could not be read. |
| `BLOCKED` | A dependency or credential is missing, so no judgement is possible yet. |
| `NEEDS_REVIEW` | A human must decide. |

`internal/domain.Health` is wider than this list: `READY`, `RUNNING`, `NOOP`,
`ACK_PENDING` and `WAIVED` describe a node in a release *transaction*, not a
repository. `domain.RepoHealthValues` is the subset that can describe a
repository, and it is the subset above.

## Reasons

Every value except `HEALTHY` carries at least one reason. Reasons are
machine-readable codes with an optional human-readable detail, so a dashboard
can group by cause while a human still sees which tag or pattern is at fault.

| Code | Raised when |
|---|---|
| `unmanaged_repo` | No release policy is declared. |
| `api_error` | Provider state could not be read. |
| `release_absent` | No non-draft release for the desired version. |
| `release_draft` | A draft release exists and was not published. |
| `repository_archived` / `repository_fork` | The repository is archived or a fork. |
| `version_drift` | The release tag is not the desired version. |
| `target_assets_missing` | A required asset pattern matched nothing. |
| `asset_empty` | A required asset pattern matched only zero-byte assets. |
| `release_run_failed` | The release workflow run did not succeed. |
| `policy_absent` / `policy_unparsable` | The policy is missing or not readable. |
| `dependency_unhealthy` | A dependency is not healthy. |
| `fleet_credential_required` | Fleet scope needs `RELEASEGRAPH_FLEET_TOKEN`. |
| `manual_review_required` | A human must decide. |

`target_assets_missing` and `asset_empty` are separate codes because they need
different fixes: re-upload the asset, or fix an upload that produced nothing.

## The asset comparison

Both the fleet view and the release gate ask the same question with the same
function (`fnmatch`), so they cannot disagree about the same release:

```
required = policy.assets.required
         + RELEASE-METADATA.json
         + SHA256SUMS            # only when checksums are enabled (default)
                                 # and the policy declares required assets
```

A required pattern is satisfied only by a matching asset with `size > 0`. A
pattern that matches nothing is *missing*; a pattern that matches only
zero-byte assets is *empty*. The release planner collapses both into "not
present" — it builds its remote set from assets with `size > 0` — so
`missing + empty` equals the planner's missing set exactly, which
`PlannerParityTest` asserts against the real planner rather than a copy of it.

## Health is not an action

`release_health` belongs to the release planner (`release_infra/release.py`).
It is the *workflow decision* — "what should the release pipeline do?" — and the
planner computes it from commit-level tag drift, drafts, and workflow state that
the fleet inventory never reads. **The fleet inventory therefore does not emit
`release_health` at all.** Two systems publishing the same field name from
different inputs is how contradictory dashboards happen, so one field, one owner.

The contract still has to know how health would project onto that vocabulary,
because that projection is how `PlannerParityTest` compares the contract against
the planner's real output. Health projects as:

| Health | `release_health` |
|---|---|
| `HEALTHY` | `healthy` |
| `DEGRADED` | `repair` |
| `BLOCKED` | `repair` |
| `NEEDS_REVIEW` | `repair` |
| `NO_RELEASE` | `missing` |
| `BROKEN` | `missing` |
| `UNMANAGED` | `missing` |

`reusable-release.yml` compares `release_health != 'healthy'` and 14
repositories call that workflow, so the four `release_health` strings are a
frozen production contract: they may gain values, never lose or rename one.
Anything that is not a clear `HEALTHY` maps to the loudest applicable value, so
an unrecognised state can never be mistaken for a healthy one.

Inside the planner the precedence is `tag-drift` → `repair` → `missing`, and
`tag-drift` wins: a fleet view cannot reproduce that label, because tag drift
there means "the released commit is not the current head", which the inventory
never inspects. Both views still agree on the only thing that matters — whether
the repository is healthy.

Why `DEGRADED` and not a separate value for "release exists, assets do not":
`internal/provider/capabilities.go` already reports missing required binaries as
`DEGRADED`. A second word for the same condition would have recreated exactly
the contradiction this contract exists to remove. The specificity lives in the
reason code.

## Reading it

```bash
releasegraph provider inspect --all --manifest fleet.yaml            # per repository
python3 -m release_infra.cli fleet-audit --owner redtidev1918        # regenerates status.json + STATUS.md
```

`status.json` carries `health`, `health_reasons`, `missing_assets`,
`empty_assets` and the projected `release_health` for every repository.
`STATUS.md` renders the health column; see the fleet dashboard for the reasons.

## Two implementations, one answer

`release_infra/health.py` and `internal/health` are independent implementations
of this contract, and they are compared against **one** set of expectations:
`testdata/health/cases.json` (25 assessments) and
`testdata/health/patterns.json` (484 pattern/name pairs). Those expectations are
written from this document, not generated from either implementation, and both
CI lanes assert them — so agreement means agreeing with the contract, not with
each other.

Measured on the real fleet, `releasegraph fleet audit` and the Python inventory
produce **identical** health values, reason codes, reason details, and
missing/empty asset lists for all 16 managed repositories, including the one
genuinely broken repository:
`redtidev1918/svn-easy-kit` is `DEGRADED` with `target_assets_missing` for the
same six patterns on both sides.

## The pattern language

Asset patterns support literals, `*` (any run, including empty) and `?` (exactly
one character). Anything else is rejected by policy validation, in both
languages, before it can be used.

That restriction is not tidiness — it is what makes two implementations possible
at all. Python's `fnmatch` and Go's `path.Match` are **different languages**:
`[!a]` means "not a" to `fnmatch` and "the literal characters `!` and `a`" to
`path.Match`, while `[^a]` means the exact opposite pair. CPython 3.14's
`fnmatch` also compiles to atomic groups and lookaheads, which Go's RE2 cannot
express, so porting it is impossible. Inside the supported language the two
engines agree — an exhaustive 236,496-pair comparison over literals, `*` and `?`
found no difference, and `testdata/health/patterns.json` pins 484 of those pairs
so both sides keep agreeing.

The restriction costs nothing today: all 48 required patterns across the 16
managed repositories use only literals and `*`, and none of the 137 released
asset names contains a reserved character. A repository that genuinely needs a
character class has to extend the language in `internal/policy/pattern.go` and
`release_infra/policy.py` at once, with the shared fixture extended in the same
commit. The boundary is loud instead of silently disagreeing.

## Which run counts

`release_run_failed` is about the **canonical caller**,
`.github/workflows/release.yml`, and only when that file exists in the default
branch. Both implementations check the file first, because the runs API resolves
a workflow by file name even after the file is deleted: querying blindly reports
`startup_failure` runs of a workflow the repository no longer has. That is
exactly `svn-easy-kit`, whose last commit dropped the reusable caller while
GitHub kept the failed runs.

## Remaining divergences

| Divergence | Impact | Where it is tracked |
|---|---|---|
| `provider.Verdict` carries no `reasons` | a `provider inspect` consumer sees a value without a cause; the fleet views carry reasons already | `internal/provider/provider.go` |
| `provider.Verdict.Health` uses the same field name for the *drift verdict* axis, including `RECOVERABLE` — an available action, not a state | the exact "health is an action" confusion this contract exists to remove | `internal/provider/provider.go`; the value must not be reported as repository health |

`tests/test_health.py`, `tests/test_health_fixture.py` and
`internal/health`'s tests enforce what can be enforced: the schema enum, the
values and reason codes in both languages, Go's repository-health assignments,
the asset comparison against the planner, the pattern language, and the
projection the workflow reads.
