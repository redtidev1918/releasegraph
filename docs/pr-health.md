# Fleet PR Health

ReleaseGraph owns health observations and incident state. `fleet.yaml` alone
selects enabled repositories. PR Health is separate from release health,
branch contracts and PR lifecycle; Dependabot is included, never closed.

## Read-only audit

```
releasegraph pr-health audit --manifest fleet.yaml --report pr-health.json
```

Requires `RELEASEGRAPH_FLEET_TOKEN` (existing credential resolver); no repository
credential fallback. Use the control-plane App workflow when the local process
has no fleet credential. The scanner never merges, closes or edits PRs.

| PR state | Evidence |
|---|---|
| PASS | Observed checks pass and required contexts are present with `isRequired` |
| FAIL | At least one check failed, even when another is pending |
| PENDING | Checks still running |
| MISSING | No checks, or an expected required check is absent |
| SKIPPED | All checks skipped/neutral, or a required check skipped; never executed success |
| UNKNOWN | API error, incomplete evidence, changed head, or unknown conclusion |

The report separately carries `complete` and aggregate `status`; incomplete
coverage is always `DEGRADED`. A fleet with no enabled entries is not healthy.
Exit 0 = complete PASS/PENDING, 20 = detected incidents, 30 = degraded scan.
A nonzero audit exit still writes its JSON report and Actions Summary.

## Collection and evidence

Open PRs use the existing paginated REST provider, checked against GraphQL open
counts before and after scanning. Each PR's head `statusCheckRollup` is paged
independently with count, cursor and identity checks. Both legacy StatusContext
and CheckRun nodes participate. Classic branch protection plus active rulesets
supply required contexts; permission failures cannot become empty requirements.
Actions runs and failed jobs have count-verified pagination. Reports preserve
check/job URLs, author, timestamps, head, draft and mergeability.

A PR head change invalidates its observation. Scans are bounded (30 minutes
fleet, 5 minutes per repository, 2 minutes per PR). API requests have 30-second
timeouts and at most four attempts with exponential backoff. Retry-After and
rate-limit reset are respected; waits exceeding 60 seconds stop the request as
degraded instead of retrying too soon. A repository error does not stop siblings.

Classification uses explicit diagnostic phrases for DEPENDENCY_BREAKING,
TYPE_ERROR, CONFIG_DRIFT, CI_CONFIGURATION, SECRET_BOUNDARY, CONTRACT_MISMATCH,
INFRASTRUCTURE; otherwise UNCLASSIFIED. A major-version PR title alone does not
prove a dependency regression. Check output and annotations are examined in
memory; only matched diagnostic phrases are exported, not raw logs. GitHub job
URLs provide access to logs under the viewer's own permissions.

## Tests

`go test ./internal/prhealth ./internal/github ./internal/cli` covers green,
failed and mixed pending checks, missing/unknown checks, required app identity,
Dependabot, multiple failures, inaccessible repositories, incomplete pages,
429/403/503 retries, cancellation, invalid/external pagination and credential
separation. Run the repository's Go/Python/static checks before pushing.

## Delivery state

Stage 1 implements audit only. Persistent Issue state and scheduled delivery
are separate reviewable stages. No schedule or real alert delivery is claimed
by this stage. A live control-plane App scan and dedicated test-PR failure,
deduplication and recovery are required before accepting the whole system.

Sources: [GitHub Checks GraphQL](https://docs.github.com/en/graphql/reference/checks),
[branch protection](https://docs.github.com/en/rest/branches/branch-protection).
