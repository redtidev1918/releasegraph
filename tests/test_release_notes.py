"""The GitHub Release body is a user-facing document.

These fixtures are the contract for `release_infra/notes.py` and for the
publish-time rules around it. They assert what a download user sees, and just as
importantly what they must never see: the automation and repository internals
that today leak onto Release pages (`chore(...)`, `ci:`, `governance`,
`provider reconciliation`, `branch contract`).
"""

import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from release_infra import notes, release

# Strings that name automation or repository internals. A user-facing body must
# never contain any of them, in any fixture.
FORBIDDEN = (
    "chore(release-infra)",
    "chore",
    "governance",
    "provider reconciliation",
    "branch contract",
    "release-infra",
    "dependabot",
    "release-please",
)


def policy(**overrides) -> dict:
    document = {
        "kind": "binary",
        "versioning": {"mode": "manual", "version": "1.0.0"},
        "assets": {"required": [], "optional": []},
        "registries": {"github": {"required": True}},
    }
    document.update(overrides)
    return document


def render(commits, *, language="en", assets=None, prerelease=False, document=None,
           version="1.0.0", tag="v1.0.0") -> str:
    entries = notes.collect_entries(commits, language)
    return notes.render_body(
        document or policy(), version, tag,
        language=language, entries=entries, assets=assets or [], prerelease=prerelease,
    )


class NoNoiseMixin:
    def assertNoNoise(self, body: str):
        lowered = body.lower()
        for forbidden in FORBIDDEN:
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, lowered)
        self.assertIsNone(__import__("re").search(r"\bci\b", lowered), body)


class FeatAndFixReleaseTest(unittest.TestCase, NoNoiseMixin):
    """Fixture 1: feat + fix are populated and translated, never echoed."""

    commits = [
        notes.Commit(subject="feat(gallery): add per-image size preflight before upload"),
        notes.Commit(subject="fix: avoid crash when sending galleries over 10MB"),
        notes.Commit(
            subject="fix(media): refactor planner (#123)",
            body="release-note: Fixed gallery delivery when some images exceeded Telegram's photo size limits.",
        ),
    ]

    def test_english_release_translates_and_separates_sections(self):
        body = render(self.commits, language="en")
        self.assertIn("## What's new", body)
        self.assertIn("- Added per-image size preflight before upload.", body)
        self.assertIn("## Fixes", body)
        self.assertIn("- Fixed crash when sending galleries over 10MB.", body)
        self.assertIn("- Fixed gallery delivery when some images exceeded Telegram's photo size limits.", body)
        self.assertNoNoise(body)

    def test_the_developer_commit_line_never_reaches_the_body(self):
        body = render(self.commits, language="en")
        self.assertNotIn("fix(media)", body)
        self.assertNotIn("(#123)", body)
        self.assertNotIn("refactor planner", body)

    def test_chinese_release_uses_chinese_headings_and_verb_labels(self):
        commits = [
            notes.Commit(subject="feat: 支持按目录批量导出"),
            notes.Commit(subject="fix: 修复发送超过 10MB 图库时的崩溃"),
        ]
        body = render(commits, language="zh")
        self.assertIn("## 新增功能", body)
        self.assertIn("- 新增：按目录批量导出", body)
        self.assertIn("## 问题修复", body)
        self.assertIn("- 修复：发送超过 10MB 图库时的崩溃", body)
        self.assertNotIn("## What's new", body)

    def test_empty_sections_are_omitted(self):
        body = render([notes.Commit(subject="fix: avoid a crash on empty input")], language="en")
        self.assertIn("## Fixes", body)
        self.assertNotIn("## What's new", body)
        self.assertNotIn("## Improvements", body)
        self.assertNotIn("## Breaking changes", body)


class NoiseFilterTest(unittest.TestCase, NoNoiseMixin):
    """Fixture 2: automation commits must not leak into a user-facing body."""

    def test_only_chore_and_ci_commits_produce_a_clean_body(self):
        commits = [
            notes.Commit(subject="chore(release-infra): migrate release protocol to v1"),
            notes.Commit(subject="ci: pin the reusable workflow to a SHA"),
            notes.Commit(subject="ci(egress): manual egress qualification probe"),
            notes.Commit(subject="chore(governance): adopt the pull request lifecycle contract"),
            notes.Commit(subject="chore: regenerate provider state",
                         body="provider reconciliation applied to the fleet"),
            notes.Commit(subject="chore: enforce the branch contract"),
            notes.Commit(subject="chore(main): release 1.7.0", author="github-actions[bot]"),
            notes.Commit(subject="chore(deps): bump lodash from 4.17.20 to 4.17.21",
                         author="dependabot[bot]"),
        ]
        body = render(commits, language="en")
        self.assertNoNoise(body)
        self.assertEqual(notes.collect_entries(commits, "en"), [])
        self.assertIn(notes.STRINGS["en"]["maintenance"], body)
        self.assertNotIn("## What's new", body)
        self.assertNotIn("## Fixes", body)

    def test_an_internal_scope_disqualifies_an_otherwise_user_facing_subject(self):
        commits = [
            notes.Commit(subject="feat(ci): add a release-engineering canary"),
            notes.Commit(subject="fix(governance): stop flaky policy checks"),
            notes.Commit(subject="chore(deps): pin a transitive build tool"),
        ]
        self.assertEqual(notes.collect_entries(commits, "en"), [])

    def test_a_real_infra_module_scope_survives_the_filter(self):
        # Regression: the scope filter split on whitespace and treated the bare
        # words `release`/`infra`/`pipeline`/`bot` as internal, so EVERY scoped
        # feature was dropped and ReleaseGraph v1.5.0 shipped an empty
        # "maintenance" body despite feat(release-notes)/fix(inventory). A scope
        # is only internal when ALL of its hyphen/underscore tokens name pure
        # automation; a real product module must survive.
        for subject in (
            "feat(release-notes): generate user-facing Release bodies",
            "fix(release): select the toolchain from the published channel",
            "fix(inventory): detect consumer callers from their call sites",
            "feat(pipeline): keep a long upload resumable across restarts",
            "feat(pipeline-deps): wire a build-only probe",
        ):
            match = notes.CONVENTIONAL.match(subject)
            self.assertFalse(notes.is_noise(subject, match), subject)

        # The whole pipeline keeps the features, not just is_noise().
        commits = [
            notes.Commit(subject="feat(release-notes): write notes readers can use"),
            notes.Commit(subject="fix(inventory): discover every configured caller"),
        ]
        entries = notes.collect_entries(commits, "en")
        self.assertEqual(
            [entry.text for entry in entries],
            ["Added write notes readers can use.",
             "Fixed discover every configured caller."],
        )

    def test_a_scope_of_only_automation_tokens_is_dropped(self):
        # `deps` is in SCOPE_STOPWORDS and is NOT a NOISE_WORD or NOISE_PHRASE, so
        # this subject is dropped by the scope rule alone — the case that isolates
        # the rule. `ci`/`chore` would instead be caught by NOISE_WORDS, and
        # `release-infra` by the explicit "release infra" NOISE_PHRASE.
        subject = "feat(deps): pin a build-only helper"
        self.assertTrue(notes.is_noise(subject, notes.CONVENTIONAL.match(subject)))
        # A scope that merely CONTAINS an automation token is still user-facing.
        mixed = "feat(pipeline-deps): wire a probe"
        self.assertFalse(notes.is_noise(mixed, notes.CONVENTIONAL.match(mixed)))
        # Documented intent: plumbing scopes keep being removed by the phrase list.
        plumbing = "feat(release-infra): add a rollout canary"
        self.assertTrue(notes.is_noise(plumbing, notes.CONVENTIONAL.match(plumbing)))

    def test_a_real_user_facing_feature_survives_the_filter(self):
        commits = [notes.Commit(subject="feat(cli): add a --dry-run flag to export")]
        entries = notes.collect_entries(commits, "en")
        self.assertEqual([entry.text for entry in entries], ["Added a --dry-run flag to export."])


class BreakingChangeTest(unittest.TestCase, NoNoiseMixin):
    """Fixture 3: a breaking change is explicit and gets an upgrade note."""

    commits = [
        notes.Commit(subject="feat(api)!: drop the legacy /v1/export response envelope"),
        notes.Commit(subject="feat: add the /v2/export endpoint",
                     body="Upgrade-Note: Point export scripts at /v2/export before upgrading."),
    ]

    def test_english_breaking_change_is_explicit(self):
        body = render(self.commits, language="en")
        self.assertIn("## Breaking changes", body)
        self.assertIn("- Added the /v2/export endpoint.", body)
        self.assertIn("## Upgrade notes", body)
        self.assertIn("- Point export scripts at /v2/export before upgrading.", body)
        self.assertNoNoise(body)

    def test_chinese_breaking_change_names_the_upgrade_section(self):
        commits = [
            notes.Commit(subject="feat(api)!: 移除旧版 /v1/export 返回结构"),
        ]
        body = render(commits, language="zh")
        self.assertIn("## 破坏性变更", body)
        self.assertIn("## 升级说明", body)
        self.assertIn(notes.STRINGS["zh"]["breaking_upgrade"], body)

    def test_a_breaking_change_without_a_note_gets_a_generic_upgrade_line(self):
        body = render([notes.Commit(subject="fix!: drop Python 3.9 support")], language="en")
        self.assertIn("## Breaking changes", body)
        self.assertIn("- Fixed drop Python 3.9 support.", body)
        self.assertIn(notes.STRINGS["en"]["breaking_upgrade"], body)


class MaintenanceReleaseTest(unittest.TestCase, NoNoiseMixin):
    """Fixture 4: a release with no user-facing change says exactly that."""

    def test_maintenance_release_still_reports_its_assets(self):
        commits = [
            notes.Commit(subject="chore(deps): bump the actions group with 3 updates"),
            notes.Commit(subject="docs: 补充贡献指南"),
        ]
        assets = notes.make_assets(
            ["app-linux-amd64", "SHA256SUMS"], tag="v1.0.0", repository="acme/app",
            sizes={"app-linux-amd64": 4096 * 1024, "SHA256SUMS": 65},
        )
        body = render(commits, language="en", assets=assets)
        self.assertIn(notes.STRINGS["en"]["maintenance"], body)
        self.assertNotIn("## What's new", body)
        self.assertNotIn("## Fixes", body)
        self.assertNotIn("## Improvements", body)
        self.assertIn("## Downloads", body)
        self.assertIn("`app-linux-amd64`", body)
        self.assertNoNoise(body)

    def test_chinese_maintenance_release_uses_the_chinese_notice(self):
        body = render([notes.Commit(subject="chore: 更新构建镜像")], language="zh")
        self.assertIn(notes.STRINGS["zh"]["maintenance"], body)


class BinaryDownloadsTest(unittest.TestCase, NoNoiseMixin):
    """Fixture 5: a binary repository's Downloads section reflects real assets."""

    document = policy(assets={"required": ["app-linux-amd64", "app-darwin-arm64"], "optional": []})
    assets = notes.make_assets(
        ["app-darwin-arm64", "app-linux-amd64", "RELEASE-METADATA.json", "SHA256SUMS"],
        tag="v2.0.0", repository="acme/app",
        sizes={"app-darwin-arm64": 8 * 1024 * 1024, "app-linux-amd64": 7 * 1024 * 1024,
               "SHA256SUMS": 200, "RELEASE-METADATA.json": 900},
    )

    def test_downloads_lists_the_real_artifacts_only(self):
        body = render([notes.Commit(subject="feat: add a --json flag")], language="en",
                      assets=self.assets, document=self.document, version="2.0.0", tag="v2.0.0")
        self.assertIn("## Downloads", body)
        self.assertIn("| macOS · arm64 | `app-darwin-arm64` | 8.0 MB | "
                      "[Download](https://github.com/acme/app/releases/download/v2.0.0/app-darwin-arm64) |", body)
        self.assertIn("`app-linux-amd64`", body)
        # Metadata and checksums are not something a download user picks.
        self.assertNotIn("| `SHA256SUMS` |", body)
        self.assertNotIn("| `RELEASE-METADATA.json` |", body)
        self.assertIn(notes.STRINGS["en"]["checksums"], body)
        self.assertNoNoise(body)

    def test_chinese_downloads_section_is_chinese(self):
        body = render([notes.Commit(subject="feat: 新增 --json 参数")], language="zh",
                      assets=self.assets, document=self.document, version="2.0.0", tag="v2.0.0")
        self.assertIn("## 下载", body)
        self.assertIn("| 平台 | 文件 | 大小 | 下载 |", body)

    def test_the_gate_reports_every_missing_required_artifact(self):
        self.assertEqual(
            release.missing_required_assets(self.document, []),
            ["app-linux-amd64", "app-darwin-arm64", "SHA256SUMS", "RELEASE-METADATA.json"],
        )
        partial = [{"name": "app-linux-amd64", "size": 10}, {"name": "SHA256SUMS", "size": 10}]
        self.assertEqual(
            release.missing_required_assets(self.document, partial),
            ["app-darwin-arm64", "RELEASE-METADATA.json"],
        )
        complete = [
            {"name": "app-linux-amd64", "size": 10}, {"name": "app-darwin-arm64", "size": 10},
            {"name": "SHA256SUMS", "size": 10}, {"name": "RELEASE-METADATA.json", "size": 10},
        ]
        self.assertEqual(release.missing_required_assets(self.document, complete), [])

    def test_an_empty_asset_does_not_satisfy_the_gate(self):
        # A zero-byte upload is not an artifact; the gate must not accept it.
        zero = [{"name": "app-linux-amd64", "size": 0}]
        self.assertIn("app-linux-amd64", release.missing_required_assets(self.document, zero))

    def test_a_service_repository_has_nothing_to_gate(self):
        self.assertEqual(release.required_asset_patterns(policy(assets={"required": []})), [])
        self.assertEqual(release.missing_required_assets(policy(assets={"required": []}), []), [])


class ServiceOnlyRepositoryTest(unittest.TestCase, NoNoiseMixin):
    """Fixture 6: no binaries means an honest note, never an empty download page."""

    def test_deploy_repository_explains_itself_instead_of_offering_downloads(self):
        document = policy(kind="none", assets={"required": []},
                          registries={"github": {"required": True}, "ghcr": {"required": True, "image": "ghcr.io/acme/deploy"}})
        body = render([notes.Commit(subject="feat: add a Fly Machines execution provider")],
                      language="en", document=document)
        self.assertNotIn("## Downloads", body)
        self.assertIn("ghcr.io/acme/deploy", body)
        self.assertIn("## What's new", body)
        self.assertNoNoise(body)

    def test_registry_only_library_names_its_real_channels(self):
        document = policy(kind="node-library", assets={"required": []},
                          registries={"github": {"required": True}, "npm": {"required": True}})
        body = render([notes.Commit(subject="feat: add a streaming writer")], language="en", document=document)
        self.assertNotIn("## Downloads", body)
        self.assertIn(notes.STRINGS["en"]["channels"].format(channels="npm"), body)

    def test_chinese_service_repository_note_is_chinese(self):
        document = policy(kind="none", assets={"required": []}, registries={"github": {"required": True}})
        body = render([notes.Commit(subject="feat: 新增调度开关")], language="zh", document=document)
        self.assertNotIn("## 下载", body)
        self.assertIn(notes.STRINGS["zh"]["plain_source"], body)


class PrereleaseTest(unittest.TestCase, NoNoiseMixin):
    """Fixture 7: a pre-release is marked as one and never takes Latest."""

    def test_a_semver_prerelease_component_marks_the_release(self):
        self.assertTrue(notes.is_prerelease("1.5.0-rc.1", {}))
        self.assertTrue(notes.is_prerelease("v2.0.0-beta.3", {}))
        self.assertFalse(notes.is_prerelease("1.5.0", {}))
        # The policy flag can force a pre-release but never demote one.
        self.assertTrue(notes.is_prerelease("1.5.0", {"release": {"prerelease": True}}))

    def test_the_body_announces_the_prerelease(self):
        body = render([notes.Commit(subject="feat: add a streaming writer")],
                      language="en", prerelease=True)
        self.assertTrue(body.startswith(notes.STRINGS["en"]["prerelease"]))
        self.assertIn(notes.STRINGS["zh"]["prerelease"],
                      render([notes.Commit(subject="feat: 新增流式写入")], language="zh", prerelease=True))

    def _publish(self, version: str, document: dict, newest: bool = True) -> list[str]:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(document))
            with mock.patch.object(release, "_is_newest", return_value=newest), \
                    mock.patch.object(release, "_release",
                                      return_value={"isDraft": True, "assets": [], "tagName": "v" + version}), \
                    mock.patch.object(release, "_upload_idempotent"), \
                    mock.patch.object(release, "audit"), \
                    mock.patch.object(release, "prune"), \
                    mock.patch.object(release, "collect_assets", return_value=[]), \
                    mock.patch.object(release, "_run", return_value="head-commit") as run:
                release.publish(str(path))
        return [" ".join(call.args[0]) for call in run.call_args_list
                if call.args[0][:3] == ["gh", "release", "edit"]]

    def test_a_prerelease_is_published_as_one_and_claims_no_latest(self):
        document = policy(versioning={"mode": "manual", "version": "1.5.0-rc.1"})
        commands = self._publish("1.5.0-rc.1", document)
        self.assertEqual(len(commands), 1, commands)
        self.assertIn("--prerelease=true", commands[0])
        self.assertNotIn("--latest", commands[0].replace("--latest=false", ""))

    def test_an_older_stable_release_explicitly_refuses_latest(self):
        document = policy(versioning={"mode": "manual", "version": "2.16.0"})
        commands = self._publish("2.16.0", document, newest=False)
        self.assertIn("--prerelease=false", commands[0])
        self.assertIn("--latest=false", commands[0])

    def test_a_stable_release_claims_latest(self):
        document = policy(versioning={"mode": "manual", "version": "2.18.0"})
        commands = self._publish("2.18.0", document)
        self.assertIn("--latest", commands[0])
        self.assertNotIn("--latest=false", commands[0])


class HistoryRetentionTest(unittest.TestCase):
    """Published stable releases are history, not cache."""

    def _prune(self, retention: dict, rows: list[dict]) -> list[list[str]]:
        document = policy(versioning={"mode": "manual", "version": "3.0.0"}, retention=retention)

        def fake_run(command, **_):
            return json.dumps(rows) if command[:3] == ["gh", "release", "list"] else ""

        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(document))
            with mock.patch.object(release, "_run", side_effect=fake_run) as run:
                release.prune(str(path))
        return [call.args[0] for call in run.call_args_list if call.args[0][:3] == ["gh", "release", "delete"]]

    rows = [
        {"tagName": "v3", "isDraft": False, "isPrerelease": False, "isLatest": True, "publishedAt": "2026-06-01T00:00:00Z"},
        {"tagName": "v2", "isDraft": False, "isPrerelease": False, "publishedAt": "2026-03-01T00:00:00Z"},
        {"tagName": "v1", "isDraft": False, "isPrerelease": False, "publishedAt": "2026-01-01T00:00:00Z"},
    ]

    def test_stable_history_is_kept_by_default(self):
        self.assertEqual(self._prune({"stable": 1, "prerelease": 1, "failed_draft": 1}, self.rows), [])

    def test_stable_history_is_only_pruned_on_explicit_opt_in(self):
        deleted = self._prune({"stable": 1, "prerelease": 1, "failed_draft": 1, "pruneStable": True}, self.rows)
        self.assertEqual(deleted, [["gh", "release", "delete", "v2", "--yes"],
                                   ["gh", "release", "delete", "v1", "--yes"]])

    def test_the_latest_release_is_never_pruned_even_when_opted_in(self):
        # Here the release marked Latest is *older* than the others, which is the
        # only case where a retention window would otherwise delete it.
        rows = [
            {"tagName": "v0", "isDraft": False, "isPrerelease": False, "isLatest": True, "publishedAt": "2026-01-01T00:00:00Z"},
            {"tagName": "v1", "isDraft": False, "isPrerelease": False, "publishedAt": "2026-06-01T00:00:00Z"},
            {"tagName": "v-1", "isDraft": False, "isPrerelease": False, "publishedAt": "2025-06-01T00:00:00Z"},
        ]
        deleted = self._prune({"stable": 1, "prerelease": 1, "failed_draft": 1, "pruneStable": True}, rows)
        self.assertEqual(deleted, [["gh", "release", "delete", "v-1", "--yes"]])


class LanguageSelectionTest(unittest.TestCase, NoNoiseMixin):
    """Language follows the repository's primary README, never a hardcode."""

    CHINESE = "# 项目\n\n面向 Telegram 的图库投递工具，支持批量下载与自动重投。\n" * 4
    ENGLISH = "# Project\n\nA gallery delivery tool for Telegram with batch download.\n" * 4

    def _root(self, readme: str) -> str:
        directory = tempfile.mkdtemp()
        (Path(directory) / "README.md").write_text(readme, encoding="utf-8")
        return directory

    def test_chinese_readme_selects_chinese_sections(self):
        body = notes.build_for_release(policy(), "1.0.0", "v1.0.0",
                                       root=self._root(self.CHINESE),
                                       commits=[notes.Commit(subject="fix: 修复空输入崩溃")])
        self.assertIn("## 问题修复", body)
        self.assertNotIn("## Fixes", body)

    def test_english_readme_selects_english_sections(self):
        body = notes.build_for_release(policy(), "1.0.0", "v1.0.0",
                                       root=self._root(self.ENGLISH),
                                       commits=[notes.Commit(subject="fix: avoid a crash on empty input")])
        self.assertIn("## Fixes", body)
        self.assertNotIn("## 问题修复", body)

    def test_an_english_mirror_does_not_change_the_primary_language(self):
        root = self._root(self.CHINESE)
        (Path(root) / "README.en.md").write_text(self.ENGLISH, encoding="utf-8")
        self.assertEqual(notes.detect_language(root), "zh")

    def test_the_policy_can_override_detection(self):
        document = policy(release={"notes": {"language": "en"}})
        body = notes.build_for_release(document, "1.0.0", "v1.0.0",
                                       root=self._root(self.CHINESE),
                                       commits=[notes.Commit(subject="fix: avoid a crash on empty input")])
        self.assertIn("## Fixes", body)

    def test_an_unknown_language_is_a_policy_error(self):
        from release_infra import policy as policy_module

        with self.assertRaises(policy_module.PolicyError):
            policy_module.validate_policy(policy(release={"notes": {"language": "fr"}}))


class HumanOverrideTest(unittest.TestCase):
    """Special releases get hand-written copy; ordinary commits cost nothing."""

    def test_a_release_notes_file_replaces_the_generated_body(self):
        directory = tempfile.mkdtemp()
        (Path(directory) / "README.md").write_text(LanguageSelectionTest.ENGLISH, encoding="utf-8")
        overrides = Path(directory) / ".github/release-notes"
        overrides.mkdir(parents=True)
        (overrides / "2.0.0.md").write_text("## What's new\n\n- Everything changed.\n", encoding="utf-8")
        body = notes.build_for_release(policy(), "2.0.0", "v2.0.0", root=directory,
                                       commits=[notes.Commit(subject="fix: avoid a crash")],
                                       asset_names=["app-linux-amd64"])
        self.assertIn("- Everything changed.", body)
        self.assertNotIn("Fixed crash", body)
        # The download promise still holds even for a hand-written body.
        self.assertIn("## Downloads", body)

    def test_without_an_override_the_commit_range_is_used(self):
        directory = tempfile.mkdtemp()
        (Path(directory) / "README.md").write_text(LanguageSelectionTest.ENGLISH, encoding="utf-8")
        body = notes.build_for_release(policy(), "2.0.0", "v2.0.0", root=directory,
                                       commits=[notes.Commit(subject="fix: avoid a crash on empty input")])
        self.assertIn("- Fixed a crash on empty input.", body)
        self.assertIsNone(notes.override_path("2.0.0", directory))


class TrailerTest(unittest.TestCase):
    def test_trailers_are_parsed_case_insensitively_with_continuations(self):
        fragments = notes.parse_trailers(
            "A longer explanation for the team.\n\n"
            "Release-note: Fixed gallery delivery when some images exceeded\n"
            "  Telegram's photo size limits.\n"
            "Upgrade-Note: Re-run deploy.sh init.\n"
            "Not-A-Trailer: ignored\n"
        )
        self.assertEqual(fragments["note"],
                         "Fixed gallery delivery when some images exceeded Telegram's photo size limits.")
        self.assertEqual(fragments["upgrade"], "Re-run deploy.sh init.")

    def test_an_explicit_fragment_is_used_verbatim(self):
        commit = notes.Commit(
            subject="fix(media): rework the planner",
            body="release-note: Fixed gallery delivery when some images exceeded Telegram's photo size limits.",
        )
        self.assertEqual(
            [entry.text for entry in notes.collect_entries([commit], "en")],
            ["Fixed gallery delivery when some images exceeded Telegram's photo size limits."],
        )

    def test_an_explicit_fragment_rescues_an_unmapped_type(self):
        commit = notes.Commit(subject="housekeeping: tidy the runner pool",
                              body="release-note: Releases are now built 40% faster.")
        entries = notes.collect_entries([commit], "en")
        self.assertEqual([entry.text for entry in entries], ["Releases are now built 40% faster."])


class StageAndPublishWiringTest(unittest.TestCase):
    """The body is attached by the engine, not by `gh --generate-notes`."""

    def test_stage_attaches_a_generated_body_and_never_generate_notes(self):
        document = policy(assets={"required": ["app"], "optional": []})
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(document))
            with mock.patch.object(release, "_run", return_value="head-commit") as run, \
                    mock.patch.object(release, "_remote_tag_commit", return_value="head-commit"), \
                    mock.patch.object(release, "_release", return_value=None), \
                    mock.patch.object(release, "collect_assets", return_value=[Path("dist/release/app")]), \
                    mock.patch.object(release, "write_checksums", return_value=Path("dist/release/SHA256SUMS")), \
                    mock.patch.object(release, "_upload_idempotent"):
                release.stage(str(path))
        commands = [" ".join(call.args[0]) for call in run.call_args_list]
        created = [command for command in commands if command.startswith("gh release create")]
        self.assertEqual(len(created), 1, commands)
        self.assertIn("--notes-file", created[0])
        self.assertNotIn("--generate-notes", " ".join(commands))

    def test_the_written_body_is_user_facing(self):
        document = policy(assets={"required": ["app-linux-amd64"], "optional": []})
        root = tempfile.mkdtemp()
        (Path(root) / "README.md").write_text(LanguageSelectionTest.ENGLISH, encoding="utf-8")
        previous = Path.cwd()
        try:
            import os

            os.chdir(root)
            notes_path = release._notes_file(document, "3.1.0", "v3.1.0", [
                {"name": "app-linux-amd64", "size": 1024 * 1024},
                {"name": "RELEASE-METADATA.json", "size": 512},
            ])
        finally:
            os.chdir(previous)
        try:
            body = Path(notes_path).read_text(encoding="utf-8")
        finally:
            Path(notes_path).unlink(missing_ok=True)
        self.assertIn("## Downloads", body)
        self.assertIn("`app-linux-amd64`", body)
        self.assertNotIn("--generate-notes", body)

    def test_publish_refuses_to_promote_a_draft_without_its_artifacts(self):
        document = policy(assets={"required": ["app-linux-amd64"], "optional": []})
        draft = {"isDraft": True, "tagName": "v1.0.0", "assets": [{"name": "RELEASE-METADATA.json", "size": 10}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(document))
            with mock.patch.object(release, "_release", return_value=draft), \
                    mock.patch.object(release, "_upload_idempotent"), \
                    mock.patch.object(release, "collect_assets", return_value=[]), \
                    mock.patch.object(release, "_run", return_value="") as run:
                with self.assertRaisesRegex(release.ReleaseError, "missing required assets"):
                    release.publish(str(path))
        commands = [" ".join(call.args[0]) for call in run.call_args_list]
        self.assertFalse([command for command in commands if command.startswith("gh release edit")], commands)
        self.assertFalse([command for command in commands if "delete" in command], commands)


class PolicySchemaAgreementTest(unittest.TestCase):
    """The schema must describe what the validators on both sides enforce."""

    SCHEMA = Path(__file__).resolve().parents[1] / "schemas" / "release-policy.schema.json"

    def test_release_notes_is_described_and_closed(self):
        declared = json.loads(self.SCHEMA.read_text())["properties"]["release"]["properties"]["notes"]
        self.assertIs(declared["additionalProperties"], False)
        self.assertEqual(declared["properties"]["language"]["enum"], list(notes.LANGUAGES))
        self.assertEqual(declared["properties"]["language"]["default"], "auto")

    def test_prune_stable_is_described_and_defaults_off(self):
        retention = json.loads(self.SCHEMA.read_text())["properties"]["retention"]["properties"]
        self.assertIs(retention["pruneStable"]["default"], False)
        self.assertEqual(retention["pruneStable"]["type"], "boolean")


class SingleSourceOfTruthTest(unittest.TestCase):
    """The body has exactly one producer, and it is not GitHub's generator."""

    ROOT = Path(__file__).resolve().parent.parent

    def test_no_workflow_or_engine_module_uses_github_generated_notes(self):
        for path in [*self.ROOT.glob(".github/workflows/*.yml"), *self.ROOT.glob("release_infra/*.py")]:
            with self.subTest(path=path.name):
                self.assertFalse("generate-notes" in path.read_text(encoding="utf-8"), path.name)

    def test_release_object_mutations_live_in_one_module(self):
        writers = sorted(
            path.name for path in self.ROOT.glob("release_infra/*.py")
            if '"release", "create"' in path.read_text(encoding="utf-8")
        )
        self.assertEqual(writers, ["release.py"])


    def test_publish_fails_closed_when_the_release_cannot_be_read(self):
        # stage() guarantees the draft exists, so an unreadable release is a
        # transient read failure. Publishing would skip the gate entirely.
        document = policy(assets={"required": ["app-linux-amd64"], "optional": []})
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(document))
            with mock.patch.object(release, "_release", return_value=None), \
                    mock.patch.object(release, "_upload_idempotent"), \
                    mock.patch.object(release, "collect_assets", return_value=[]), \
                    mock.patch.object(release, "_run", return_value="") as run:
                with self.assertRaisesRegex(release.ReleaseError, "cannot read"):
                    release.publish(str(path))
        commands = [" ".join(call.args[0]) for call in run.call_args_list]
        self.assertFalse([command for command in commands if command.startswith("gh release edit")], commands)


class ChineseReleaseBodyConstraintTest(unittest.TestCase):
    """Every user-facing Release body must contain Chinese, regardless of the
    language auto-detection or a hand-written override."""

    def test_english_generated_body_appends_chinese_notice(self):
        with tempfile.TemporaryDirectory() as directory:
            body = notes.build_for_release(
                policy(),
                "1.0.0",
                "v1.0.0",
                root=Path(directory),
                commits=[],
                asset_names=[],
            )
        self.assertIsNotNone(notes.CJK.search(body), body)
        self.assertIn(notes.CHINESE_NOTICE, body)

    def test_english_hand_written_override_appends_chinese_notice(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            override_dir = root / ".github" / "release-notes"
            override_dir.mkdir(parents=True)
            (override_dir / "v1.0.0.md").write_text(
                "English only release body.\n",
                encoding="utf-8",
            )
            body = notes.build_for_release(
                policy(),
                "1.0.0",
                "v1.0.0",
                root=root,
                asset_names=[],
            )
        self.assertIn("English only release body", body)
        self.assertIsNotNone(notes.CJK.search(body), body)
        self.assertIn(notes.CHINESE_NOTICE, body)


if __name__ == "__main__":
    unittest.main()
