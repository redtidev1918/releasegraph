# Post-release actions

Post-release actions are resumable, idempotent steps that run **after** a
release is public and audited. The release itself stays published — the
transaction just isn't fully converged until every `required` action succeeds.

## When you need them

Typical use: after a GitHub Release lands, refresh a generated download page,
rebuild a documentation site, announce to a channel, or push a registry index.
Because the trigger is *ReleaseGraph*, not a `release: published` webhook, it
also works when the release was created with the repository's `GITHUB_TOKEN`
(which never re-fires workflow events) — the exact tag is passed down
explicitly, so nothing downstream ever has to guess "latest".

## Policy

Add a `release.postRelease` list to `.release-policy.yml`:

```yaml
release:
  postRelease:
    - id: refresh-docs
      type: github-workflow
      required: true
      workflow: update-download-page.yml
      inputs:
        tag: "{{tag}}"
    - id: deploy-docs
      type: github-workflow
      required: false
      workflow: docs.yml
      ref: default # Pages deploy: default branch
```

Fields per action:

| Field | Meaning |
| --- | --- |
| `id` | Unique action id (required) |
| `type` | `github-workflow` (currently the only adapter) |
| `required` | `true`: the release transaction fails until this action succeeds. `false` (default): failure records a warning only |
| `workflow` | Workflow file to dispatch (`type: github-workflow`) |
| `inputs` | `workflow_dispatch` inputs; `{{tag}}`, `{{version}}`, `{{repo}}`, `{{release_id}}`, `{{release_url}}` are substituted from the exact release context |

The legacy single-shell `release.post_publish` string keeps working unchanged.
Repositories without `postRelease` behave exactly as before — nothing is
dispatched implicitly.

## Semantics

- The release body carries a machine-readable marker
  (`<!-- releasegraph-post-release {...} -->`) with per-action
  `status` / `attempts` / `last_error`. Health is re-derived from GitHub; there
  is no state directory.
- Idempotent: an action already `success` is never re-dispatched (same
  repo + tag + action id).
- Resume: a later reconcile with `post_release_health=pending` re-enters the
  release workflow and finishes only the missing actions. It does not
  re-create the release, re-tag, or re-upload assets.
- Required failure keeps the release published and marks the transaction
  `post-release pending`; the next scheduled run retries it (bounded by the
  existing cooldown guard).
- The `github-workflow` adapter dispatches `workflow_dispatch` and polls the
  correlated run to a terminal conclusion (bounded, default 30 minutes),
  instead of treating a `204` as success.

## Verification

```bash
python3 -m release_infra.cli post-release status --path .release-policy.yml
python3 -m release_infra.cli post-release run    --path .release-policy.yml   # resume + retry
```

`releasegraph workflow-plan` (or the reusable `reusable-release.yml`'s
`build-plan` job) now reports `post_release_health`:
`absent` (no actions configured) · `pending` (release public, actions
incomplete) · `satisfied` (all actions success).

## Blocking labels on release PRs

release-please refuses to open the next version while any merged release PR
still carries `autorelease: pending` / `autorelease: triggered`.
`provider reconcile` therefore scans **all** such PRs (not just the manifest's
current version):

- The version is resolved from the PR title first, then from the manifest
  version the merge commit introduced.
- A healthy release transaction is acknowledged automatically
  (add `autorelease: tagged`, remove pending).
- A version superseded by newer releases that cannot be repaired gets an
  explicit human waiver first (`provider waive --repo <repo> --version <v>`,
  recorded as `releasegraph: historical-waived`), then reconcile completes
  the ACK.
- If neither the title nor the manifest resolves the version, reconcile
  errors with the guidance above instead of guessing.
