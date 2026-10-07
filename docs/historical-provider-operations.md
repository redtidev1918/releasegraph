# Historical provider operations

Keep existing tags and released artifacts intact. After explicit human acceptance
of a historical conflict, inspect its version first. Component release PRs can
omit a numeric version in their title; use the inspected PR explicitly:

```sh
releasegraph provider inspect --repo owner/name --version 0.4.1 --scope fleet
releasegraph provider waive --repo owner/name --version 0.4.1 --pr 29 --scope fleet
releasegraph provider waive --repo owner/name --version 0.4.1 --pr 29 --scope fleet --apply
releasegraph provider reconcile --repo owner/name --version 0.4.1 --scope fleet
releasegraph provider reconcile --repo owner/name --version 0.4.1 --scope fleet --apply
```

The explicit PR must match the inspected release and its immutable merge commit.
Planning does not change labels. The existing title-based waiver command remains
compatible. Record the human acceptance in the PR conversation.

An obsolete release PR must not roll an already published version backwards:

```sh
releasegraph provider retire --repo owner/name --version 3.1.0 --pr 17 --superseded-by 3.1.1 --scope fleet
releasegraph provider retire --repo owner/name --version 3.1.0 --pr 17 --superseded-by 3.1.1 --scope fleet --apply
```

Retirement requires a strictly newer stable version with a public release,
existing tag, complete artifacts, verified checksums, and healthy required
registries. It archives the PR context in an issue before commenting and closing.
It preserves the branch and all history, and refuses `keep-open` PRs.

The manually dispatched `provider-operations.yml` workflow supports these
commands on Linux hosts. It requires the explicit `RELEASEGRAPH_FLEET_TOKEN`
secret, performs inspection and a plan before applying, and never uses the
repository token as a fallback. Leave `apply` unchecked to obtain a reviewable
plan.
