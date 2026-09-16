# Architecture

ReleaseGraph is a serverless release control plane. Each GitHub Actions run is an ephemeral reconcile worker.

```text
Event
  ↓
Reconcile actual state
  ↓
Compare desired state, policy, and release graph
  ↓
Plan ready / blocked / recoverable / noop work
  ↓
Dispatch eligible work
  ↓
Exit
```

There is no always-on process and no central database. Authoritative state comes from Git refs, GitHub Releases and workflow state, registry state, repository policies, metadata, and the declarative graph. `status.json` is only a cache/snapshot for reporting.

## Layers

1. **Generic engine**: Go CLI, typed domain model, DAG engine, policy/asset/registry contracts, reusable Actions, examples, and tests.
2. **User control repository**: `release-graph.yml`, orchestration workflows, watchdog, fleet snapshot, and user-specific docs.
3. **Managed repositories**: `.release-policy.yml`, version provider configuration, and optional local build adapter writing to `dist/release/`.

The generic engine contains no owner-specific conditional logic. User-specific repositories exist only in a control repository, examples, anonymized fixtures, or historical migration snapshots.

## DAG semantics

The graph is declarative and acyclic. The engine validates node references, dependency conditions, parallel branches, and cycle errors. It never guesses missing edges or repairs an invalid graph automatically.

Node kinds:

- `release`: produces or validates a project release according to policy.
- `deploy`: dispatches/validates deployment without requiring a new semantic version.
- `reconcile-only`: recomputes health but performs no release.

A release event only wakes downstream reconciliation. A downstream node is `NOOP` unless its own desired state changed.

## Single-release transaction

```text
PREPARE → TEST → BUILD → ASSET GATE → VERIFY/CREATE TAG
      → RESUME DRAFT → UPLOAD → PUBLISH REQUIRED REGISTRIES
      → VERIFY REGISTRIES → PUBLISH GITHUB RELEASE → MARK LATEST
      → AUDIT → RETENTION → EMIT RECONCILE SIGNAL
```

Required assets and required registries are hard gates. Optional channels cannot block required channels. Public incomplete Releases are invalid.

## Recovery

The release transaction key is `repository + version`. A missing tag is created; an existing tag at the expected commit resumes; a tag pointing elsewhere hard-fails. Existing assets with the same digest resume; different digests hard-fail. A registry version is verified, never overwritten. Transient failures use bounded retry with backoff; policy, graph, checksum, tag, and immutable registry conflicts do not retry.

## Security levels

- **Audit Only**: read-only `doctor`, `graph`, `inspect`, `plan`, `audit`, and `fleet`.
- **Release Management**: per-repository release transaction permission.
- **DAG Orchestration**: short-lived cross-repository dispatch token via GitHub App installation token, with fine-grained PAT as fallback.

No classic all-repository super PAT is required.

The current Go canary stops after a live, read-only plan. Registry inspection, dispatch, and mutation remain on the Python path until their contracts are implemented and canaried.
