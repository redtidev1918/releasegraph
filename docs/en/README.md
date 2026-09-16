# ReleaseGraph

Serverless, declarative, DAG-driven release orchestration for GitHub Actions.

ReleaseGraph computes release plans from desired state, current GitHub and registry state, project policy, and a dependency graph. Every worker runs in GitHub Actions and exits after planning or dispatching work. No server or central database is required.

**Language / 语言:** [中文](/) · English · [GitHub repository](https://github.com/redtidev1918/releasegraph)

## Start here

1. Read the [architecture](ARCHITECTURE.md) and [policy contract](POLICY.md).
2. Copy the [control repository example](https://github.com/redtidev1918/releasegraph/tree/v1/examples/control).
3. Run `releasegraph plan --graph release-graph.yml --live --output json` with read-only access.
4. Review [authentication](authentication.md) before enabling writes.

## Documentation

- [Architecture](ARCHITECTURE.md)
- [Policy](POLICY.md)
- [Calling the release workflow](callers.md)
- [Release health](health.md)
- [Plan contract](plan.md)
- [Branch contract](branch-contract.md)
- [Authentication](authentication.md)
- [Recovery](RECOVERY.md)
- [Migration](MIGRATION.md)
- [Quick start](quick-start.md)
- [Core concepts](concepts.md)

## Fleet observation snapshots

This repository is a release control / fleet repository. Besides the desired / managed
inventory in `fleet.yaml`, two **generated snapshots are intentionally committed**:

| File | Purpose | How it is produced |
| :-- | :-- | :-- |
| `STATUS.md` | Human-readable fleet status snapshot | `.github/workflows/fleet-audit.yml` runs `release_infra/inventory.py` |
| `status.json` | Machine-readable twin (with `schema_version`) | The same run; identical timestamp to `STATUS.md` |

They are **not hand-maintained configuration**, and they are deliberately **not** demoted to pure
CI artifacts: committing them makes git history the record of how fleet state changed over time —
for example when a repository moved from `NEEDS_REVIEW` to `HEALTHY`. The workflow commits only
when the state actually changes, so it does not produce a commit on every run.

```text
fleet.yaml   = desired / managed inventory (source of truth)
STATUS.md    = generated human snapshot
status.json  = generated machine snapshot
```

Both files carry a **GENERATED — DO NOT EDIT** header. To change state, change `fleet.yaml` or the
generator — not the snapshot.
