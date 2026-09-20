# ReleaseGraph

Serverless, declarative, DAG-driven release orchestration for GitHub Actions.

**Language / 语言:** [中文](README.md) · English · [Documentation](https://redtidev1918.github.io/releasegraph/)

```text
        core
       /    \
     cli    web
       \    /
      deploy
```

ReleaseGraph determines what is ready, dispatches it, verifies releases, and recovers incomplete transactions. No server, database, or polling daemon is required.

## Status

The repository is migrating from the production-proven Python implementation to a standalone Go binary.

**What actually runs in production today**, in the reusable workflow every managed
repository calls:

| capability | implementation | mutating |
|---|---|---|
| provider reconciliation (`provider reconcile --apply`) | **Go** | yes — it reconciles release-please metadata and labels |
| release planning (`workflow-plan`) | Python | no |
| static checks, asset gate | Python | no |
| staging, publishing, audit | Python | yes |

So the Go core is **not** read-only in production: it already performs the provider
reconciliation step, and that is why its contract is pinned by every caller. Everything
else on the write path is still Python v1.

The Go core is also safe to adopt read-only, which is how the rest of it is used today:

```bash
releasegraph doctor
releasegraph fleet --owner acme --public-only --output json
releasegraph graph --file release-graph.yml --format mermaid
releasegraph inspect --path .release-policy.yml
releasegraph audit --path .release-policy.yml --root dist/release --output json
releasegraph plan --path .release-policy.yml --root . --output json
releasegraph plan --graph release-graph.yml --state health.json --output json
releasegraph plan --graph release-graph.yml --live --output json
```

Mutating release, repair, dispatch, registry publishing, and retention remain on the Python v1 path until Python/Go contract canaries pass.

## Serverless control repository

A thin control repository contains user topology and ephemeral reconcile workers:

```text
release-graph.yml
.github/workflows/orchestrate.yml
.github/workflows/watchdog.yml
```

Copy the minimal callers from [`examples/control`](examples/control). A reconcile event fetches current GitHub state, computes a plan, and exits; completion starts a new run rather than holding a long-lived job.

## 30-second read-only trial

```bash
go build -o releasegraph ./cmd/releasegraph
./releasegraph doctor
./releasegraph graph --file examples/control/release-graph.yml --format mermaid
./releasegraph inspect --path examples/policy/.release-policy.yml
```

Audit-only commands never call a write API. Grant write permission only when adopting the existing reusable release workflow; grant cross-repository dispatch permission only after validating the graph.

## Declarative graph

```yaml
apiVersion: releasegraph.dev/v1
projects:
  core:
    repo: {owner: acme, name: core}
  cli:
    repo: {owner: acme, name: cli}
    dependsOn:
      - {id: core, condition: healthy}
```

Events request reconciliation. They are not state: every plan is computed from fresh Git refs, GitHub Releases, workflow state, policies, and registry state. A healthy upstream wakes downstream, but it never forces a meaningless downstream version bump.

## Project policy

A managed repository declares its required contract in `.release-policy.yml`:

```yaml
apiVersion: releasegraph.dev/v1
kind: binary
versioning:
  provider: release-please
assets:
  required:
    - app-*-linux-amd64.tar.gz
    - app-*-darwin-arm64.tar.gz
registries:
  github:
    required: true
checksums: true
metadata: true
release:
  postRelease:
    - id: refresh-docs
      type: github-workflow
      required: true
      workflow: update-download-page.yml
      inputs:
        tag: "{{tag}}"
```

Post-release actions (`release.postRelease`) run after a release is published:
resumable, idempotent, exact-tag workflows (see [docs/EN POST-RELEASE](docs/en/POST-RELEASE.md)).

Build adapters own compilers and package managers and place candidates in `dist/release/`. ReleaseGraph validates ordering, the asset contract, checksums/metadata, immutable tags, registries, and recovery.

See:

- [Architecture](docs/en/ARCHITECTURE.md)
- [Authentication](docs/en/authentication.md)
- [Policy](docs/en/POLICY.md)
- [Migration](docs/en/MIGRATION.md)
- [Recovery](docs/en/RECOVERY.md)

## Documentation

This README covers what ReleaseGraph is; the contracts, auth, and operations live on the docs site
<https://redtidev1918.github.io/releasegraph/>:

| What you want | Where |
| --- | --- |
| Get one release through | [Quick start](docs/quick-start.md) |
| Concepts and the DAG plan contract | [Concepts](docs/concepts.md) · [Plan](docs/plan.md) |
| Call it from your repository | [Callers](docs/callers.md) |
| Permissions and credentials | [Authentication](docs/authentication.md) |
| Troubleshooting, rollback, recovery | [Health](docs/health.md) · [Recovery](docs/RECOVERY.md) |
| Branch contract and PR flow | [Branch contract](docs/branch-contract.md) · [PR lifecycle](docs/pr-lifecycle.md) |

## Acknowledgements

- [gopkg.in/yaml.v3](https://github.com/go-yaml/yaml): plan parsing (the only Go dependency).
- [GitHub Actions](https://github.com/features/actions): the execution plane — ReleaseGraph orchestrates workflows rather than running its own scheduler.
