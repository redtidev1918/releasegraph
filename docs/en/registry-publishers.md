# Registry publishing and repository renames

This page collects rules that recur across repositories and have been verified
against real release pipelines. Do not override them based on one-off web
search results.

## Common rules

- The GitHub repository name is a case-sensitive OIDC claim. After a rename,
  every trusted publisher's Repository field must use the current canonical
  name. GitHub API redirects old names, but OIDC validation does not.
- PyPI and pub.dev trusted publisher configuration is web-console only: there is
  no public management API or CLI. `dart pub` and `twine` can publish, not edit
  publisher settings.
- npm and GHCR do not match on a repository claim: npm uses tokens and GHCR uses
  `GITHUB_TOKEN`, so repository renames do not break their publisher matching.
- Metadata for already-published versions (for example the Repository links in a
  pub.dev sidebar) is historical. It is not edited in the admin page; the next
  version published with corrected metadata replaces it.

## PyPI

- Trusted Publisher fields must match exactly: Owner, Repository (canonical
  case), Workflow file name, and Environment name.
- After a repository rename, an outdated Repository entry fails with
  `invalid-publisher: valid token, but no corresponding publisher`.
- PyPI accepts push and workflow_dispatch events; it has no pub.dev-style
  tag-only restriction.

## pub.dev

- pub.dev allows automated publishing from GitHub Actions only when the
  workflow is triggered by a git tag. Branch-ref OIDC tokens are always rejected:
  `publishing is only allowed from 'tag' refType, this token has 'branch' refType`.
- Each package configures a tag pattern in Admin -> Publishing -> GitHub
  Actions, for example `my_package-v{{version}}`. The publishing workflow must
  be triggered by a tag push matching that pattern.
- After a repository rename, update the Trusted Publisher Repository to the
  canonical name for every package. Tag patterns, when they are per-package, do
  not change.
- For multi-package repositories, use one component tag per package, for example
  `dakit_core-v1.2.3`; never try to publish directly from a main-branch push.

## Complete checklist after a repository rename

1. Get canonical names:
   `gh api users/<owner>/repos --paginate --jq '.[].name'`.
2. Update stale names in `fleet.yaml`.
3. Update PyPI / pub.dev trusted publishers to the canonical repository name.
4. Push code, then run `fleet-audit` to regenerate `status.json` and snapshots.
5. Verify with the next real release. Old CHANGELOG / README links are redirected
   by GitHub and are optional text cleanup, not a release gate.
