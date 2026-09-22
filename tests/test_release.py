import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from release_infra import release


class ReleaseTest(unittest.TestCase):
    def setUp(self):
        # plan() probes git for tag drift; keep tests hermetic unless a test
        # overrides these itself (nested patches win).
        self._git_patches = [
            mock.patch.object(release, "_run", return_value="head-commit"),
            mock.patch.object(release, "_remote_tag_commit", return_value=None),
        ]
        for patch in self._git_patches:
            patch.start()
            self.addCleanup(patch.stop)

    def test_manual_recovery_targets_same_version(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value={"isDraft": True, "assets": []}):
                result = release.plan(str(path), repair=True)
        self.assertEqual(result["version"], "9.9.9")
        self.assertEqual(result["tag"], "v9.9.9")
        self.assertEqual(result["run_release"], "1")

    def test_plan_reports_healthy_public_release(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app-*.tgz"]}, "registries": {"github": {"required": True}}}
        release_info = {
            "isDraft": False,
            "assets": [{"name": "app-1.2.3.tgz", "size": 1}, {"name": "SHA256SUMS", "size": 1}, {"name": "RELEASE-METADATA.json", "size": 1}],
        }
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=release_info):
                result = release.plan(str(path), repair=True)
        self.assertEqual(result["release_health"], "healthy")
        self.assertEqual(result["run_release"], "1")

    def test_historical_tag_drift_is_not_republished(self):
        policy = {"kind": "container", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": []}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None), mock.patch.object(release, "_run", return_value="new"), mock.patch.object(release, "_remote_tag_commit", return_value="old"):
                result = release.plan(str(path), repair=True)
        self.assertEqual(result["release_health"], "tag-drift")
        self.assertEqual(result["run_release"], "0")

    def test_force_allows_dry_run_build_validation_for_tag_drift(self):
        policy = {"kind": "container", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": []}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None), mock.patch.object(release, "_run", return_value="new"), mock.patch.object(release, "_remote_tag_commit", return_value="old"):
                result = release.plan(str(path), force=True)
        self.assertEqual(result["release_health"], "tag-drift")
        self.assertEqual(result["run_release"], "1")

    def test_policy_matrix_is_exported_for_github_actions(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "build": {"matrix": [{"runner": "ubuntu-latest", "command": "make linux"}, {"runner": "windows-latest", "command": "make windows"}]}, "assets": {"required": ["linux", "windows.exe"]}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None):
                result = release.plan(str(path))
        self.assertEqual(json.loads(result["build_matrix"])["include"][1]["runner"], "windows-latest")

    def test_no_download_assets_skips_checksum_but_keeps_metadata(self):
        policy = {"kind": "container", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": []}, "registries": {"github": {"required": True}, "ghcr": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None):
                result = release.plan(str(path))
        self.assertEqual(result["has_assets"], "0")
        self.assertEqual(result["checksums_enabled"], "false")

    def test_toolchain_signals_are_exposed(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            (Path(directory) / "go.mod").write_text("module app\n\ngo 1.23\n")
            previous = Path.cwd()
            try:
                import os
                os.chdir(directory)
                with mock.patch.object(release, "_release", return_value=None):
                    result = release.plan(str(path))
            finally:
                os.chdir(previous)
        self.assertEqual(result["needs_go"], "1")
        self.assertEqual(result["go_version"], "1.23")

    def test_nested_flutter_workspace_requests_flutter(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            package = Path(directory) / "packages/app"
            package.mkdir(parents=True)
            (package / "pubspec.yaml").write_text("environment:\n  sdk: flutter\n")
            previous = Path.cwd()
            try:
                import os
                os.chdir(directory)
                with mock.patch.object(release, "_release", return_value=None):
                    result = release.plan(str(path))
            finally:
                os.chdir(previous)
        self.assertEqual(result["needs_flutter"], "1")

    def test_android_project_requests_java(self):
        policy = {"kind": "flutter", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": []}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            (Path(directory) / "android").mkdir()
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            previous = Path.cwd()
            try:
                import os
                os.chdir(directory)
                with mock.patch.object(release, "_release", return_value=None):
                    result = release.plan(str(path))
            finally:
                os.chdir(previous)
        self.assertEqual(result["needs_java"], "1")

    def test_ghcr_channel_is_exposed_without_custom_publish_command(self):
        policy = {"kind": "hybrid", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}, "ghcr": {"required": False, "image": "ghcr.io/owner/app", "platforms": "linux/amd64,linux/arm64"}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None):
                result = release.plan(str(path))
        self.assertEqual(result["ghcr_enabled"], "true")
        self.assertEqual(result["ghcr_required"], "false")
        self.assertEqual(result["ghcr_image"], "ghcr.io/owner/app")

    def test_npm_registry_requests_the_node_toolchain(self):
        # A registry entry is a channel, not a command to be parsed. The plan has
        # to state it, because `npx --yes npm@11 publish` never contains the
        # substring "npm publish".
        policy = {"kind": "hybrid", "versioning": {"mode": "manual", "version": "1.2.3"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}, "npm": {"required": True, "publish": "npx --yes npm@11 publish --provenance", "verify": "npm view app@$RELEASE_VERSION version"}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None):
                result = release.plan(str(path))
        self.assertEqual(result["npm_publish_enabled"], "1")

    def test_github_only_policy_does_not_request_the_node_toolchain(self):
        policy = {"kind": "hybrid", "versioning": {"mode": "manual", "version": "1.2.3"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None):
                result = release.plan(str(path))
        self.assertEqual(result["npm_publish_enabled"], "0")

    def test_incomplete_public_release_waits_for_explicit_repair(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        public = {"isDraft": False, "assets": [{"name": "app", "size": 1}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=public):
                waiting = release.plan(str(path))
                repairing = release.plan(str(path), repair=True)
        self.assertEqual(waiting["run_release"], "0")
        self.assertEqual(waiting["needs_repair"], "true")
        self.assertEqual(repairing["run_release"], "1")

    def test_public_metadata_asset_is_preserved_during_repair(self):
        asset = Path("RELEASE-METADATA.json")
        current = {"isDraft": False, "assets": [{"name": asset.name, "size": 1}]}
        with mock.patch.object(release, "_release", return_value=current), \
                mock.patch.object(release, "_run") as run:
            release._upload_idempotent("v1.2.3", [asset])

        commands = [" ".join(call.args[0]) for call in run.call_args_list]
        self.assertFalse(any("release download" in command for command in commands))
        self.assertFalse(any("release upload" in command for command in commands))

    def test_retention_deletes_release_objects_without_tags(self):
        # Deleting published stable releases is opt-in: they are history, and the
        # default keeps them. This fixture exercises the opt-in path.
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "2.0.0"}, "assets": {"required": []}, "registries": {"github": {"required": True}}, "retention": {"stable": 1, "prerelease": 0, "pruneStable": True}}
        rows = [{"tagName": "v2", "isDraft": False, "isPrerelease": False}, {"tagName": "v1", "isDraft": False, "isPrerelease": False}, {"tagName": "v3-rc", "isDraft": False, "isPrerelease": True}]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_run", side_effect=[json.dumps(rows), "", ""]) as run:
                release.prune(str(path))
        commands = [call.args[0] for call in run.call_args_list]
        self.assertIn(["gh", "release", "delete", "v1", "--yes"], commands)
        self.assertNotIn("--cleanup-tag", " ".join(sum(commands, [])))

    def test_publish_leaves_label_acknowledgement_to_the_provider(self):
        """publish() must not be a second writer of provider-derived label state."""
        self.assertFalse(hasattr(release, "reconcile_release_labels"))
        policy = {
            "kind": "binary",
            "versioning": {"mode": "manual", "version": "1.2.3"},
            "assets": {"required": [], "optional": []},
            "registries": {"github": {"required": True}},
            "retention": {"stable": 1, "prerelease": 0, "failed_draft": 0},
        }
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "collect_assets", return_value=[]), \
                    mock.patch.object(release, "_upload_idempotent"), \
                    mock.patch.object(release, "_release", return_value={"isDraft": True, "assets": []}), \
                    mock.patch.object(release, "_run", return_value="") as run, \
                    mock.patch.object(release, "audit"), \
                    mock.patch.object(release, "prune"):
                release.publish(str(path))
        commands = [" ".join(call.args[0]) for call in run.call_args_list]
        self.assertFalse(any("pr edit" in command for command in commands))
        self.assertFalse(any("label" in command for command in commands))

    def test_stage_dry_run_never_creates_tag_or_release(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".release-policy.yml").write_text(json.dumps(policy))
            (root / "dist/release").mkdir(parents=True)
            (root / "dist/release/app").write_text("ok")
            previous = Path.cwd()
            with mock.patch.object(release, "_run", return_value="abc"), mock.patch.object(release, "_remote_tag_commit") as remote, mock.patch.object(release, "_release"), mock.patch.object(release, "_upload_idempotent") as upload, mock.patch("os.getcwd", return_value=directory):
                try:
                    import os
                    os.chdir(directory)
                    release.stage(dry_run=True)
                finally:
                    os.chdir(previous)
        remote.assert_not_called()
        upload.assert_not_called()

    def test_stage_reopens_incomplete_public_release_as_draft(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        public = {"isDraft": False, "assets": []}
        reopened = {"isDraft": True, "assets": []}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".release-policy.yml").write_text(json.dumps(policy))
            (root / "dist/release").mkdir(parents=True)
            (root / "dist/release/app").write_text("ok")
            previous = Path.cwd()
            with mock.patch.object(release, "_release", side_effect=[public, reopened]) as fetch, \
                    mock.patch.object(release, "_upload_idempotent") as upload, \
                    mock.patch("os.getcwd", return_value=directory):
                try:
                    import os
                    os.chdir(directory)
                    release.stage()
                finally:
                    os.chdir(previous)
        commands = [call.args[0] for call in release._run.call_args_list]
        self.assertIn(["gh", "release", "edit", "v9.9.9", "--draft"], commands)
        self.assertEqual(fetch.call_count, 2)
        upload.assert_called_once()

    def test_stage_refuses_complete_public_release(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["app"]}, "registries": {"github": {"required": True}}}
        public = {
            "isDraft": False,
            "assets": [
                {"name": "app", "size": 1},
                {"name": "SHA256SUMS", "size": 1},
                {"name": "RELEASE-METADATA.json", "size": 1},
            ],
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / ".release-policy.yml").write_text(json.dumps(policy))
            (root / "dist/release").mkdir(parents=True)
            (root / "dist/release/app").write_text("ok")
            previous = Path.cwd()
            with mock.patch.object(release, "_release", return_value=public), \
                    mock.patch.object(release, "_upload_idempotent") as upload, \
                    mock.patch("os.getcwd", return_value=directory):
                try:
                    import os
                    os.chdir(directory)
                    with self.assertRaisesRegex(release.ReleaseError, "already public and complete"):
                        release.stage()
                finally:
                    os.chdir(previous)
        commands = [call.args[0] for call in release._run.call_args_list]
        self.assertFalse(any(call[:4] == ["gh", "release", "edit", "v9.9.9"] for call in commands))
        upload.assert_not_called()



    def test_plan_pypi_missing_repairs(self):
        policy = {"kind": "python-library", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["*.whl", "*.tar.gz"]}, "registries": {"github": {"required": True}, "pypi": {"required": True, "packages_dir": "dist/pypi"}}}
        release_info = {"isDraft": False, "assets": [{"name": "x-1.2.3-py3-none-any.whl", "size": 1}, {"name": "x-1.2.3.tar.gz", "size": 1}, {"name": "SHA256SUMS", "size": 1}, {"name": "RELEASE-METADATA.json", "size": 1}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=release_info), mock.patch.object(release, "_pypi_status", return_value="missing") as probe:
                result = release.plan(str(path), repair=True)
        self.assertEqual(result["release_health"], "repair")
        self.assertEqual(result["should_publish_pypi"], "1")
        self.assertEqual(result["run_release"], "1")
        probe.assert_called_once()

    def test_plan_pypi_published_is_healthy(self):
        policy = {"kind": "python-library", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["*.whl"]}, "registries": {"github": {"required": True}, "pypi": {"required": True}}}
        release_info = {"isDraft": False, "assets": [{"name": "x-1.2.3-py3-none-any.whl", "size": 1}, {"name": "SHA256SUMS", "size": 1}, {"name": "RELEASE-METADATA.json", "size": 1}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=release_info), mock.patch.object(release, "_pypi_status", return_value="published"):
                result = release.plan(str(path), repair=True)
        self.assertEqual(result["release_health"], "healthy")
        self.assertEqual(result["should_publish_pypi"], "0")
        self.assertEqual(result["pypi_enabled"], "true")

    def test_plan_pypi_unknown_is_partial_and_does_not_publish(self):
        policy = {"kind": "python-library", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["*.whl"]}, "registries": {"github": {"required": True}, "pypi": {"required": True}}}
        release_info = {"isDraft": False, "assets": [{"name": "x-1.2.3-py3-none-any.whl", "size": 1}, {"name": "SHA256SUMS", "size": 1}, {"name": "RELEASE-METADATA.json", "size": 1}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=release_info), mock.patch.object(release, "_pypi_status", return_value="unknown"):
                result = release.plan(str(path), repair=True)
        self.assertEqual(result["release_health"], "partial")
        self.assertEqual(result["should_publish_pypi"], "0")

    def test_plan_pypi_missing_without_repair_waits(self):
        policy = {"kind": "python-library", "versioning": {"mode": "manual", "version": "9.9.9"}, "assets": {"required": ["*.whl"]}, "registries": {"github": {"required": True}, "pypi": {"required": True}}}
        release_info = {"isDraft": False, "assets": [{"name": "x-1.2.3-py3-none-any.whl", "size": 1}, {"name": "SHA256SUMS", "size": 1}, {"name": "RELEASE-METADATA.json", "size": 1}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=release_info), mock.patch.object(release, "_pypi_status", return_value="missing"):
                result = release.plan(str(path))
        self.assertEqual(result["run_release"], "0")
        self.assertEqual(result["should_publish_pypi"], "0")
        self.assertEqual(result["needs_repair"], "true")
    def test_plan_local_git_failure_is_not_swallowed(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "1.2.3"}, "assets": {"required": []}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=None), \
                    mock.patch.object(release, "_run", side_effect=release.ReleaseError("no git")):
                with self.assertRaises(release.ReleaseError):
                    release.plan(str(path))

    def test_retention_prunes_old_drafts_beyond_failed_draft_limit(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "2.0.0"}, "assets": {"required": []}, "registries": {"github": {"required": True}}, "retention": {"stable": 1, "prerelease": 1, "failed_draft": 1}}
        rows = [
            {"tagName": "v-draft-old", "isDraft": True, "isPrerelease": False, "createdAt": "2026-01-01T00:00:00Z"},
            {"tagName": "v1", "isDraft": False, "isPrerelease": False, "publishedAt": "2026-03-01T00:00:00Z"},
            {"tagName": "v-draft-new", "isDraft": True, "isPrerelease": False, "createdAt": "2026-05-01T00:00:00Z"},
        ]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_run", side_effect=[json.dumps(rows), ""]) as run:
                release.prune(str(path))
        deleted = [call.args[0] for call in run.call_args_list if call.args[0][:3] == ["gh", "release", "delete"]]
        self.assertEqual(deleted, [["gh", "release", "delete", "v-draft-old", "--yes"]])

    def test_audit_requires_checksums_to_cover_required_assets(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "1.2.3"}, "assets": {"required": ["app-linux", "app-macos"]}, "registries": {"github": {"required": True}}, "checksums": True}
        public = {"isDraft": False, "isPrerelease": False, "isLatest": True, "assets": [{"name": name, "size": 1} for name in ("app-linux", "app-macos", "SHA256SUMS", "RELEASE-METADATA.json")]}
        env = {"GITHUB_SHA": "head-commit"}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.dict("os.environ", env), \
                    mock.patch.object(release, "_release", return_value=public), \
                    mock.patch.object(release, "_remote_tag_commit", return_value="head-commit"):
                with mock.patch.object(release, "_remote_text", return_value="deadbeef  app-linux\n"):
                    with self.assertRaisesRegex(release.ReleaseError, "app-macos"):
                        release.audit(str(path))
                with mock.patch.object(release, "_remote_text", return_value="a  app-linux\nb  app-macos\n"):
                    release.audit(str(path))




if __name__ == "__main__":
    unittest.main()


class LatestGuardTest(unittest.TestCase):
    """Same-version repair must never move the Latest badge backwards."""

    def test_version_key_orders_numerically(self):
        self.assertLess(release._version_key("2.9.0"), release._version_key("2.16.0"))
        self.assertLess(release._version_key("v2.16.1"), release._version_key("2.17.0"))
        self.assertEqual(release._version_key("2.17.0"), release._version_key("v2.17.0"))

    def test_repairing_older_version_does_not_claim_latest(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "2.16.0"}, "assets": {"required": []}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_is_newest", return_value=False), \
                    mock.patch.object(release, "_upload_idempotent"), \
                    mock.patch.object(release, "_release", return_value={"isDraft": True, "assets": []}), \
                    mock.patch.object(release, "audit"), \
                    mock.patch.object(release, "prune"), \
                    mock.patch.object(release, "_run", return_value="head-commit") as run, \
                    mock.patch.object(release, "collect_assets", return_value=[]):
                release.publish(str(path))
        commands = [" ".join(call.args[0]) for call in run.call_args_list]
        publish = [c for c in commands if c.startswith("gh release edit")]
        self.assertEqual(len(publish), 1, commands)
        # Not merely absent: demanded as false, so the API default cannot move
        # the Latest badge backwards.
        self.assertIn("--latest=false", publish[0])
        self.assertNotIn("--latest ", publish[0] + " ")

    def test_current_version_still_claims_latest(self):
        policy = {"kind": "binary", "versioning": {"mode": "manual", "version": "2.18.0"}, "assets": {"required": []}, "registries": {"github": {"required": True}}}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_is_newest", return_value=True), \
                    mock.patch.object(release, "_upload_idempotent"), \
                    mock.patch.object(release, "_release", return_value={"isDraft": True, "assets": []}), \
                    mock.patch.object(release, "audit"), \
                    mock.patch.object(release, "prune"), \
                    mock.patch.object(release, "_run", return_value="head-commit") as run, \
                    mock.patch.object(release, "collect_assets", return_value=[]):
                release.publish(str(path))
        publish = [" ".join(call.args[0]) for call in run.call_args_list if call.args[0][:3] == ["gh", "release", "edit"]]
        self.assertEqual(len(publish), 1, publish)
        self.assertIn("--latest ", publish[0] + " ")
        self.assertNotIn("--latest=false", publish[0])


class ReleaseContractTest(unittest.TestCase):
    """RELEASE-METADATA.json records the contract in force at publish time, so a
    later policy change cannot retroactively make history unhealthy."""

    def test_capabilities_are_derived_per_repository(self):
        binary = {
            "kind": "binary",
            "build": {"matrix": [{"runner": "ubuntu-latest", "command": "make"}]},
            "assets": {"required": ["app-linux"]},
            "registries": {"github": {"required": True}},
            "checksums": True,
        }
        self.assertEqual(
            release.capabilities(binary),
            {"github_release": True, "binaries": True, "checksums": True,
             "registries": [], "required_assets": ["app-linux"]},
        )

        npm_only = {
            "kind": "node-library",
            "artifacts": {"binaries": {"enabled": False}},
            "assets": {"required": []},
            "registries": {"npm": {"required": True}},
            "checksums": True,
        }
        caps = release.capabilities(npm_only)
        self.assertFalse(caps["binaries"])
        self.assertFalse(caps["checksums"])
        self.assertEqual(caps["registries"], ["npm"])
        self.assertEqual(caps["required_assets"], [])

        source_only = {"kind": "none", "assets": {"required": []}, "registries": {}}
        caps = release.capabilities(source_only)
        self.assertFalse(caps["binaries"])
        self.assertFalse(caps["checksums"])
        self.assertEqual(caps["registries"], [])

    def test_metadata_records_capabilities(self):
        policy = {
            "kind": "python-library",
            "versioning": {"mode": "manual", "version": "1.0.0"},
            "assets": {"required": []},
            "registries": {"pypi": {"required": True}},
            "checksums": True,
            "_hash": "policy-hash",
        }
        metadata = release._metadata(policy, "1.0.0", "v1.0.0", [])
        self.assertFalse(metadata["capabilities"]["binaries"])
        self.assertEqual(metadata["capabilities"]["registries"], ["pypi"])
        self.assertEqual(metadata["policy_schema"], 1)
