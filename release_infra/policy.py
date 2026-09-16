from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path
from typing import Any

from . import notes


KINDS = {"binary", "python-library", "node-library", "flutter", "android", "container", "hybrid", "none"}
VERSIONING = {"release-please", "manual"}
REGISTRIES = {"github", "pypi", "npm", "pub", "ghcr"}


class PolicyError(ValueError):
    pass


def load_policy(path: str | Path = ".release-policy.yml") -> dict[str, Any]:
    raw = Path(path).read_bytes()
    try:
        policy = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise PolicyError("policy must use JSON syntax (valid YAML 1.2)") from exc
    validate_policy(policy)
    policy["_hash"] = hashlib.sha256(raw).hexdigest()
    return policy


# The asset pattern language.
#
# ReleaseGraph supports literal characters, `*` (any run, including empty) and
# `?` (exactly one character). Everything else is rejected.
#
# The reason is parity, and it is measured rather than assumed. Python's
# `fnmatch` and Go's `path.Match` are NOT the same language: `[!a]` means "not a"
# to fnmatch and "the literal characters ! and a" to path.Match, and `[^a]` means
# the exact opposite pair. CPython 3.14's fnmatch also compiles to atomic groups
# and lookaheads, which Go's RE2 cannot express at all, so porting it is
# impossible. Restricting the language to the region where both engines provably
# agree is what makes the two implementations comparable:
# `testdata/health/patterns.json` pins 484 (pattern, name) pairs that Python and
# Go are both required to match identically.
#
# The restriction costs nothing today: all 48 required patterns across the 16
# managed repositories use only literals and `*`, and none of the 137 released
# asset names contains a reserved character.
RESERVED_PATTERN_CHARS = "\\/[]!^"


def validate_asset_pattern(pattern: str) -> None:
    """Raise PolicyError unless the pattern is inside the supported language."""
    if not pattern:
        raise PolicyError("asset patterns must be non-empty")
    for char in pattern:
        if char in RESERVED_PATTERN_CHARS:
            raise PolicyError(
                f"asset pattern {pattern} uses reserved character {char}; "
                "supported syntax is literals, * and ?"
            )
        if ord(char) < 0x20 or ord(char) == 0x7F:
            raise PolicyError(f"asset pattern {pattern} contains a control character")


def validate_policy(policy: Any) -> None:
    if not isinstance(policy, dict):
        raise PolicyError("policy must be an object")
    if policy.get("kind") not in KINDS:
        raise PolicyError(f"kind must be one of: {', '.join(sorted(KINDS))}")
    versioning = policy.get("versioning")
    if not isinstance(versioning, dict) or versioning.get("mode") not in VERSIONING:
        raise PolicyError("versioning.mode must be release-please or manual")
    assets = policy.get("assets", {})
    for key in ("required", "optional"):
        if not isinstance(assets.get(key, []), list) or not all(isinstance(v, str) and v for v in assets.get(key, [])):
            raise PolicyError(f"assets.{key} must be a list of non-empty strings")
        for pattern in assets.get(key, []):
            validate_asset_pattern(pattern)
    registries = policy.get("registries", {})
    unknown = set(registries) - REGISTRIES
    if unknown:
        raise PolicyError(f"unknown registries: {', '.join(sorted(unknown))}")
    for name, config in registries.items():
        if not isinstance(config, dict) or config.get("required", True) not in (True, False):
            raise PolicyError(f"registries.{name} must be an object with boolean required")
        for command in ("publish", "verify"):
            value = config.get(command, "")
            if not isinstance(value, str) or "\n" in value:
                raise PolicyError(f"registries.{name}.{command} must be a single-line string")
    for name in ("test", "command", "version_check"):
        value = policy.get("build", {}).get(name, "")
        if not isinstance(value, str) or "\n" in value:
            raise PolicyError(f"build.{name} must be a single-line string")
    matrix = policy.get("build", {}).get("matrix")
    if matrix is not None:
        if not isinstance(matrix, list) or not matrix:
            raise PolicyError("build.matrix must be a non-empty array")
        allowed = {"runner", "command", "version_check"}
        for item in matrix:
            if not isinstance(item, dict) or not isinstance(item.get("runner"), str) or not item["runner"]:
                raise PolicyError("each build.matrix item needs a runner string")
            if not isinstance(item.get("command"), str) or "\n" in item["command"]:
                raise PolicyError("each build.matrix item needs a single-line command")
            if set(item) - allowed:
                raise PolicyError(f"unknown build.matrix field(s): {', '.join(sorted(set(item) - allowed))}")
    post_publish = policy.get("release", {}).get("post_publish", "")
    if not isinstance(post_publish, str) or "\n" in post_publish:
        raise PolicyError("release.post_publish must be a single-line string")
    post_release = policy.get("release", {}).get("postRelease")
    if post_release is not None:
        validate_post_release(post_release)
    note_settings = policy.get("release", {}).get("notes", {})
    if not isinstance(note_settings, dict):
        raise PolicyError("release.notes must be an object")
    language = note_settings.get("language", "auto")
    if language not in notes.LANGUAGES:
        raise PolicyError(f"release.notes.language must be one of: {', '.join(notes.LANGUAGES)}")
    retention = policy.get("retention", {})
    if not isinstance(retention, dict):
        raise PolicyError("retention must be an object")
    for name, minimum in (("stable", 1), ("prerelease", 0), ("failed_draft", 0)):
        value = retention.get(name)
        if value is not None and (isinstance(value, bool) or not isinstance(value, int) or value < minimum):
            raise PolicyError(f"retention.{name} must be an integer >= {minimum}")
    if not isinstance(retention.get("pruneStable", False), bool):
        raise PolicyError("retention.pruneStable must be a boolean")
    repository = policy.get("repository")
    if repository is not None:
        if not isinstance(repository, dict):
            raise PolicyError("repository must be an object")
        git = repository.get("git")
        if git is not None:
            if not isinstance(git, dict):
                raise PolicyError("repository.git must be an object")
            production_operations = git.get("productionOperations")
            if production_operations is not None:
                validate_production_operations(production_operations)
        pull_requests = repository.get("pullRequests")
        if pull_requests is not None:
            if not isinstance(pull_requests, dict):
                raise PolicyError("repository.pullRequests must be an object")
            lifecycle = pull_requests.get("lifecycle")
            if lifecycle is not None:
                validate_pr_lifecycle(lifecycle)


def _validate_branch_patterns(field: str, patterns: Any) -> None:
    if not isinstance(patterns, list) or not patterns or not all(isinstance(v, str) and v for v in patterns):
        raise PolicyError(f"{field} must be a non-empty list of non-empty strings")


def validate_pr_lifecycle(lifecycle: Any) -> None:
    """Mirror internal/policy.validatePRLifecycle.

    Two invariants are deliberately not opt-in: the contract never deletes a
    branch, and it never closes parked work without first archiving it into an
    issue. Both are rejected rather than accepted-and-ignored, so a policy that
    says otherwise is a policy error on both sides.
    """
    field = "repository.pullRequests.lifecycle"
    if not isinstance(lifecycle, dict):
        raise PolicyError(f"{field} must be an object")
    if lifecycle.get("deleteBranch") is True:
        raise PolicyError(f"{field}.deleteBranch must be false; the lifecycle contract never deletes a branch")
    parked = lifecycle.get("parkedAfterDays")
    if parked is not None and (isinstance(parked, bool) or not isinstance(parked, int) or not 1 <= parked <= 365):
        raise PolicyError(f"{field}.parkedAfterDays must be between 1 and 365")
    closes = lifecycle.get("closeParked", True) is not False
    archives = lifecycle.get("archiveParkedToIssue", True) is not False
    if closes and not archives:
        raise PolicyError(
            f"{field}.archiveParkedToIssue must be true while parked pull requests are closed; "
            "closing without an issue discards the work"
        )
    exempt = lifecycle.get("exempt", {})
    if not isinstance(exempt, dict):
        raise PolicyError(f"{field}.exempt must be an object")
    # Branch patterns are checked for shape, not for glob syntax: the branch
    # language is validated by the Go core, exactly as it already is for
    # repository.git.productionOperations.branches.
    _validate_string_list(f"{field}.exempt.branches", exempt.get("branches", []))
    _validate_string_list(f"{field}.exempt.actors", exempt.get("actors", []))
    _validate_string_list(f"{field}.exempt.labels", exempt.get("labels", []))


def _validate_string_list(field: str, values: Any) -> None:
    if not isinstance(values, list) or not all(isinstance(v, str) and v and "\n" not in v for v in values):
        raise PolicyError(f"{field} must be a list of non-empty single-line strings")


def validate_production_operations(po: Any) -> None:
    if not isinstance(po, dict):
        raise PolicyError("repository.git.productionOperations must be an object")
    base = po.get("base")
    if not isinstance(base, str) or not base:
        raise PolicyError('repository.git.productionOperations.base must be a non-empty string (branch name or "default")')
    _validate_branch_patterns("repository.git.productionOperations.branches", po.get("branches"))
    require_latest = po.get("requireLatestBase", True)
    if not isinstance(require_latest, bool) or not require_latest:
        raise PolicyError(
            "repository.git.productionOperations.requireLatestBase must be true; "
            "the production-operation contract has no opt-out"
        )
    operations = po.get("operations", {})
    if not isinstance(operations, dict):
        raise PolicyError("repository.git.productionOperations.operations must be an object")
    for name, operation in operations.items():
        field = f"repository.git.productionOperations.operations.{name}"
        if not isinstance(operation, dict):
            raise PolicyError(f"{field} must be an object")
        _validate_branch_patterns(f"{field}.branches", operation.get("branches"))
        allowed_paths = operation.get("allowedPaths", [])
        if not isinstance(allowed_paths, list) or not all(isinstance(v, str) and v for v in allowed_paths):
            raise PolicyError(f"{field}.allowedPaths must be a list of non-empty strings")


def desired_version(policy: dict[str, Any], explicit: str | None = None, root: str | Path = ".") -> str:
    if explicit:
        version = explicit.removeprefix("v")
        if not re.fullmatch(r"[0-9]+(?:\.[0-9A-Za-z-]+)+", version):
            raise PolicyError(f"invalid release version: {explicit}")
        return version
    versioning = policy["versioning"]
    if versioning["mode"] == "manual":
        version = versioning.get("version")
        if not version:
            raise PolicyError("manual versioning requires versioning.version or --version")
        return desired_version({"versioning": {"mode": "manual", "version": None}}, str(version))
    manifest_path = Path(root) / versioning.get("manifest", ".release-please-manifest.json")
    manifest = json.loads(manifest_path.read_text())
    package = versioning.get("package", ".")
    if package not in manifest:
        raise PolicyError(f"manifest has no package {package!r}")
    return desired_version({"versioning": {"mode": "manual", "version": None}}, str(manifest[package]))


def validate_post_release(post_release: Any) -> None:
    """Mirror the policy contract for postRelease actions (see actions.py)."""
    if not isinstance(post_release, list):
        raise PolicyError("release.postRelease must be an array")
    seen: set[str] = set()
    allowed = {"id", "type", "required", "workflow", "inputs", "ref"}
    for item in post_release:
        if not isinstance(item, dict):
            raise PolicyError("each release.postRelease entry must be an object")
        action_id = item.get("id")
        if not isinstance(action_id, str) or not action_id:
            raise PolicyError("release.postRelease action needs a non-empty id")
        if action_id in seen:
            raise PolicyError(f"release.postRelease action id duplicated: {action_id}")
        seen.add(action_id)
        atype = item.get("type")
        if atype not in {"github-workflow"}:
            raise PolicyError(
                f"release.postRelease action {action_id}: unknown type {atype!r} (supported: github-workflow)")
        if atype == "github-workflow" and (not isinstance(item.get("workflow"), str) or not item.get("workflow")):
            raise PolicyError(f"release.postRelease action {action_id}: github-workflow needs a workflow")
        if not isinstance(item.get("required", False), bool):
            raise PolicyError(f"release.postRelease action {action_id}: required must be a boolean")
        ref = item.get("ref", "tag")
        if ref not in ("tag", "default"):
            raise PolicyError(f"release.postRelease action {action_id}: ref must be tag or default")
        unknown = set(item) - allowed
        if unknown:
            raise PolicyError(f"release.postRelease action {action_id}: unknown field(s): {sorted(unknown)}")
        inputs = item.get("inputs")
        if inputs is not None:
            if not isinstance(inputs, dict) or not all(isinstance(k, str) and isinstance(v, str) for k, v in inputs.items()):
                raise PolicyError(f"release.postRelease action {action_id}: inputs must be a string mapping")
