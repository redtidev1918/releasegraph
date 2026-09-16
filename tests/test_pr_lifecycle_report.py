"""The one `PR Lifecycle` field in the fleet dashboard.

`releasegraph pr-lifecycle audit --report pr-lifecycle.json` owns the
classification; `release_infra.inventory` owns the rendered field. These tests
pin the reader side of that contract: what a declared contract looks like, and
what the absence of evidence must degrade to.
"""

import json
import tempfile
import unittest
from pathlib import Path

from release_infra.inventory import write_outputs


class DashboardColumnTest(unittest.TestCase):
    ROW = {
        "repo": "owner/managed",
        "visibility": "public",
        "classification": "managed",
        "health": "HEALTHY",
        "actual_assets": [],
        "latest_release": "v1",
    }

    def _write(self, sidecar):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            if sidecar is not None:
                (root / "pr-lifecycle.json").write_text(json.dumps(sidecar))
            write_outputs([dict(self.ROW)], root)
            return (
                (root / "STATUS.md").read_text(),
                json.loads((root / "status.json").read_text()),
            )

    @staticmethod
    def _row(status: str) -> str:
        """The one data row, so a dash can be attributed to its own column."""
        return next(line for line in status.splitlines() if line.startswith("| owner/managed ")).strip()

    def test_the_column_shows_the_queue_and_what_is_leaving_it(self):
        # "3/5 (-2)" reads as: five open, three still in the merge queue, two
        # about to leave. An open count alone would read as a backlog.
        status, machine = self._write({
            "repositories": [{
                "repository": "owner/managed",
                "status": "evaluated",
                "enabled": True,
                "open": 5,
                "inQueue": 3,
                "shouldLeave": 2,
            }],
        })
        self.assertIn("| PR Lifecycle |", status)
        self.assertEqual(
            self._row(status),
            "| owner/managed | managed | — | v1 | 0 | — | — | 3/5 (-2) | HEALTHY |",
        )
        self.assertEqual(machine["repositories"][0]["prLifecycle"]["shouldLeave"], 2)

    def test_a_quiet_repository_shows_its_whole_queue_as_in_queue(self):
        status, _ = self._write({
            "repositories": [{
                "repository": "owner/managed",
                "status": "evaluated",
                "enabled": True,
                "open": 4,
                "inQueue": 4,
                "shouldLeave": 0,
            }],
        })
        self.assertEqual(
            self._row(status),
            "| owner/managed | managed | — | v1 | 0 | — | — | 4/4 | HEALTHY |",
        )

    def test_an_undeclared_or_disabled_contract_is_not_a_finding(self):
        for outcome in (
            {"status": "undeclared", "enabled": False, "open": 5, "inQueue": 5},
            {"status": "evaluated", "enabled": False, "open": 5, "inQueue": 5},
        ):
            with self.subTest(status=outcome["status"], enabled=outcome["enabled"]):
                status, machine = self._write({"repositories": [{"repository": "owner/managed", **outcome}]})
                self.assertEqual(
                    self._row(status),
                    "| owner/managed | managed | — | v1 | 0 | — | — | — | HEALTHY |",
                )
                self.assertFalse(machine["repositories"][0]["prLifecycle"]["enabled"])

    def test_a_missing_sidecar_degrades_to_not_declared(self):
        # The release dimension must not break because the lifecycle pass was
        # unavailable, and "no evidence" must never render as a queue size.
        status, machine = self._write(None)
        self.assertEqual(
            self._row(status),
            "| owner/managed | managed | — | v1 | 0 | — | — | — | HEALTHY |",
        )
        self.assertIsNone(machine["repositories"][0]["prLifecycle"])

    def test_a_malformed_sidecar_degrades_to_not_declared(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "pr-lifecycle.json").write_text("{not json")
            write_outputs([dict(self.ROW)], root)
            self.assertIn(
                "| owner/managed | managed | — | v1 | 0 | — | — | — | HEALTHY |",
                (root / "STATUS.md").read_text(),
            )


if __name__ == "__main__":
    unittest.main()
