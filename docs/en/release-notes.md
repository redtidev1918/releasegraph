# Release notes (user-facing)

A GitHub Release page is written for the person **downloading a build**, not for the person who merged the pull request. ReleaseGraph generates that page; a business repository never writes or maintains it.

`CHANGELOG.md` may stay the fuller history. The Release body is only the user-facing summary.

## Body structure

Sections appear only when they have content. **Empty sections are omitted.**

| English repository | Chinese repository |
|---|---|
| `What's new` | `新增功能` |
| `Fixes` | `问题修复` |
| `Improvements` | `体验改进` |
| `Breaking changes` | `破坏性变更` |
| `Upgrade notes` | `升级说明` |
| `Downloads` | `下载` |

`Downloads` appears **only when there is something to download**. A service, deploy, container or registry-only repository has no binaries, so the body names the real delivery channel instead (a `ghcr.io/...` image, an npm/PyPI package) rather than leaving a blank download area.

## Language

The language follows the repository's **primary README** — the one GitHub shows on the front page. A clearly Chinese `README.md` selects the Chinese sections; anything else selects English. A mirror such as `README.en.md` does not participate.

To force one, declare it in the business repository's `.release-policy.yml`:

```json
{"release": {"notes": {"language": "zh"}}}
```

Allowed values are `auto` (default), `en`, `zh`.

## Automatic derivation covers 80–90%

ReleaseGraph treats Conventional Commits as an **input signal**, never as the final copy:

- `feat` → new; `fix` / `hotfix` / `bugfix` / `security` / `revert` → fixes; `perf` / `refactor` / `improve` → improvements.
- `chore`, `ci`, `build`, `release`, `docs`, `test`, `deps`, `governance`, `ops`, `infra` — and **any type that is not mapped** — never reach the body.
- Dependency bots (dependabot / renovate / github-actions[bot]) never reach the body.
- Version bumps, merge commits and release-please commits never reach the body.
- Type, scope and PR numbers are stripped: `fix(media): refactor planner (#123)` can never appear verbatim on the page.
- A subject that names automation (`release-infra`, `provider reconciliation`, `branch contract`, `generated metadata`, `governance`, `lockfile`, …) drops the whole entry even when its type looks like `feat`.

## Human override (special releases only)

An ordinary commit costs its author **nothing**. Only special releases need hand-written copy, and there are three ways:

1. **A trailer in the commit body** (the common case):

   ```text
   fix: avoid crash when sending galleries over 10MB

   release-note: Fixed gallery delivery when some images exceeded Telegram's photo size limits.
   ```

   Keys are case-insensitive and may continue on the next indented line:

   | Key | Effect |
   |---|---|
   | `release-note:` | Replaces the generated sentence verbatim (no rewriting) |
   | `release-note-breaking:` | Same, and forces the entry into Breaking changes |
   | `upgrade-note:` | Appends an Upgrade notes entry |

   `release-note:` also rescues a type that would otherwise be filtered out (for example `housekeeping:`), because a human decided it matters.

2. **A whole hand-written body**: drop `.github/release-notes/<version>.md` (or `v<version>.md`, or `current.md`) into the business repository. A match replaces the entire body. If the hand-written body has no `Downloads` section, ReleaseGraph still appends the real asset table.

3. **Fix the language** in the policy, as above.

Preview locally, without releasing:

```bash
cd <business repository>
PYTHONPATH=<releasegraph> python3 -m release_infra.cli notes --path .release-policy.yml --version 1.2.3
```

## Release contract

The generated body is used inside ReleaseGraph's transaction instead of GitHub's `--generate-notes`:

- **Draft first.** The body is written with the draft; artifacts land on the draft and are validated before it is promoted. A repository that declares binary assets is refused at publish time when one is missing, so a "Source code only" page can never become the official release.
- **Latest.** A pre-release — a version with a SemVer pre-release component such as `1.5.0-rc.1`, or `release.prerelease: true` in the policy — never takes the `Latest` badge. Repairing an older version explicitly demands `--latest=false`, so `Latest` can never move backwards.
- **History.** Published stable releases are history and are **kept by default**. They are only recycled when the policy explicitly sets `retention.pruneStable: true`, and the release currently marked `Latest` is never recycled. Pre-releases and failed drafts still follow `retention.prerelease` / `retention.failed_draft`.
