# Migration

The migration preserves the Python release semantics that already ran in production. Do not rewrite language, release semantics, and all repositories in one change.

## Phases

1. Audit the Python implementation and freeze its tested invariants.
2. Extract the owner-agnostic domain model.
3. Ship the Go binary as read-only: `doctor`, `graph`, `inspect`, and `plan`.
4. Compare Go plans against Python output with fixtures (`scripts/contract-compare`).
5. Add Go asset inspection and remote read-only GitHub/registry adapters.
6. Canary Go audit/fleet output while Python remains the mutator.
7. Implement same-version Go repair and release transaction behind a dry-run gate.
8. Add serverless reconcile/dispatch and watchdog workflows.
9. Move user fleet configuration into a thin `release-control` repository.
10. Canary binary/hybrid, Python, Node, container, and deploy project kinds.
11. Move Go to stable `v1`; keep the Python wrapper as a temporary compatibility alias.
12. Remove the obsolete Python runtime only after the Go stable channel has canaried.

## Compatibility

- Existing callers pinned to the pre-rename `redtidev1918/release-infra/.github/workflows/reusable-release.yml@v1` continue through GitHub redirects; new callers use `redtidev1918/releasegraph`.
- Release Please continues to manage versions/PRs/changelogs only; it does not create public Releases.
- Published Git tags remain immutable and historical tags are never deleted by retention.
- Infrastructure failures are repaired at the same version rather than producing a new patch.
- `status.json` is a snapshot/dashboard artifact, not a database.

## Private repositories and reusable workflows

A reusable workflow can only call another repository's reusable workflow when the caller can access the called repository. On a personal account this means a **private** managed repository cannot call the reusable workflow in the public ReleaseGraph repository: the workflow run ends in `startup_failure` with no job created, regardless of the repository Actions allow-list.

Options, in order of preference:

1. Keep the engine private and place all managed private repositories in the same organization/enterprise with an Actions access policy covering the engine.
2. Make the managed repository public (preferred for tools that already publish public Releases).
3. Keep it unmanaged: retain a repository-owned `scripts/build-release` and `.release-policy.yml` as the local build contract, and release manually. Do not copy the reusable workflow implementation into the private repository.

## Read-only adoption

Start with no write permissions:

```bash
releasegraph graph --file release-graph.yml --output json
releasegraph inspect --path .release-policy.yml
releasegraph plan --graph release-graph.yml --live --output json
```

Add release write permissions only after those plans match expected behavior. Add cross-repository dispatch credentials last.
