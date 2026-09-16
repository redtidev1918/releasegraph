## Workflow-dispatch post-release permission

`release.postRelease` with `type: github-workflow` dispatches a same-repo `workflow_dispatch`. That endpoint requires `Actions: write` on the ephemeral `GITHUB_TOKEN`.

- `github-workflow` postRelease requires the calling workflow to grant `actions: write`.
- The job executing `github-workflow` postRelease must preserve `actions: write` in its job permissions.
- `GITHUB_TOKEN` is sufficient for a same-repo `workflow_dispatch` when `Actions: write` permission is granted; a PAT/App is not required by default.

# Authentication

ReleaseGraph uses progressively broader permissions. A reusable workflow cannot elevate the permissions granted by its caller.

## Level 1: Audit only

Public repositories need no write credential. Set `GITHUB_TOKEN` only for private repositories or higher API limits; grant repository contents and metadata read access.

## Level 2: Release management

Each managed repository uses its ephemeral Actions `GITHUB_TOKEN`. The caller grants only the permissions required by that repository, normally `contents: write` plus registry-specific trusted-publishing permissions.

## Level 3: DAG orchestration

Prefer a GitHub App installation token scoped to repositories in the graph. `repository_dispatch` requires `Contents: write` on each target. If a control repository chooses `workflow_dispatch` instead, that endpoint requires `Actions: write`.

```yaml
- id: token
  uses: actions/create-github-app-token@bcd2ba49218906704ab6c1aa796996da409d3eb1 # v3
  with:
    client-id: ${{ vars.RELEASEGRAPH_CLIENT_ID }}
    private-key: ${{ secrets.RELEASEGRAPH_PRIVATE_KEY }}
    owner: ${{ vars.RELEASEGRAPH_OWNER }}
    permission-contents: write
```

A fine-grained PAT is the fallback for a small fleet. Restrict it to graph repositories and grant `Contents: write` for `repository_dispatch`, or `Actions: write` for `workflow_dispatch`. Do not use a classic all-repository PAT by default.

Never put tokens in policies, graph files, release metadata, logs, or artifacts.
