"""Release health contract.

Three layers must describe the same state with the same words:
`release_infra/health.py` (fleet inventory), `internal/domain/health.go` (Go
core) and `schemas/health-v1.json` (the published schema). On top of that the
contract must agree with the release planner in `release_infra/release.py`,
whose `release_health` output is a frozen production contract read by
`reusable-release.yml` and by 14 consuming repositories.

So this file does two jobs: it tests the decisions, and it fails loudly when any
of those layers drifts apart.
"""

import json
import re
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from release_infra import health, release

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / "schemas" / "health-v1.json"
GO_MODEL = ROOT / "internal" / "domain" / "model.go"
GO_HEALTH = ROOT / "internal" / "domain" / "health.go"
REUSABLE_WORKFLOW = ROOT / ".github" / "workflows" / "reusable-release.yml"

METADATA_ASSET = {"name": "RELEASE-METADATA.json", "size": 10}
CHECKSUMS_ASSET = {"name": "SHA256SUMS", "size": 10}


def _policy(required=("app-*.tgz",), **overrides):
    policy = {
        "kind": "binary",
        "versioning": {"mode": "manual", "version": "1.2.3"},
        "assets": {"required": list(required)},
        "registries": {"github": {"required": True}},
    }
    policy.update(overrides)
    return policy


class AssetContractTest(unittest.TestCase):
    def test_required_set_matches_the_planner(self):
        # release.py builds: assets.required + RELEASE-METADATA.json
        #                    + SHA256SUMS when checksums are enabled.
        self.assertEqual(
            health.required_assets(_policy()),
            ["app-*.tgz", "RELEASE-METADATA.json", "SHA256SUMS"],
        )

    def test_checksums_disabled_drops_the_checksum_manifest(self):
        self.assertEqual(
            health.required_assets(_policy(checksums=False)),
            ["app-*.tgz", "RELEASE-METADATA.json"],
        )

    def test_no_declared_assets_needs_no_checksum_manifest(self):
        self.assertEqual(
            health.required_assets(_policy(required=())),
            ["RELEASE-METADATA.json"],
        )

    def test_missing_pattern_is_reported(self):
        missing, empty = health.asset_gaps(
            _policy(), [METADATA_ASSET, CHECKSUMS_ASSET]
        )
        self.assertEqual(missing, ["app-*.tgz"])
        self.assertEqual(empty, [])

    def test_zero_byte_asset_counts_as_empty_not_present(self):
        assets = [
            METADATA_ASSET,
            CHECKSUMS_ASSET,
            {"name": "app-1.2.3.tgz", "size": 0},
        ]
        missing, empty = health.asset_gaps(_policy(), assets)
        self.assertEqual(missing, [])
        self.assertEqual(empty, ["app-*.tgz"])

    def test_present_assets_produce_no_gaps(self):
        assets = [METADATA_ASSET, CHECKSUMS_ASSET, {"name": "app-1.2.3.tgz", "size": 5}]
        self.assertEqual(health.asset_gaps(_policy(), assets), ([], []))

    def test_gaps_equal_the_planners_missing_set(self):
        # The planner builds its remote set from assets with size > 0 and then
        # asks `_required_assets_present`. Contract and planner must agree on
        # every shape of release, including the degenerate ones.
        cases = [
            [METADATA_ASSET, CHECKSUMS_ASSET, {"name": "app-1.2.3.tgz", "size": 5}],
            [METADATA_ASSET, CHECKSUMS_ASSET],
            [METADATA_ASSET],
            [],
            [{"name": "app-1.2.3.tgz", "size": 0}, METADATA_ASSET, CHECKSUMS_ASSET],
        ]
        for assets in cases:
            with self.subTest(assets=assets):
                missing, empty = health.asset_gaps(_policy(), assets)
                not_present = set(missing) | set(empty)
                remote = {a["name"] for a in assets if int(a.get("size") or 0) > 0}
                planner_missing = {
                    pattern
                    for pattern in health.required_assets(_policy())
                    if not release._required_assets_present({pattern}, remote)
                }
                self.assertEqual(
                    not_present,
                    planner_missing,
                    f"contract and planner disagree on {assets}",
                )
                self.assertTrue(
                    release._required_assets_present(
                        set(health.required_assets(_policy())), remote
                    )
                    == (not planner_missing)
                )


class EvaluateTest(unittest.TestCase):
    def evaluate(self, policy=None, **kwargs):
        kwargs.setdefault("release", "v1.2.3")
        kwargs.setdefault("assets", [METADATA_ASSET, CHECKSUMS_ASSET, {"name": "app-1.2.3.tgz", "size": 5}])
        policy = _policy() if policy is None else policy
        kwargs.setdefault("has_policy", True)
        return health.evaluate(policy, health.Observation(**kwargs))

    def test_complete_release_is_healthy_with_no_reasons(self):
        result = self.evaluate()
        self.assertEqual(result.value, health.HEALTHY)
        self.assertEqual(result.reasons, ())
        self.assertTrue(result.ok)

    def test_incomplete_release_is_degraded_with_the_missing_pattern(self):
        result = self.evaluate(assets=[METADATA_ASSET, CHECKSUMS_ASSET])
        self.assertEqual(result.value, health.DEGRADED)
        self.assertEqual(result.codes, (health.CODE_TARGET_ASSETS_MISSING,))
        self.assertEqual(result.missing_assets, ("app-*.tgz",))
        self.assertEqual(result.reasons[0].detail, "app-*.tgz")

    def test_empty_upload_is_reported_separately_from_a_missing_one(self):
        result = self.evaluate(
            assets=[METADATA_ASSET, CHECKSUMS_ASSET, {"name": "app-1.2.3.tgz", "size": 0}]
        )
        self.assertEqual(result.codes, (health.CODE_ASSET_EMPTY,))
        self.assertEqual(result.empty_assets, ("app-*.tgz",))

    def test_absent_release_is_no_release(self):
        result = self.evaluate(release=None)
        self.assertEqual(result.value, health.NO_RELEASE)
        self.assertEqual(result.codes, (health.CODE_RELEASE_ABSENT,))

    def test_draft_only_is_no_release_and_names_the_draft(self):
        result = self.evaluate(release=None, draft_release="v1.2.3")
        self.assertEqual(result.value, health.NO_RELEASE)
        self.assertEqual(result.reasons[0].code, health.CODE_RELEASE_DRAFT)
        self.assertEqual(result.reasons[0].detail, "v1.2.3")

    def test_a_stray_draft_degrades_an_otherwise_healthy_release(self):
        result = self.evaluate(draft_release="v1.2.4")
        self.assertEqual(result.value, health.DEGRADED)
        self.assertEqual(result.codes, (health.CODE_RELEASE_DRAFT,))

    def test_version_drift_is_degraded(self):
        result = self.evaluate(tag_drift=True)
        self.assertEqual(result.codes, (health.CODE_VERSION_DRIFT,))

    def test_failed_release_run_is_degraded(self):
        result = self.evaluate(run_conclusion="failure")
        self.assertEqual(result.codes, (health.CODE_RELEASE_RUN_FAILED,))

    def test_skipped_and_successful_runs_are_accepted(self):
        for conclusion in ("success", "skipped", None):
            with self.subTest(conclusion=conclusion):
                self.assertTrue(self.evaluate(run_conclusion=conclusion).ok)

    def test_unreadable_provider_is_broken_before_anything_else(self):
        result = self.evaluate(api_error="connection reset", unmanaged=True)
        self.assertEqual(result.value, health.BROKEN)
        self.assertEqual(result.reasons[0].detail, "connection reset")

    def test_archived_and_fork_repositories_are_no_release(self):
        archived = self.evaluate(archived=True)
        fork = self.evaluate(fork=True)
        self.assertEqual(archived.value, health.NO_RELEASE)
        self.assertEqual(fork.value, health.NO_RELEASE)
        self.assertEqual(archived.codes, (health.CODE_REPOSITORY_ARCHIVED,))
        self.assertEqual(fork.codes, (health.CODE_REPOSITORY_FORK,))

    def test_unmanaged_repository_skips_the_release_check(self):
        result = self.evaluate(unmanaged=True, release=None)
        self.assertEqual(result.value, health.UNMANAGED)
        self.assertEqual(result.codes, (health.CODE_UNMANAGED_REPO,))

    def test_missing_dependency_or_credential_blocks(self):
        blocked = self.evaluate(dependency_blocked="golang")
        credential = self.evaluate(credential_missing=True)
        self.assertEqual(blocked.value, health.BLOCKED)
        self.assertEqual(blocked.reasons[0].detail, "golang")
        self.assertEqual(credential.codes, (health.CODE_FLEET_CREDENTIAL_REQUIRED,))

    def test_unparsable_policy_is_reported(self):
        result = self.evaluate(policy_parsable=False)
        self.assertEqual(result.value, health.DEGRADED)
        self.assertEqual(result.codes, (health.CODE_POLICY_UNPARSABLE,))

    def test_missing_policy_is_reported(self):
        result = self.evaluate(has_policy=False)
        self.assertEqual(result.codes, (health.CODE_POLICY_ABSENT,))

    def test_manual_review_wins_over_healthy(self):
        result = self.evaluate(needs_review=True)
        self.assertEqual(result.value, health.NEEDS_REVIEW)
        self.assertEqual(result.codes, (health.CODE_MANUAL_REVIEW_REQUIRED,))

    def test_every_non_healthy_result_carries_a_reason(self):
        observations = [
            {"release": None},
            {"release": None, "draft_release": "v1"},
            {"assets": []},
            {"tag_drift": True},
            {"run_conclusion": "failure"},
            {"unmanaged": True},
            {"api_error": "boom"},
            {"archived": True},
            {"fork": True},
            {"needs_review": True},
            {"has_policy": False},
            {"policy_parsable": False},
            {"credential_missing": True},
            {"dependency_blocked": "x"},
        ]
        for kwargs in observations:
            with self.subTest(kwargs=kwargs):
                result = self.evaluate(**kwargs)
                self.assertNotEqual(result.value, health.HEALTHY)
                self.assertTrue(result.reasons, f"{kwargs} produced no reason")
                self.assertIn(result.value, health.VALUES)


class WorkflowContractTest(unittest.TestCase):
    """`release_health` is read by reusable-release.yml and 14 repositories."""

    def test_healthy_maps_to_the_string_the_workflow_compares_against(self):
        self.assertEqual(health.to_release_health(health.HEALTHY), "healthy")

    def test_every_projection_is_a_value_the_planner_can_emit(self):
        for value in health.VALUES:
            with self.subTest(value=value):
                self.assertIn(
                    health.to_release_health(value), health.RELEASE_HEALTH_VALUES
                )

    def test_unknown_values_are_rejected_rather_than_defaulted(self):
        with self.assertRaises(ValueError):
            health.to_release_health("SOMETHING_NEW")
        with self.assertRaises(ValueError):
            health.from_release_health("something-new")

    def test_reverse_projection_keeps_healthy_and_missing_apart(self):
        self.assertEqual(health.from_release_health("healthy"), health.HEALTHY)
        self.assertEqual(health.from_release_health("missing"), health.NO_RELEASE)

    def test_workflow_still_gates_on_the_frozen_vocabulary(self):
        source = REUSABLE_WORKFLOW.read_text()
        self.assertIn("steps.plan.outputs.release_health != 'healthy'", source)
        for literal in ("healthy", "tag-drift", "repair", "missing"):
            self.assertIn(literal, health.RELEASE_HEALTH_VALUES)

    def test_planner_literals_are_covered_by_the_contract(self):
        source = (ROOT / "release_infra" / "release.py").read_text()
        emitted = set(
            re.findall(r'"release_health":\s*(.+)', source)[0].split('"')
        ) & set(health.RELEASE_HEALTH_VALUES)
        self.assertEqual(emitted, set(health.RELEASE_HEALTH_VALUES))


class PlannerParityTest(unittest.TestCase):
    """Drive the real planner and require it to agree with the contract."""

    def setUp(self):
        patches = [
            mock.patch.object(release, "_run", return_value="head-commit"),
            mock.patch.object(release, "_remote_tag_commit", return_value=None),
        ]
        for patch in patches:
            patch.start()
            self.addCleanup(patch.stop)

    def _plan(self, policy, release_info):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / ".release-policy.yml"
            path.write_text(json.dumps(policy))
            with mock.patch.object(release, "_release", return_value=release_info):
                return release.plan(str(path), repair=True)

    def test_planner_health_matches_the_contract_for_every_asset_shape(self):
        complete = [
            {"name": "app-1.2.3.tgz", "size": 5},
            {"name": "RELEASE-METADATA.json", "size": 5},
            {"name": "SHA256SUMS", "size": 5},
        ]
        cases = {
            "complete": complete,
            "missing-binary": [complete[1], complete[2]],
            "empty-binary": [
                {"name": "app-1.2.3.tgz", "size": 0},
                complete[1],
                complete[2],
            ],
            "missing-metadata": [complete[0], complete[2]],
            "missing-checksums": [complete[0], complete[1]],
        }
        for name, assets in cases.items():
            with self.subTest(case=name):
                policy = _policy()
                planned = self._plan(policy, {"isDraft": False, "assets": assets})
                assessed = health.evaluate(
                    policy,
                    health.Observation(
                        release="v1.2.3",
                        assets=[{"name": a["name"], "size": a.get("size")} for a in assets],
                        run_conclusion=None,
                    ),
                )
                self.assertEqual(
                    planned["release_health"],
                    assessed.release_health(),
                    f"{name}: planner said {planned['release_health']}, "
                    f"contract said {assessed.release_health()}",
                )
                self.assertEqual(
                    planned["release_health"] == "healthy", assessed.ok, name
                )


class VocabularyParityTest(unittest.TestCase):
    """One vocabulary, three definitions. None may drift."""

    def _go_domain_values(self):
        source = GO_MODEL.read_text()
        return dict(re.findall(r'(Health\w+)\s+Health = "([A-Z_]+)"', source))

    def _go_repo_health(self):
        source = GO_HEALTH.read_text()
        block = re.search(r"var RepoHealthValues = \[\]Health\{(.*?)\n\}", source, re.S).group(1)
        return [name for name in re.findall(r"(Health\w+)", block)]

    def test_python_go_and_schema_agree_on_the_value_set(self):
        schema = json.loads(SCHEMA.read_text())
        self.assertEqual(list(health.VALUES), schema["properties"]["health"]["enum"])
        go_values = self._go_domain_values()
        go_subset = [go_values[name] for name in self._go_repo_health()]
        self.assertEqual(list(health.VALUES), go_subset)

    def test_reason_codes_agree_across_languages_and_schema(self):
        schema = json.loads(SCHEMA.read_text())
        codes = schema["$defs"]["reason"]["properties"]["code"]["enum"]
        self.assertEqual(list(health.CODES), codes)
        go_codes = re.findall(r'Reason\w+\s+= "(\w+)"', GO_HEALTH.read_text())
        self.assertEqual(list(health.CODES), go_codes)

    def test_schema_shape_matches_the_emitted_payload(self):
        schema = json.loads(SCHEMA.read_text())
        self.assertEqual(schema["required"], ["health", "reasons"])
        self.assertEqual(schema["$defs"]["reason"]["required"], ["code"])
        self.assertFalse(schema["$defs"]["reason"]["additionalProperties"])
        # as_dict() must never emit a field the schema does not declare, and the
        # schema must never promise a field nothing produces.
        declared = set(schema["properties"])
        emitted: set[str] = set()
        observations = [
            health.Observation(release="v1.2.3", assets=[METADATA_ASSET, CHECKSUMS_ASSET, {"name": "app-1.2.3.tgz", "size": 5}]),
            health.Observation(release="v1.2.3", assets=[METADATA_ASSET, CHECKSUMS_ASSET]),
            health.Observation(release="v1.2.3", assets=[METADATA_ASSET, CHECKSUMS_ASSET, {"name": "app-1.2.3.tgz", "size": 0}]),
            health.Observation(release=None),
        ]
        for observation in observations:
            emitted |= set(health.evaluate(_policy(), observation).as_dict())
        self.assertEqual(emitted, declared)

    def test_go_fleet_layer_stays_inside_the_contract(self):
        # internal/fleet is Go's repository-health reporter, the counterpart of
        # release_infra/inventory.py. Whatever it assigns must be a value this
        # contract defines, or `releasegraph fleet` is inventing vocabulary.
        source = (ROOT / "internal" / "fleet" / "fleet.go").read_text()
        assigned = set(re.findall(r"rgdomain\.(Health\w+)", source))
        self.assertTrue(assigned, "fleet.go assigns no health value at all")
        go_values = self._go_domain_values()
        for name in assigned:
            self.assertIn(go_values[name], health.VALUES, f"fleet.go assigns {name}")

    def test_provider_verdict_axis_is_not_folded_into_repo_health(self):
        # provider.Verdict.Health is the drift verdict, and it reports
        # RECOVERABLE — an available action, not a state. If that axis is ever
        # folded into RepoHealthValues, this fails so the decision has to be
        # explicit instead of silent.
        go_values = self._go_domain_values()
        self.assertIn(go_values["HealthDegraded"], health.VALUES)
        self.assertIn(go_values["HealthHealthy"], health.VALUES)
        for name in ("HealthRunning", "HealthWaived", "HealthACKPending", "HealthRecoverable"):
            with self.subTest(name=name):
                self.assertNotIn(go_values[name], health.VALUES)

    def test_health_dict_is_schema_shaped(self):
        result = health.evaluate(_policy(), health.Observation(release=None))
        payload = result.as_dict()
        self.assertEqual(payload["health"], "NO_RELEASE")
        self.assertEqual(payload["reasons"], [{"code": "release_absent"}])
        schema = json.loads(SCHEMA.read_text())
        self.assertIn(payload["health"], schema["properties"]["health"]["enum"])
        for reason in payload["reasons"]:
            self.assertIn(reason["code"], schema["$defs"]["reason"]["properties"]["code"]["enum"])


if __name__ == "__main__":
    unittest.main()
