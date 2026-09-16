# Core concepts

**Language / 语言:** [中文](/concepts.md) · English

## Desired vs. Actual

Events are not the source of truth. On receiving an event, ReleaseGraph re-queries Git tags,
GitHub Releases, assets, workflows and registries, then produces a plan from:

```text
Desired State + Actual State + Policy + Dependency Graph → ReleasePlan
```

## Event and reconcile

The standard execution model is:

```text
Event → Reconcile → Plan → Dispatch → Exit
```

An upstream release completing only wakes the downstream to recompute. When the downstream has
no new desired version it must be a `NOOP` — it must never invent a patch version on its own.

## DAG

The dependency graph must be acyclic. Independent branches may run in parallel, and one failing
branch must not block unrelated branches. A cycle, a missing node or an illegal condition makes
graph validation hard-fail.

## Safe release and recovery

A real release must become public only after tests, builds and asset checks have passed. An
existing tag may continue only when it points at the expected commit; a wrong commit, a
same-named asset with a different digest, and immutable-registry conflicts must all hard-fail.
Infrastructure failures are recovered through `repair`/`retry` at the same version — never by
bumping another patch to work around them.

Historical Git tags are never deleted by retention. `status.json` is only a snapshot, not a
database.
