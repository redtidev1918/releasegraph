"""The Python half of the pull-request lifecycle policy.

`releasegraph pr-lifecycle` reads `.release-policy.yml`, and Python reads the
same file for the fleet inventory. Both sides can therefore reject a policy, so
`release_infra.policy.validate_pr_lifecycle` mirrors
`internal/policy.validatePRLifecycle`, and `schemas/release-policy.schema.json`
must describe the same field with the same bounds.

The two invariants that are deliberately not opt-in are the point of most of
these tests: the contract never deletes a branch, and it never closes parked
work without first archiving it into an issue.
"""

import json
import unittest
from pathlib import Path

from release_infra.policy import PolicyError, validate_policy

ROOT = Path(__file__).resolve().parents[1]
SCHEMA = ROOT / "schemas" / "release-policy.schema.json"

POLICY = {
    "kind": "binary",
    "versioning": {"mode": "release-please"},
    "assets": {"required": ["app.zip"]},
    "registries": {"github": {"required": True}},
}

VALID_LIFECYCLE = {
    "parkedAfterDays": 7,
    "exempt": {
        "branches": ["release-please--*"],
        "actors": ["github-actions[bot]", "dependabot[bot]"],
        "labels": ["keep-open"],
    },
}


def with_lifecycle(lifecycle) -> dict:
    return {**POLICY, "repository": {"pullRequests": {"lifecycle": lifecycle}}}


class PRLifecyclePolicyTest(unittest.TestCase):
    def test_a_declared_contract_is_valid(self):
        validate_policy(with_lifecycle(VALID_LIFECYCLE))

    def test_the_contract_never_deletes_a_branch(self):
        with self.assertRaises(PolicyError):
            validate_policy(with_lifecycle({**VALID_LIFECYCLE, "deleteBranch": True}))

    def test_delete_branch_false_is_the_only_accepted_value(self):
        validate_policy(with_lifecycle({**VALID_LIFECYCLE, "deleteBranch": False}))

    def test_the_parked_window_is_bounded(self):
        for value in (0, -1, 366):
            with self.subTest(parkedAfterDays=value):
                with self.assertRaises(PolicyError):
                    validate_policy(with_lifecycle({**VALID_LIFECYCLE, "parkedAfterDays": value}))
        # `True` is an int in Python; a boolean is not a day count.
        with self.assertRaises(PolicyError):
            validate_policy(with_lifecycle({**VALID_LIFECYCLE, "parkedAfterDays": True}))

    def test_closing_parked_work_requires_an_archive_issue(self):
        with self.assertRaises(PolicyError):
            validate_policy(with_lifecycle({
                **VALID_LIFECYCLE, "closeParked": True, "archiveParkedToIssue": False,
            }))

    def test_not_closing_parked_work_needs_no_archive(self):
        validate_policy(with_lifecycle({
            **VALID_LIFECYCLE, "closeParked": False, "archiveParkedToIssue": False,
        }))

    def test_exemption_lists_reject_empty_and_multiline_values(self):
        for field, value in (("branches", [""]), ("actors", [""]), ("labels", ["keep\nopen"])):
            with self.subTest(field=field):
                with self.assertRaises(PolicyError):
                    validate_policy(with_lifecycle({**VALID_LIFECYCLE, "exempt": {field: value}}))

    def test_an_undeclared_contract_stays_valid(self):
        # The dimension is opt-in: a policy that never mentions pull requests
        # must keep loading.
        validate_policy(POLICY)


class PolicySchemaAgreementTest(unittest.TestCase):
    """Pin the schema to the validator this module implements.

    There is no JSON-Schema library in this repository's dependencies, so the
    agreement is asserted on the schema document itself. That is enough to catch
    the failure that matters: a policy the schema permits while Python (and Go)
    reject it, or the reverse.
    """

    def setUp(self):
        schema = json.loads(SCHEMA.read_text())
        self.lifecycle = schema["properties"]["repository"]["properties"]["pullRequests"]["properties"]["lifecycle"]

    def test_the_lifecycle_object_is_closed(self):
        self.assertEqual(self.lifecycle["type"], "object")
        self.assertIs(self.lifecycle["additionalProperties"], False)

    def test_the_schema_pins_the_two_non_optional_invariants(self):
        self.assertIs(self.lifecycle["properties"]["deleteBranch"]["const"], False)
        self.assertIs(self.lifecycle["properties"]["archiveParkedToIssue"]["default"], True)
        self.assertIs(self.lifecycle["properties"]["closeParked"]["default"], True)

    def test_the_schema_bounds_the_parked_window_like_the_validator(self):
        window = self.lifecycle["properties"]["parkedAfterDays"]
        self.assertEqual((window["minimum"], window["maximum"]), (1, 365))
        self.assertEqual(window["default"], 7)

    def test_the_schema_closes_the_exemption_object(self):
        exempt = self.lifecycle["properties"]["exempt"]
        self.assertIs(exempt["additionalProperties"], False)
        self.assertEqual(
            sorted(exempt["properties"]),
            ["actors", "branches", "labels"],
        )


if __name__ == "__main__":
    unittest.main()
