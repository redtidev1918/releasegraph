# Quick start

**Language / 语言:** [中文](/quick-start.md) · English

Try it read-only first, then grant permissions step by step.

## 1. Build the CLI

```bash
git clone https://github.com/redtidev1918/releasegraph.git
cd releasegraph
go build -o releasegraph ./cmd/releasegraph
./releasegraph doctor
```

## 2. Define the DAG

Create `release-graph.yml` in the control repository:

```yaml
apiVersion: releasegraph.dev/v1
projects:
  core:
    repo: {owner: acme, name: core}
  app:
    repo: {owner: acme, name: app}
    dependsOn:
      - {id: core, condition: healthy}
```

## 3. Declare a release contract per project

Create `.release-policy.yml` in the managed repository:

```yaml
apiVersion: releasegraph.dev/v1
kind: binary
versioning:
  provider: release-please
assets:
  required:
    - app-linux-amd64
registries:
  github:
    required: true
checksums: true
metadata: true
```

The project's own build script is responsible for placing candidate files into `dist/release/`;
ReleaseGraph only validates and orchestrates the release contract.

## 4. Generate a read-only live plan

```bash
GITHUB_TOKEN=... ./releasegraph plan \
  --graph release-graph.yml \
  --live \
  --output json
```

Public repositories can run without a token, but the anonymous API limit is lower. Do not grant
write permissions during the read-only phase.

## 5. Put it into the control repository

Copy the two caller workflows from
[`examples/control`](https://github.com/redtidev1918/releasegraph/tree/v1/examples/control).
An event triggers one re-read and plan, and a watchdog compensates for lost events every 5 hours;
neither starts a long-running process.

The current example only generates a live plan and does not dispatch. Once the write path passes
the contract canary, add the minimal cross-repository permissions described in
[authentication](authentication.md).
