import json
import tempfile
import unittest
from pathlib import Path

from release_infra import compatibility
from release_infra.assets import collect_assets


class CompatibilityMetadataTest(unittest.TestCase):
    """A ReleaseGraph release declares its own compatibility, so rollout can skip
    repositories this release cannot affect."""

    def test_metadata_records_the_honest_scope(self):
        document = compatibility.metadata("1.5.0", affected_capabilities=["binary", "binary", "checksums"])
        self.assertEqual(document["version"], "1.5.0")
        self.assertEqual(document["affected_capabilities"], ["binary", "checksums"])
        self.assertFalse(document["breaking"])
        for key in ("workflow_api", "policy_schema", "minimum_policy_schema"):
            self.assertIn(key, document)

    def test_empty_scope_is_explicit(self):
        document = compatibility.metadata("1.5.0")
        self.assertEqual(document["affected_capabilities"], [])

    def test_written_asset_is_collectable_by_the_asset_gate(self):
        policy = {
            "assets": {"required": ["app-linux"], "optional": [compatibility.METADATA_ASSET]},
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "app-linux").write_text("binary")
            path = compatibility.write("1.5.0", str(root), affected_capabilities=["binary"])
            self.assertTrue(path.exists())
            collected = collect_assets(policy["assets"]["required"], policy["assets"]["optional"], root)
            self.assertEqual(sorted(p.name for p in collected),
                             sorted(["app-linux", compatibility.METADATA_ASSET]))
            written = json.loads(path.read_text())
            self.assertEqual(written["affected_capabilities"], ["binary"])
