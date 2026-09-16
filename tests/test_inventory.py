import base64
import json
import tempfile
import unittest
from pathlib import Path

from unittest import mock

from release_infra import health
from release_infra.github import GitHubError
from release_infra.inventory import (
    _desired_manifest_version,
    _release_tags,
    _scan_repo,
    scan,
    write_outputs,
)


class InventoryTest(unittest.TestCase):
    def test_release_tags_support_plain_and_component_templates(self):
        self.assertEqual(_release_tags("1.2.3", None), {"1.2.3", "v1.2.3"})
        self.assertIn("dakit_cli-v0.4.1", _release_tags("0.4.1", {"tag": {"template": "dakit_cli-v{version}"}}))

    def test_component_policy_selects_cli_manifest_version(self):
        policy = {"versioning": {"package": "packages/dakit_cli"}}
        manifest = {"packages/dakit_core": "1.0.0", "packages/dakit_cli": "0.4.1"}
        self.assertEqual(_desired_manifest_version(policy, manifest), "0.4.1")

    def test_scan_fails_instead_of_publishing_network_broken_inventory(self):
        source = [{"full_name": "owner/repo", "owner": {"login": "owner"}, "default_branch": "main", "visibility": "public", "archived": False, "fork": False}]
        broken = {"repo": "owner/repo", "health": "BROKEN", "error": "connection reset by peer"}
        with mock.patch("release_infra.inventory.GitHub.api", return_value=source), mock.patch("release_infra.inventory._scan_repo", return_value=broken):
            with self.assertRaises(GitHubError):
                scan("owner", include_private=False)

    def test_dashboard_keeps_every_classification(self):
        rows = [
            {"repo": "owner/managed", "visibility": "public", "classification": "managed", "health": "HEALTHY", "actual_assets": ["app"], "latest_release": "v1"},
            {"repo": "owner/fork", "visibility": "public", "classification": "fork", "health": "NO_RELEASE"},
            {"repo": "owner/none", "visibility": "public", "classification": "no-release", "health": "NO_RELEASE"},
        ]
        with tempfile.TemporaryDirectory() as directory:
            write_outputs(rows, directory)
            status = json.loads((Path(directory) / "status.json").read_text())
            self.assertEqual(len(status["repositories"]), 3)
            self.assertIn("owner/fork", (Path(directory) / "STATUS.md").read_text())
            self.assertTrue((Path(directory) / "migration/snapshots/managed.json").exists())


class _FakeGitHub:
    """The smallest GitHub surface `_scan_repo` touches."""

    def __init__(self, policy, releases):
        self._contents = {".release-policy.yml": json.dumps(policy)}
        self._releases = releases

    def api(self, path):
        if path.startswith("repos/") and "/git/trees/" in path:
            return {"tree": [{"path": ".release-policy.yml", "type": "blob"}]}
        if "/contents/" in path:
            name = path.split("/contents/", 1)[1]
            if name in self._contents:
                encoded = base64.b64encode(self._contents[name].encode()).decode()
                return {"encoding": "base64", "content": encoded}
        raise GitHubError(f"not found: {path}")

    def releases(self, repo):
        return self._releases

    def tags(self, repo):
        return []

    def workflows(self, repo):
        return []

    def runs(self, repo):
        return []


class InventoryHealthTest(unittest.TestCase):
    """The fleet view must apply the shared contract, not re-invent it."""

    policy = {
        "kind": "binary",
        "versioning": {"mode": "manual", "version": "1.2.3"},
        "assets": {"required": ["app-*.tgz"]},
        "registries": {"github": {"required": True}},
    }
    source = {
        "full_name": "owner/repo",
        "owner": {"login": "owner"},
        "default_branch": "main",
        "visibility": "public",
        "archived": False,
        "fork": False,
        "is_template": False,
    }

    def _scan(self, assets):
        release = {
            "tag_name": "v1.2.3",
            "draft": False,
            "prerelease": False,
            "published_at": "2026-01-01T00:00:00Z",
            "assets": assets,
        }
        fake = _FakeGitHub(self.policy, [release])
        with mock.patch("release_infra.inventory.GitHub", return_value=fake):
            return _scan_repo(self.source)

    def test_complete_release_is_healthy(self):
        item = self._scan([
            {"name": "app-1.2.3.tgz", "size": 5},
            {"name": "RELEASE-METADATA.json", "size": 5},
            {"name": "SHA256SUMS", "size": 5},
        ])
        self.assertEqual(item["health"], "HEALTHY")
        self.assertEqual(item["health_reasons"], [])
        self.assertEqual(item["missing_assets"], [])

    def test_incomplete_release_reports_the_missing_patterns(self):
        item = self._scan([{"name": "RELEASE-METADATA.json", "size": 5}])
        self.assertEqual(item["health"], "DEGRADED")
        self.assertEqual(
            [reason["code"] for reason in item["health_reasons"]],
            ["target_assets_missing"],
        )
        self.assertEqual(
            item["missing_assets"], ["app-*.tgz", "SHA256SUMS"],
        )

    def test_zero_byte_asset_is_reported_as_empty(self):
        item = self._scan([
            {"name": "app-1.2.3.tgz", "size": 0},
            {"name": "RELEASE-METADATA.json", "size": 5},
            {"name": "SHA256SUMS", "size": 5},
        ])
        self.assertEqual(item["empty_assets"], ["app-*.tgz"])
        self.assertEqual(item["missing_assets"], [])

    def test_inventory_does_not_claim_the_planners_field(self):
        # `release_health` is the planner's workflow decision, computed from
        # state this inventory never reads. Two owners for one field name is
        # how contradictory dashboards happen.
        item = self._scan([{"name": "RELEASE-METADATA.json", "size": 5}])
        self.assertNotIn("release_health", item)
        self.assertIn(item["health"], health.VALUES)


if __name__ == "__main__":
    unittest.main()
