# Prepare a ReleaseGraph release tag

For ReleaseGraph's manual version provider, first update
`versioning.version` in `.release-policy.yml` through a reviewed PR. Merge it
into the configured production base and check out that base's current remote
HEAD in your own worktree. The tracked working tree must be clean.

Plan the tag, then apply the same command explicitly:

```bash
python -m release_infra.cli prepare-tag --version 1.5.19
python -m release_infra.cli prepare-tag --version 1.5.19 --apply
```

The plan reports the version, tag, source commit and proposed action as JSON.
The version must match the committed policy. The production base comes from
`repository.git.productionOperations.base`, or the remote's default branch.
Feature commits and stale base commits are rejected.

Apply creates and pushes an annotated tag through the existing tag primitive.
An existing tag at that commit is a no-op; a tag pointing elsewhere fails and
is never moved. A push failure is reported to the caller.

The `v1.*.*` tag push starts `.github/workflows/infra-release.yml`, which runs
tests, builds all six binaries, stages assets, publishes and audits the release.
Creating the tag does not mean publishing succeeded: verify the workflow and
release artifacts before reporting the version as published. Push using an
identity whose events can trigger Actions; GitHub's workflow `GITHUB_TOKEN`
suppresses downstream push-triggered workflows.
