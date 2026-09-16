# Recovery

Scheduled callers compare the manifest version with the tag, draft/public Release, required assets, registries, and Latest. A healthy version is a no-op. An absent or draft version resumes the same transaction; no new version is calculated.

Run the repository's **Release** workflow manually with:

- `version`: an existing manifest version; blank reads the manifest. Branch names are rejected.
- `dry_run`: build and validate without publishing.
- `repair`: allow an incomplete public Release to enter explicit repair.
- `force`: rerun a healthy version for audit/build diagnostics.
- `stage`: reserved recovery label recorded in the Job Summary.

## Manual dispatch semantics

The reusable workflow distinguishes PR validation, safe rehearsals, and real transactions:

| Trigger | `dry_run` | `force` | `repair` | What runs | Remote mutation |
|---|---:|---:|---:|---|---|
| pull request | forced `true` | — | forced `true` | plan, test, build matrix, version gate, artifact collection | none; publish/audit/retention steps are skipped |
| manual, healthy version (no options) | false | false | false | plan only — healthy version is a **no-op** | none |
| manual `dry_run=true` | true | false | any | plan, test, build matrix, version gate, asset gate; **stops before** draft/registry/Release/audit | none |
| manual `dry_run=true force=true` | true | true | any | full build matrix for the current version even when healthy; same stop point | none |
| schedule (recovery) | false | false | false | missing/incomplete → resume same transaction; healthy → no-op | yes, only when work exists |
| push (release PR merge) | false | false | false | full release transaction | yes |
| manual `repair=true` | false | false | true | completes an incomplete release at the **same** version | yes, idempotent |

A dry run never performs a remote publish/audit/retention operation. In particular it does not audit the remote tag commit: a PR or manual dispatch is never the released commit, so auditing immutable state there could only hard-fail with `tag commit mismatch` without proving anything. Use `force=true` *without* `dry_run` if you explicitly want to re-audit a healthy release against live state.

## Repair covers

- GitHub Release exists but is missing required assets or checksums.
- npm/PyPI package published but GitHub assets missing (existing registry versions are verified, never re-published).
- GHCR image pushed but the Release finalize step was interrupted.
- Workflow interrupted between tag creation and publication.

## Deterministic failures (no retry, no new version)

Tag/asset/registry hash conflicts are deterministic hard failures. Network and API failures use bounded exponential retry. Never move or delete a historical semver tag to make recovery pass.

```bash
# repository-local equivalent of the Actions inputs
PYTHONPATH=.releasegraph python3 -m release_infra.cli workflow-plan --version 1.2.3
PYTHONPATH=.releasegraph python3 -m release_infra.cli audit --version 1.2.3
```
