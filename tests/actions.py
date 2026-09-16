"""Post-release actions: resumable, idempotent actions that run after a
release is published.

Mechanism (no persisted state directory):
  * The release BODY carries a machine-readable marker section containing the
    runtime state of every post-release action for that release. Health is
    re-derived from GitHub on every run, exactly like the rest of ReleaseGraph.
  * An action whose state is already ``success`` is skipped (idempotent resume).
  * A ``github-workflow`` action dispatches a workflow_dispatch on the release
    repository (or any repository the token can reach) and polls the created
    run to a terminal conclusion. Bounded by a configurable timeout.
  * ``required: true`` actions must all succeed for the release transaction to
    be considered converged; ``required: false`` failures record a warning and
    do not fail the run.

CLI (via release_infra.cli):
    python3 -m release_infra.cli post-release run    --path .release-policy.yml [--version X]
    python3 -m release_infra.cli post-release status --path .release-policy.yml [--version X]
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

from .policy import desired_version, load_policy
from .retry import retry

MARKER = "releasegraph-post-release"
ACTION_TYPES = {"github-workflow"}
DEFAULT_MAX_WAIT = 30 * 60  # seconds to poll one dispatched workflow run
POLL_INTERVAL = 15


class PostReleaseError(RuntimeError):
    pass


# --- gh interaction (single shell-out point, easy to fake in tests) ---------

def _gh(args: list[str], *, capture: bool = True, retries: bool = True) -> str:
    def invoke() -> str:
        result = subprocess.run(["gh", *args], text=True, capture_output=True)
        if result.returncode:
            detail = (result.stderr.strip() or result.stdout.strip()
                      or f"gh {' '.join(args)} failed")
            raise PostReleaseError(f"gh api failed ({' '.join(args[:2])} ...): {detail}")
        return result.stdout.strip() if capture else ""

    if retries:
        transient = ("timed out", "timeout", "connection reset", "rate limit", "http 5", "secondary rate")
        return retry(invoke, lambda exc: any(t in str(exc).lower() for t in transient))
    return invoke()


def _release_payload(repo: str, tag: str) -> dict:
    return json.loads(_gh(["api", f"repos/{repo}/releases/tags/{tag}", "--jq",
                           "{id, tag_name, body, draft, prerelease, published_at}"]))


def _update_release_body(repo: str, tag: str, body: str) -> None:
    body_path = Path(f".post-release-body-{tag}-{os.getpid()}.md")
    try:
        body_path.write_text(body, encoding="utf-8")
        _gh(["release", "edit", tag, "--repo", repo, "--notes-file", str(body_path)])
    finally:
        body_path.unlink(missing_ok=True)


# --- runtime state (release body marker) -------------------------------------

def parse_marker(body: str) -> dict[str, dict[str, Any]]:
    """Extract per-action runtime state from the release body."""
    m = re.search(rf"<!--\s*{MARKER}\s*(\{{.*?\}})\s*-->", body, re.S)
    if not m:
        return {}
    try:
        data = json.loads(m.group(1))
    except json.JSONDecodeError:
        return {}
    return {str(k): dict(v) for k, v in data.items() if isinstance(v, dict)}


def render_marker(state: dict[str, dict[str, Any]]) -> str:
    return f"<!-- {MARKER} {json.dumps(state, sort_keys=True, ensure_ascii=False)} -->"


def with_marker(body: str, state: dict[str, dict[str, Any]]) -> str:
    """Append or replace the marker section in a release body."""
    marker = render_marker(state)
    if MARKER in body:
        return re.sub(rf"<!--\s*{MARKER}\s*\{{.*?\}}\s*-->", marker, body, flags=re.S)
    return (body.rstrip() + "\n\n" + marker + "\n") if body else marker + "\n"


# --- action definitions (policy) ---------------------------------------------

def load_actions(policy: dict) -> list[dict[str, Any]]:
    raw = policy.get("release", {}).get("postRelease") or []
    actions: list[dict[str, Any]] = []
    seen: set[str] = set()
    for item in raw:
        if not isinstance(item, dict):
            raise PostReleaseError("postRelease entries must be objects")
        action_id = item.get("id")
        if not isinstance(action_id, str) or not action_id:
            raise PostReleaseError("postRelease action needs a non-empty id")
        if action_id in seen:
            raise PostReleaseError(f"postRelease action id duplicated: {action_id}")
        seen.add(action_id)
        atype = item.get("type")
        if atype not in ACTION_TYPES:
            raise PostReleaseError(
                f"postRelease action {action_id}: unknown type {atype!r} (supported: {sorted(ACTION_TYPES)})")
        if atype == "github-workflow" and not item.get("workflow"):
            raise PostReleaseError(f"postRelease action {action_id}: github-workflow needs a workflow")
        inputs = item.get("inputs") or {}
        if not isinstance(inputs, dict):
            raise PostReleaseError(f"postRelease action {action_id}: inputs must be a mapping")
        actions.append({
            "id": action_id,
            "type": atype,
            "required": bool(item.get("required", False)),
            "workflow": item.get("workflow", ""),
            "inputs": {str(k): str(v) for k, v in inputs.items()},
        })
    return actions


def resolve_inputs(action: dict, ctx: dict[str, str]) -> dict[str, str]:
    """Substitute {{tag}} / {{version}} / {{repo}} / {{release_id}} / {{release_url}}."""
    def sub(value: str) -> str:
        return re.sub(r"\{\{(\w+)\}\}", lambda m: ctx.get(m.group(1), m.group(0)), value)
    return {k: sub(v) for k, v in action["inputs"].items()}


# --- github-workflow adapter -------------------------------------------------

def _dispatch_and_wait(repo: str, workflow: str, ref: str, inputs: dict[str, str],
                       head_sha: str, max_wait: int) -> None:
    """Dispatch a workflow_dispatch and poll the correlated run to completion.

    Correlation: right after dispatching, list the runs of that workflow
    triggered by workflow_dispatch on the given head commit and take the most
    recent one. Raises PostReleaseError on dispatch failure, missing run, or a
    run that finishes with a non-success conclusion.
    """
    payload = json.dumps({"ref": ref, "inputs": inputs})
    result = subprocess.run(
        ["gh", "api", "-X", "POST", f"repos/{repo}/actions/workflows/{workflow}/dispatches",
         "--input", "-"],
        input=payload, text=True, capture_output=True)
    if result.returncode:
        raise PostReleaseError(
            f"workflow dispatch failed for {workflow}: "
            f"{result.stderr.strip() or result.stdout.strip()}")

    workflow_id = _gh(["api", f"repos/{repo}/actions/workflows/{workflow}", "--jq", ".id"])
    run_id = None
    deadline = time.monotonic() + max_wait
    while time.monotonic() < deadline:
        runs = json.loads(_gh(["api",
                              f"repos/{repo}/actions/workflows/{workflow_id}/runs",
                              "-f", "per_page=10",
                              "--jq", ".workflow_runs[] | {id, head_sha, created_at, event, status, conclusion}"]))
        candidates = [r for r in runs
                      if r.get("event") == "workflow_dispatch" and r.get("head_sha") == head_sha]
        if candidates:
            run_id = sorted(candidates, key=lambda r: r.get("created_at") or "")[-1]["id"]
            break
        time.sleep(3)
    if run_id is None:
        raise PostReleaseError(f"no workflow run found for {workflow} on {head_sha[:12]} after dispatch")

    print(f"  dispatched {workflow}: run {run_id} (inputs: {inputs})")
    while time.monotonic() < deadline:
        run = json.loads(_gh(["api", f"repos/{repo}/actions/runs/{run_id}",
                             "--jq", "{status, conclusion}"]))
        status, conclusion = run.get("status"), run.get("conclusion")
        if status == "completed":
            if conclusion == "success":
                print(f"  {workflow} run {run_id}: success")
                return
            raise PostReleaseError(f"{workflow} run {run_id} concluded {conclusion}")
        time.sleep(POLL_INTERVAL)
    raise PostReleaseError(f"{workflow} run {run_id} did not finish within {max_wait // 60} minutes")


def run_action(action: dict, ctx: dict[str, str], max_wait: int) -> None:
    if action["type"] == "github-workflow":
        _dispatch_and_wait(ctx["repo"], action["workflow"], ctx["default_branch"],
                           resolve_inputs(action, ctx), ctx["head_sha"], max_wait)
        return
    raise PostReleaseError(f"unimplemented action type: {action['type']}")


# --- engine -------------------------------------------------------------------

def post_release_status(policy: dict, version: str | None) -> tuple[str, dict, dict, dict, dict]:
    """(tag, release, state, actions, ctx) or raise when the release is not public.

    Runs for a release that is not public yet return released=None so callers
    can report "release missing" instead of failing the transaction.
    """
    desired = desired_version(policy, version)
    tag = policy.get("tag", {}).get("template", "v{version}").format(version=desired)
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    if not repo:
        raise PostReleaseError("GITHUB_REPOSITORY is not set; cannot run post-release actions")
    release = _release_payload(repo, tag)
    if release.get("draft"):
        return tag, {"draft": True}, {}, {}, {}
    head_sha = os.environ.get("GITHUB_SHA", "")
    default_branch = _gh(["api", f"repos/{repo}", "--jq", ".default_branch"])
    ctx = {
        "repo": repo,
        "tag": tag,
        "version": desired,
        "release_id": str(release.get("id", "")),
        "release_url": f"https://github.com/{repo}/releases/tag/{tag}",
        "head_sha": head_sha,
        "default_branch": default_branch,
    }
    return tag, release, parse_marker(release.get("body") or ""), load_actions(policy), ctx


def run(policy_path: str = ".release-policy.yml", version: str | None = None, *,
        dry_run: bool = False, max_wait: int = DEFAULT_MAX_WAIT) -> int:
    policy = load_policy(policy_path)
    tag, release, state, actions, ctx = post_release_status(policy, version)
    if release.get("draft") or not release.get("published_at"):
        print(f"post-release: release {tag} is not public yet; nothing to do")
        return 0
    if not actions:
        print("post-release: no actions configured")
        return 0

    new_state: dict[str, dict[str, Any]] = {}
    failed_required = False
    for action in actions:
        action_id = action["id"]
        prev = state.get(action_id, {})
        if prev.get("status") == "success":
            print(f"  {action_id}: already satisfied (skipping)")
            new_state[action_id] = prev
            continue
        attempts = int(prev.get("attempts", 0)) + 1
        if dry_run:
            print(f"  {action_id}: would run ({action['type']}, required={action['required']})")
            new_state[action_id] = {"status": "pending", "attempts": attempts}
            continue
        try:
            print(f"  {action_id}: running ({action['type']} {action.get('workflow')})")
            run_action(action, ctx, max_wait)
            new_state[action_id] = {"status": "success", "attempts": attempts, "last_error": ""}
        except PostReleaseError as exc:
            message = str(exc)[:300]
            new_state[action_id] = {"status": "failed", "attempts": attempts, "last_error": message}
            print(f"  {action_id}: FAILED - {message}", file=sys.stderr)
            if action["required"]:
                failed_required = True
    if dry_run:
        print(f"post-release: dry-run planned for {tag}")
        return 0

    body = release.get("body") or ""
    merged = {k: v for k, v in {**state, **new_state}.items()
              if any(a["id"] == k for a in actions)}
    _update_release_body(ctx["repo"], tag, with_marker(body, merged))

    satisfied = ", ".join(f"{action_id}={new_state.get(action_id, state.get(action_id, {})).get('status')}"
                          for action_id in [a["id"] for a in actions])
    print(f"post-release: {tag} -> {satisfied}")
    return 1 if failed_required else 0


def show_status(policy_path: str = ".release-policy.yml", version: str | None = None) -> int:
    policy = load_policy(policy_path)
    tag, release, state, actions, _ctx = post_release_status(policy, version)
    if release.get("draft"):
        print(f"{tag}: release not public yet")
        return 0
    if not actions:
        print(f"{tag}: no post-release actions configured")
        return 0
    for action in actions:
        st = state.get(action["id"], {})
        status = st.get("status", "pending")
        attempts = st.get("attempts", 0)
        last_error = st.get("last_error", "")
        extra = f" last_error={last_error}" if last_error and status == "failed" else ""
        print(f"{tag}: {action['id']} type={action['type']} required={action['required']} "
              f"status={status} attempts={attempts}{extra}")
    return 0


def health(policy_path: str = ".release-policy.yml", version: str | None = None) -> dict:
    """Machine-readable status used by plan(): absent | pending | satisfied."""
    results = {"post_release_actions": 0, "post_release_health": "absent",
               "post_release_status": ""}
    try:
        policy = load_policy(policy_path)
        tag, release, state, actions, _ = post_release_status(policy, version)
    except PostReleaseError:
        return results
    if release.get("draft"):
        return results
    results["post_release_actions"] = len(actions)
    if not actions:
        return results
    if all(state.get(a["id"], {}).get("status") == "success" for a in actions):
        results["post_release_health"] = "satisfied"
        results["post_release_status"] = ",".join(a["id"] + "=success" for a in actions)
    else:
        results["post_release_health"] = "pending"
        results["post_release_status"] = ",".join(
            f"{a['id']}={state.get(a['id'], {}).get('status', 'pending')}" for a in actions)
    return results