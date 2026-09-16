"""The release-health contract, asserted from the same files as Go.

`testdata/health/cases.json` and `testdata/health/patterns.json` are shared with
`internal/health`'s tests. They are the parity mechanism: the two languages
cannot drift about health values, reason codes, asset gaps, or the pattern
language without one of the two CI lanes failing.

The expectations in those files are written from the contract in docs/health.md,
not generated from either implementation, so agreeing with them means agreeing
with the contract rather than with each other.
"""

import json
import unittest
from pathlib import Path

from release_infra import health
from release_infra.policy import PolicyError, validate_asset_pattern, validate_policy

ROOT = Path(__file__).resolve().parents[1]
CASES = ROOT / "testdata" / "health" / "cases.json"
PATTERNS = ROOT / "testdata" / "health" / "patterns.json"

#: Every field a case's observation must state, so no case depends on a
#: language's zero value.
OBSERVATION_FIELDS = {
    "release", "draft_release", "tag_drift", "assets", "run_conclusion",
    "has_policy", "policy_parsable", "archived", "fork", "unmanaged",
    "api_error", "dependency_blocked", "credential_missing", "needs_review",
}


class SharedCasesTest(unittest.TestCase):
    def setUp(self):
        self.document = json.loads(CASES.read_text())
        self.cases = self.document["cases"]
        self.assertTrue(self.cases, "cases.json has no cases")
        self.assertGreaterEqual(len(self.cases), 20, "cases.json looks truncated")

    def test_every_case_states_every_field(self):
        # Go's zero values differ from Python's defaults, so a case that omitted
        # a field would be free to mean two different things.
        for case in self.cases:
            with self.subTest(case=case["name"]):
                self.assertEqual(set(case["observation"]), OBSERVATION_FIELDS)

    def test_every_policy_in_the_fixture_is_valid(self):
        for case in self.cases:
            with self.subTest(case=case["name"]):
                validate_policy(case["policy"])

    def test_assessments_match_the_contract(self):
        for case in self.cases:
            with self.subTest(case=case["name"]):
                observation = health.Observation(**case["observation"])
                result = health.evaluate(case["policy"], observation)
                expect = case["expect"]
                self.assertEqual(result.value, expect["health"])
                self.assertEqual(list(result.codes), expect["reasons"])
                self.assertEqual(
                    list(result.missing_assets), expect.get("missing_assets", [])
                )
                self.assertEqual(
                    list(result.empty_assets), expect.get("empty_assets", [])
                )
                self.assertEqual(result.ok, not expect["reasons"])
                if result.ok:
                    self.assertEqual(result.reasons, ())

    def test_health_values_are_the_published_vocabulary(self):
        for case in self.cases:
            with self.subTest(case=case["name"]):
                self.assertIn(case["expect"]["health"], health.VALUES)

    def test_reason_codes_are_the_published_vocabulary(self):
        for case in self.cases:
            for code in case["expect"]["reasons"]:
                with self.subTest(case=case["name"], code=code):
                    self.assertIn(code, health.CODES)


class SharedPatternsTest(unittest.TestCase):
    def setUp(self):
        self.document = json.loads(PATTERNS.read_text())

    def test_the_language_accepts_what_it_claims_to(self):
        for pattern in self.document["valid"]:
            with self.subTest(pattern=pattern):
                validate_asset_pattern(pattern)

    def test_the_language_rejects_everything_else(self):
        for entry in self.document["invalid"]:
            with self.subTest(pattern=entry["pattern"]):
                with self.assertRaises(PolicyError):
                    validate_asset_pattern(entry["pattern"])

    def test_python_matches_every_shared_row(self):
        # Go asserts the same rows against path.Match. This is the pair of checks
        # that makes "the two engines agree inside this language" a fact.
        rows = self.document["matches"]
        self.assertTrue(rows, "patterns.json has no match rows")
        import fnmatch

        for row in rows:
            validate_asset_pattern(row["pattern"])
            for name, want in row["matches"].items():
                with self.subTest(pattern=row["pattern"], name=name):
                    self.assertNotIn("/", name, "GitHub asset names cannot contain /")
                    self.assertEqual(fnmatch.fnmatch(name, row["pattern"]), want)

    def test_a_policy_with_an_unsupported_pattern_is_invalid(self):
        policy = {
            "kind": "binary",
            "versioning": {"mode": "manual"},
            "assets": {"required": ["app-[0-9].tgz"]},
        }
        with self.assertRaises(PolicyError) as caught:
            validate_policy(policy)
        self.assertIn("reserved character", str(caught.exception))

    def test_repository_policies_use_only_the_supported_language(self):
        # The restriction is only free if nothing in the wild leaves it.
        import base64
        import subprocess

        fleet = (ROOT / "fleet.yaml").read_text()
        import re

        repos = re.findall(r"-\s*name:\s*(redtidev1918/\S+)", fleet)
        self.assertTrue(repos, "fleet.yaml lists no repositories")
        checked = 0
        for repo in repos:
            raw = subprocess.run(
                ["gh", "api", f"repos/{repo}/contents/.release-policy.yml"],
                capture_output=True, text=True,
            )
            if raw.returncode != 0:
                continue
            policy = json.loads(base64.b64decode(json.loads(raw.stdout)["content"]).decode())
            for pattern in policy.get("assets", {}).get("required", []) or []:
                with self.subTest(repo=repo, pattern=pattern):
                    validate_asset_pattern(pattern)
                checked += 1
        if checked == 0:
            self.skipTest("no policies were readable (gh unavailable?)")


class UnusablePolicyTest(unittest.TestCase):
    def test_an_assembled_policy_with_a_bad_pattern_is_broken(self):
        # Go asserts the identical outcome: BROKEN + policy_unparsable. A policy
        # loaded from disk cannot reach this, because loading validates.
        policy = {
            "kind": "binary",
            "versioning": {"mode": "manual"},
            "assets": {"required": ["app-[0-9].tgz"]},
        }
        result = health.evaluate(
            policy,
            health.Observation(release="v1.0.0", has_policy=True, policy_parsable=True),
        )
        self.assertEqual(result.value, health.BROKEN)
        self.assertEqual(result.codes, (health.CODE_POLICY_UNPARSABLE,))


if __name__ == "__main__":
    unittest.main()
