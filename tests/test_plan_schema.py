"""Plan contract: the JSON `releasegraph plan --graph` publishes.

The graph plan is consumed in production by
`.github/workflows/reusable-readonly-plan.yml`, so its shape is a contract, not
an implementation detail. This file pins it three ways:

1. a committed fixture produced by the real binary must satisfy
   `schemas/plan-v1.json`;
2. the schema must still agree with the Go types that produce it;
3. the schema must be *narrow*: `releasegraph plan --path` emits a different
   payload that shares the property name `blocked` with a different type, and
   that payload must be rejected rather than silently accepted.

There is no JSON-Schema library in this repository's dependencies, so the small
validator below covers exactly the keywords the schemas use. It is deliberately
strict about unknown keywords: if a schema grows a keyword this validator does
not implement, the check would silently pass, so `test_validator_rejects_unknown_keywords`
fails instead.
"""

import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PLAN_SCHEMA = ROOT / "schemas" / "plan-v1.json"
HEALTH_SCHEMA = ROOT / "schemas" / "health-v1.json"
FIXTURE = ROOT / "testdata" / "plan" / "graph-plan.json"
STATE_FIXTURE = ROOT / "testdata" / "graph" / "diamond-health.json"
GO_VIEW = ROOT / "internal" / "plan" / "plan.go"
GO_ENVELOPE = ROOT / "internal" / "cli" / "cli.go"

#: Keywords the validator implements. Anything else in a schema is an error.
SUPPORTED = {
    "$schema", "$id", "$defs", "$ref", "title", "description",
    "type", "required", "properties", "additionalProperties", "items",
    "enum", "const", "minLength", "propertyNames",
}


def validate(instance, schema, root, path="$"):
    """Return a list of human-readable violations. Empty means valid."""
    unknown = set(schema) - SUPPORTED
    if unknown:
        raise AssertionError(f"{path}: unimplemented schema keyword(s): {sorted(unknown)}")

    if "$ref" in schema:
        target = root
        for part in schema["$ref"][2:].split("/"):
            target = target[part]
        return validate(instance, target, root, path)

    errors = []
    if "const" in schema and instance != schema["const"]:
        errors.append(f"{path}: expected {schema['const']!r}, got {instance!r}")
    if "enum" in schema and instance not in schema["enum"]:
        errors.append(f"{path}: {instance!r} is not one of {schema['enum']}")

    kind = schema.get("type")
    if kind == "object":
        if not isinstance(instance, dict):
            return errors + [f"{path}: expected object, got {type(instance).__name__}"]
        for key in schema.get("required", []):
            if key not in instance:
                errors.append(f"{path}: missing required {key!r}")
        if isinstance(schema.get("propertyNames"), dict):
            for key in instance:
                errors += validate(key, schema["propertyNames"], root, f"{path}<key>")
        properties = schema.get("properties", {})
        extra = schema.get("additionalProperties", True)
        for key, value in instance.items():
            if key in properties:
                errors += validate(value, properties[key], root, f"{path}.{key}")
            elif extra is False:
                errors.append(f"{path}: unexpected property {key!r}")
            elif isinstance(extra, dict):
                errors += validate(value, extra, root, f"{path}.{key}")
    elif kind == "array":
        if not isinstance(instance, list):
            return errors + [f"{path}: expected array, got {type(instance).__name__}"]
        if isinstance(schema.get("items"), dict):
            for index, value in enumerate(instance):
                errors += validate(value, schema["items"], root, f"{path}[{index}]")
    elif kind == "string":
        if not isinstance(instance, str):
            errors.append(f"{path}: expected string, got {type(instance).__name__}")
        elif len(instance) < schema.get("minLength", 0):
            errors.append(f"{path}: shorter than minLength {schema['minLength']}")
    elif kind in ("integer", "number"):
        # bool is a subclass of int in Python, and `true` is not a number here.
        if isinstance(instance, bool) or not isinstance(instance, (int, float)):
            errors.append(f"{path}: expected {kind}, got {type(instance).__name__}")
        elif kind == "integer" and not isinstance(instance, int):
            errors.append(f"{path}: expected integer, got {instance!r}")
    elif kind == "boolean":
        if not isinstance(instance, bool):
            errors.append(f"{path}: expected boolean, got {type(instance).__name__}")
    elif kind is not None:
        raise AssertionError(f"{path}: validator does not implement type {kind!r}")
    return errors


def load(path):
    return json.loads(path.read_text())


def go_struct_tags(source, struct_name):
    """Map JSON tag -> Go field name for one struct literal."""
    block = re.search(
        rf"type {struct_name} struct \{{(.*?)\n\}}", source, re.S
    ).group(1)
    return {
        tag: field
        for field, tag in re.findall(r"(\w+)\s+[^\n`]*`json:\"([^,\"]+)", block)
    }


class ValidatorTest(unittest.TestCase):
    def test_validator_rejects_unknown_keywords(self):
        with self.assertRaises(AssertionError):
            validate({}, {"type": "object", "notAKeyword": 1}, {})

    def test_validator_actually_catches_violations(self):
        schema = {
            "type": "object",
            "required": ["a"],
            "properties": {"a": {"type": "integer"}},
            "additionalProperties": False,
        }
        self.assertEqual(validate({"a": 1}, schema, schema), [])
        self.assertTrue(validate({}, schema, schema), "missing required key must fail")
        self.assertTrue(validate({"a": 1, "b": 2}, schema, schema), "extra key must fail")
        self.assertTrue(validate({"a": "x"}, schema, schema), "wrong type must fail")
        self.assertTrue(validate({"a": 1}, {"type": "string"}, {}))


class GraphPlanFixturesTest(unittest.TestCase):
    def test_committed_fixture_satisfies_the_schema(self):
        schema = load(PLAN_SCHEMA)
        fixture = load(FIXTURE)
        self.assertEqual(validate(fixture, schema, schema), [])

    def test_fixture_is_the_real_shape_not_an_invention(self):
        fixture = load(FIXTURE)
        self.assertEqual(fixture["schemaVersion"], 1)
        self.assertEqual(sorted(fixture["data"]), ["blocked", "noop", "ready"])
        self.assertIsInstance(fixture["data"]["blocked"], dict)
        for blocked_by in fixture["data"]["blocked"].values():
            self.assertIsInstance(blocked_by, list)

    def test_state_input_fixture_uses_the_shared_health_vocabulary(self):
        schema = load(PLAN_SCHEMA)
        state = load(STATE_FIXTURE)
        self.assertEqual(validate(state, schema["$defs"]["stateInput"], schema), [])
        for project, value in state.items():
            self.assertIn(value, schema["$defs"]["healthValue"]["enum"], project)

    def test_state_input_rejects_a_value_from_another_vocabulary(self):
        schema = load(PLAN_SCHEMA)
        errors = validate({"core": "OK"}, schema["$defs"]["stateInput"], schema)
        self.assertTrue(errors, "an unknown health value must not validate")


class RepositoryPlanIsOutOfScopeTest(unittest.TestCase):
    """`plan --path` is a different payload. The schema must say so by rejecting it."""

    def test_repository_plan_shape_is_rejected(self):
        schema = load(PLAN_SCHEMA)
        payload = {
            "schemaVersion": 1,
            "releasegraphVersion": "0.1.0-go-readonly",
            "generatedAt": "2026-01-01T00:00:00Z",
            "data": {
                "ready": ["local"],
                "blocked": [],
                "noop": [],
                "nodes": [{"id": "local", "kind": "release", "health": "READY"}],
            },
        }
        errors = validate(payload, schema, schema)
        self.assertTrue(errors, "the repository plan shape must not validate as a graph plan")
        self.assertTrue(any("blocked" in error or "nodes" in error for error in errors), errors)

    def test_schema_documents_the_other_shape_by_name(self):
        description = load(PLAN_SCHEMA)["description"]
        self.assertIn("--path", description)
        self.assertIn("nodes", description)


class SchemaAgreesWithGoTest(unittest.TestCase):
    def test_data_keys_match_the_view_struct(self):
        schema = load(PLAN_SCHEMA)
        tags = set(go_struct_tags(GO_VIEW.read_text(), "View"))
        self.assertEqual(set(schema["$defs"]["graphView"]["properties"]), tags)
        self.assertEqual(set(schema["$defs"]["graphView"]["required"]), tags)

    def test_blocked_is_a_map_because_the_go_struct_says_so(self):
        source = GO_VIEW.read_text()
        blocked = re.search(r"Blocked\s+([^\s`]+)", source).group(1)
        self.assertEqual(blocked, "map[string][]string")
        schema = load(PLAN_SCHEMA)
        self.assertEqual(
            schema["$defs"]["graphView"]["properties"]["blocked"]["type"], "object"
        )

    def test_envelope_properties_come_from_the_go_envelope(self):
        schema = load(PLAN_SCHEMA)
        tags = set(go_struct_tags(GO_ENVELOPE.read_text(), "Envelope"))
        self.assertEqual(set(schema["properties"]), tags)
        for name in schema["required"]:
            self.assertIn(name, tags)

    def test_error_field_is_declared_but_never_emitted(self):
        # The schema keeps `error` because the Go type declares it, and says
        # plainly that nothing sets it. If a command starts emitting it, this
        # fails and the prose has to change with the code.
        source = (ROOT / "internal" / "cli").glob("*.go")
        assignments = [
            line
            for path in source
            for line in path.read_text().splitlines()
            if re.search(r"\bError:\s*&?ErrorBody\{", line)
        ]
        self.assertEqual(assignments, [], f"error envelope is now emitted: {assignments}")
        self.assertIn("NEVER emitted", load(PLAN_SCHEMA)["properties"]["error"]["description"])

    def test_state_input_takes_the_node_vocabulary_from_go(self):
        # The state file describes a node's current state, so it accepts every
        # domain.Health value -- including READY, which the diamond fixture
        # actually uses and which repository health deliberately excludes.
        model = (ROOT / "internal" / "domain" / "model.go").read_text()
        go_values = re.findall(r'Health\w+\s+Health = "([A-Z_]+)"', model)
        self.assertEqual(load(PLAN_SCHEMA)["$defs"]["healthValue"]["enum"], go_values)

    def test_repo_health_is_a_strict_subset_of_the_node_vocabulary(self):
        node = set(load(PLAN_SCHEMA)["$defs"]["healthValue"]["enum"])
        repo = set(load(HEALTH_SCHEMA)["properties"]["health"]["enum"])
        self.assertTrue(repo <= node, f"repo health not covered: {repo - node}")
        self.assertTrue(node - repo, "the node vocabulary must be strictly wider")
        self.assertIn("READY", node - repo)

    def test_readonly_plan_workflow_still_passes_graph(self):
        # The consumer this schema exists for. If it stops using --graph, the
        # schema's stated scope is wrong.
        workflow = (ROOT / ".github" / "workflows" / "reusable-readonly-plan.yml").read_text()
        self.assertIn("plan --graph", workflow)
        self.assertIn('--output json', workflow)


if __name__ == "__main__":
    unittest.main()
