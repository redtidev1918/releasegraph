"""ReleaseGraph compatibility metadata.

Every ReleaseGraph release states which interfaces it exposes and which
capabilities it changes, so a rollout can decide who is affected instead of
upgrading the whole fleet for every fix.
"""
from __future__ import annotations

import json
from pathlib import Path

METADATA_ASSET = "RELEASEGRAPH-METADATA.json"
SOURCE = "compatibility.json"


def source(path: str = SOURCE) -> dict:
    raw = json.loads(Path(path).read_text())
    return {
        "workflow_api": int(raw.get("workflow_api", 1)),
        "policy_schema": int(raw.get("policy_schema", 1)),
        "minimum_policy_schema": int(raw.get("minimum_policy_schema", 1)),
    }


def metadata(version: str, affected_capabilities: list[str] | None = None,
             breaking: bool = False, path: str = SOURCE) -> dict:
    """Build the compatibility document for a release.

    affected_capabilities is the honest scope of the change: "binary" for a
    packaging fix, [] only when the change genuinely touches everything.
    """
    document = {"version": version}
    document.update(source(path))
    document["breaking"] = bool(breaking)
    capabilities = sorted({name.strip() for name in (affected_capabilities or []) if name.strip()})
    document["affected_capabilities"] = capabilities
    return document


def write(version: str, directory: str = "dist/release",
          affected_capabilities: list[str] | None = None,
          breaking: bool = False, path: str = SOURCE) -> Path:
    target = Path(directory) / METADATA_ASSET
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(metadata(version, affected_capabilities, breaking, path), indent=2) + "\n")
    return target
