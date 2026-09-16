import base64
import json
import unittest
from pathlib import Path

from release_infra.github import GitHubError
from release_infra.inventory import _branch_contract_status, _gate_caller_refs


class FakeGH:
    def __init__(self, files: dict[str, str]):
        self.files = files

    def api(self, path: str) -> dict:
        name = path.split("/contents/")[-1]
        if name in self.files:
            return {"encoding": "base64", "content": base64.b64encode(self.files[name].encode()).decode()}
        raise GitHubError(f"404: {name}")


SHA = "3d3c42e5aac5ba805825da76410c181273ba90b1"
CANONICAL = "redtidev1918/releasegraph/.github/workflows/reusable-branch-contract.yml"
CALLER_PATH = ".github/workflows/branch-contract.yml"
DEFINITION_PATH = ".github/workflows/reusable-branch-contract.yml"
DEFINITION = (Path(__file__).resolve().parents[1] / DEFINITION_PATH).read_text()

POLICY = json.dumps(
    {
        "kind": "binary",
        "versioning": {"mode": "manual", "version": "1.0.0"},
        "assets": {"required": []},
        "registries": {},
        "repository": {
            "git": {
                "productionOperations": {
                    "base": "default",
                    "branches": ["chore/cutover-*", "ops/*", "release/*", "hotfix/*"],
                }
            }
        },
    }
)


def caller(ref: str) -> str:
    return f"name: Branch contract\non: [pull_request]\njobs:\n  branch-contract:\n    uses: {CANONICAL}@{ref}\n"


def status(files: dict[str, str], policy: str | None = None) -> dict:
    return _branch_contract_status(
        FakeGH(files), "owner/repo", set(files), json.loads(policy) if policy else None
    )


class BranchContractAuditTest(unittest.TestCase):
    def test_no_policy_no_gate_reports_absent(self):
        status_ = _branch_contract_status(FakeGH({}), "owner/repo", set(), None)
        self.assertEqual(
            status_,
            {"configured": False, "gateInstalled": False, "gatePinned": False, "policyValid": None},
        )

    def test_policy_without_gate(self):
        status_ = _branch_contract_status(FakeGH({}), "owner/repo", set(), json.loads(POLICY))
        self.assertTrue(status_["configured"])
        self.assertFalse(status_["gateInstalled"])
        self.assertFalse(status_["gatePinned"])
        self.assertTrue(status_["policyValid"])

    def test_gate_pinned_by_immutable_sha(self):
        status_ = _branch_contract_status(
            FakeGH({CALLER_PATH: caller(SHA)}), "owner/repo", {CALLER_PATH}, json.loads(POLICY)
        )
        self.assertTrue(status_["gateInstalled"])
        self.assertTrue(status_["gatePinned"])

    def test_gate_on_mutable_ref_is_reported_unpinned(self):
        status_ = _branch_contract_status(
            FakeGH({CALLER_PATH: caller("main")}), "owner/repo", {CALLER_PATH}, None
        )
        self.assertTrue(status_["gateInstalled"])
        self.assertFalse(status_["gatePinned"])

    def test_invalid_operations_report_policy_invalid(self):
        broken = json.loads(POLICY)
        broken["repository"]["git"]["productionOperations"]["requireLatestBase"] = False
        status_ = _branch_contract_status(FakeGH({}), "owner/repo", set(), broken)
        self.assertFalse(status_["policyValid"])

    # --- Detection is structural: what counts as an installation -------------

    def test_a_reusable_definition_is_not_a_consumer_gate(self):
        status_ = status({DEFINITION_PATH: DEFINITION})
        self.assertFalse(status_["gateInstalled"])
        self.assertFalse(status_["gatePinned"])
        self.assertEqual(_gate_caller_refs(DEFINITION), [])

    def test_b_comment_mentioning_the_canonical_workflow_is_not_an_installation(self):
        text = f"name: x\n# uses: {CANONICAL}@<pinned-ref>\non: [pull_request]\n"
        self.assertEqual(_gate_caller_refs(text), [])
        self.assertFalse(status({".github/workflows/ci.yml": text})["gateInstalled"])

    def test_c_docs_mentioning_the_canonical_workflow_are_not_an_installation(self):
        text = f"name: x\non: [pull_request]\n# see docs/governance.md: {CANONICAL}@main\n"
        self.assertFalse(status({".github/workflows/ci.yml": text})["gateInstalled"])

    def test_d_consumer_caller_on_mutable_tag_is_installed_but_not_pinned(self):
        status_ = status({CALLER_PATH: caller("v1")})
        self.assertTrue(status_["gateInstalled"])
        self.assertFalse(status_["gatePinned"])

    def test_e_consumer_caller_on_version_tag_is_installed_but_not_pinned(self):
        status_ = status({CALLER_PATH: caller("v1.4.11")})
        self.assertTrue(status_["gateInstalled"])
        self.assertFalse(status_["gatePinned"])

    def test_f_consumer_caller_on_full_sha_is_installed_and_pinned(self):
        status_ = status({CALLER_PATH: caller(SHA)})
        self.assertTrue(status_["gateInstalled"])
        self.assertTrue(status_["gatePinned"])

    def test_g_unrelated_job_level_reusable_call_does_not_count(self):
        text = (
            "name: x\non: [pull_request]\njobs:\n  release:\n    uses: redtidev1918/releasegraph/"
            f".github/workflows/reusable-release.yml@{SHA}\n"
        )
        self.assertEqual(_gate_caller_refs(text), [])
        self.assertFalse(status({".github/workflows/ci.yml": text})["gateInstalled"])

    def test_h_canonical_path_inside_run_block_or_step_does_not_count(self):
        text = (
            "name: x\non: [pull_request]\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n"
            f"      - uses: {CANONICAL}@{SHA}\n"
            "      - run: |\n"
            f"          uses: {CANONICAL}@{SHA}\n"
        )
        self.assertEqual(_gate_caller_refs(text), [])
        self.assertFalse(status({".github/workflows/ci.yml": text})["gateInstalled"])

    def test_i_yaml_extension_caller_is_detected(self):
        status_ = status({".github/workflows/branch-contract.yaml": caller(SHA)})
        self.assertTrue(status_["gateInstalled"])
        self.assertTrue(status_["gatePinned"])

    def test_j_only_the_workflow_that_calls_the_gate_counts(self):
        files = {
            ".github/workflows/ci.yml": "name: ci\non: [pull_request]\njobs:\n  build:\n    runs-on: ubuntu-latest\n",
            CALLER_PATH: caller(SHA),
            ".github/workflows/release.yml": (
                "name: release\non: [push]\njobs:\n  release:\n    uses: redtidev1918/releasegraph/"
                f".github/workflows/reusable-release.yml@{SHA}\n"
            ),
        }
        status_ = status(files)
        self.assertTrue(status_["gateInstalled"])
        self.assertTrue(status_["gatePinned"])
        self.assertEqual(_gate_caller_refs(files[CALLER_PATH]), [SHA])

    def test_k_releasegraph_cannot_self_detect_its_own_definition(self):
        # The provider ships the definition; it is not a consumer of it. If this
        # ever flips, ReleaseGraph has genuinely installed the gate on itself and
        # the expectation should be changed deliberately, not silently.
        other = "name: ci\non: [pull_request]\njobs:\n  build:\n    runs-on: ubuntu-latest\n"
        status_ = status({DEFINITION_PATH: DEFINITION, ".github/workflows/fleet-audit.yml": other})
        self.assertFalse(status_["gateInstalled"])
        self.assertFalse(status_["gatePinned"])

    def test_partially_pinned_repository_is_not_pinned(self):
        two = (
            "name: Branch contract\non: [pull_request]\njobs:\n"
            f"  a:\n    uses: {CANONICAL}@{SHA}\n"
            f"  b:\n    uses: {CANONICAL}@v1\n"
        )
        self.assertEqual(_gate_caller_refs(two), [SHA, "v1"])
        self.assertTrue(status({CALLER_PATH: two})["gateInstalled"])
        self.assertFalse(status({CALLER_PATH: two})["gatePinned"])


if __name__ == "__main__":
    unittest.main()
