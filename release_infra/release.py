from __future__ import annotations

import datetime as dt
import fnmatch
import json
import os
import re
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from typing import Any
from pathlib import Path

from . import __version__, notes
from .assets import collect_assets, sha256, write_checksums
from .github import GitHubError
from .actions import health as post_release_health
from .policy import desired_version, load_policy
from .retry import retry


class ReleaseError(RuntimeError):
    pass


def _required_assets_present(required: set[str], remote: set[str]) -> bool:
    return all(any(fnmatch.fnmatch(name, pattern) for name in remote) for pattern in required)


def _pypi_status(package: str | None, version: str) -> str:
    """Return 'published', 'missing', or 'unknown' for a PyPI version.

    A network/parse failure returns 'unknown' so callers never treat a
    transient outage as a confirmable missing version and then blind-publish.
    """
    if not package:
        return "unknown"
    url = f"https://pypi.org/pypi/{urllib.parse.quote(package)}/{urllib.parse.quote(version)}/json"
    try:
        with urllib.request.urlopen(url, timeout=15) as response:
            json.load(response)
            return "published"
    except urllib.error.HTTPError as exc:
        return "missing" if exc.code == 404 else "unknown"
    except Exception:
        return "unknown"


def _pypi_package(policy: dict) -> str | None:
    """Derive the PyPI package name from policy, pyproject, or repo name."""
    pypi = policy.get("registries", {}).get("pypi", {})
    if isinstance(pypi, dict) and pypi.get("package"):
        return str(pypi["package"])
    pyproject = Path("pyproject.toml")
    if pyproject.exists():
        try:
            import tomllib
            return tomllib.loads(pyproject.read_text()).get("project", {}).get("name")
        except Exception:
            pass
    repo = os.environ.get("GITHUB_REPOSITORY", "")
    return repo.rsplit("/", 1)[-1] or None


def required_asset_patterns(policy: dict) -> list[str]:
    """Every asset a *public* release must carry. Never empty.

    A repository with no declared binary patterns has nothing to gate beyond the
    release record itself: its Release legitimately contains only metadata, and
    the body says so instead of showing an empty download area. The metadata is
    still mandatory — every managed release carries `RELEASE-METADATA.json`
    regardless of policy (the health contract and `plan()` both require it), so a
    release without it is incomplete even for a policy that declares no binary
    assets. Returning an empty list here made `stage()` treat such a release as
    "already public and complete" and refuse the same-version repair that the
    planner had just asked for.
    """
    patterns = list(policy.get("assets", {}).get("required", []))
    if patterns and policy.get("checksums", True):
        patterns.append("SHA256SUMS")
    patterns.append("RELEASE-METADATA.json")
    return patterns


def missing_required_assets(policy: dict, remote: list[dict]) -> list[str]:
    """Required patterns with no non-empty asset on the release.

    This is the promote-after-artifacts gate. GitHub will happily publish a
    Release that holds nothing but "Source code"; the binary workflow failing
    later then leaves that empty page as the official release.
    """
    present = {str(asset.get("name", "")) for asset in remote if int(asset.get("size", 0)) > 0}
    return [pattern for pattern in required_asset_patterns(policy)
            if not any(fnmatch.fnmatch(name, pattern) for name in present)]



def _run(command: list[str], *, capture: bool = False) -> str:
    def invoke() -> str:
        result = subprocess.run(command, text=True, capture_output=True)
        if result.returncode:
            raise ReleaseError(result.stderr.strip() or result.stdout.strip() or f"command failed: {' '.join(command)}")
        if not capture and result.stdout:
            print(result.stdout, end="")
        return result.stdout.strip() if capture else ""

    transient = ("timed out", "timeout", "connection reset", "temporary failure", "http 5", "rate limit", "secondary rate")
    return retry(invoke, lambda exc: command[0] in {"gh", "git"} and any(text in str(exc).lower() for text in transient))


def _release(tag: str) -> dict | None:
    result = subprocess.run(
        ["gh", "release", "view", tag, "--json", "id,tagName,isDraft,isPrerelease,assets,url"],
        text=True, capture_output=True,
    )
    if result.returncode:
        return None
    release = json.loads(result.stdout)
    latest = subprocess.run(["gh", "release", "view", "--json", "tagName"], text=True, capture_output=True)
    release["isLatest"] = not latest.returncode and json.loads(latest.stdout)["tagName"] == tag
    return release


def _version_key(version: str) -> tuple:
    """Order versions numerically without adding a dependency."""
    parts: list[tuple[int, Any]] = []
    for chunk in re.split(r"[.\-+]", version.removeprefix("v")):
        parts.append((0, int(chunk)) if chunk.isdigit() else (1, chunk))
    return tuple(parts)


def _is_newest(version: str) -> bool:
    """Report whether version is at least as new as the current Latest release.

    Repairing an older version must never move the Latest badge backwards, so
    publish() only passes --latest when this holds.
    """
    result = subprocess.run(["gh", "release", "view", "--json", "tagName"], text=True, capture_output=True)
    if result.returncode:
        return True
    current = json.loads(result.stdout).get("tagName") or ""
    if not current:
        return True
    return _version_key(version) >= _version_key(current)


def _remote_tag_commit(tag: str) -> str | None:
    result = subprocess.run(["git", "ls-remote", "--tags", "origin", f"refs/tags/{tag}^{{}}", f"refs/tags/{tag}"], text=True, capture_output=True, check=True)
    lines = result.stdout.splitlines()
    peeled = next((line.split()[0] for line in lines if line.endswith("^{}")), None)
    return peeled or (lines[0].split()[0] if lines else None)


def _ensure_tag(tag: str, commit: str, *, dry_run: bool = False) -> None:
    remote = _remote_tag_commit(tag)
    if remote and remote != commit:
        raise ReleaseError(f"tag {tag} points to {remote}, expected {commit}")
    if not remote and not dry_run:
        _run(["git", "tag", "-a", tag, commit, "-m", f"Release {tag}"])
        _run(["git", "push", "origin", f"refs/tags/{tag}"])


def _upload_idempotent(tag: str, paths: list[Path], *, dry_run: bool = False) -> None:
    current = _release(tag)
    remote = {asset["name"]: asset for asset in (current or {}).get("assets", [])}
    with tempfile.TemporaryDirectory() as directory:
        for path in paths:
            if path.name in remote:
                # Metadata records the publishing run and container digest, so
                # a repair run naturally regenerates it. Once a release is
                # public, preserve its immutable first publication record.
                if path.name == "RELEASE-METADATA.json" and not (current or {}).get("isDraft", True):
                    continue
                _run(["gh", "release", "download", tag, "--pattern", path.name, "--dir", directory])
                downloaded = Path(directory) / path.name
                if sha256(downloaded) != sha256(path):
                    raise ReleaseError(f"remote asset differs: {path.name}")
                downloaded.unlink()
                continue
            if not dry_run:
                _run(["gh", "release", "upload", tag, str(path)])


def _notes_file(policy: dict, version: str, tag: str, assets: list[dict]) -> Path:
    """Render the user-facing Release body to a temporary file.

    The body is ReleaseGraph's, not GitHub's default generator: that one renders
    merged pull-request titles, which is developer and automation internals.
    See `release_infra/notes.py`.
    """
    body = notes.build_for_release(
        policy, version, tag,
        asset_names=[str(asset.get("name", "")) for asset in assets],
        asset_sizes={str(asset.get("name", "")): int(asset.get("size", 0)) for asset in assets},
    )
    with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False, encoding="utf-8") as handle:
        handle.write(body)
        return Path(handle.name)


def _local_assets(paths: list[Path]) -> list[dict]:
    described = []
    for path in paths:
        try:
            size = path.stat().st_size
        except OSError:
            size = 0
        described.append({"name": path.name, "size": size})
    return described


def _publish_notes(policy: dict, version: str, tag: str, assets: list[Path]) -> None:
    """Create the draft with a user-facing body, never with generated notes."""
    path = _notes_file(policy, version, tag, _local_assets(assets))
    try:
        _run(["gh", "release", "create", tag, "--verify-tag", "--draft", "--title", tag,
              "--notes-file", str(path)])
    finally:
        path.unlink(missing_ok=True)


def stage(policy_path: str = ".release-policy.yml", version: str | None = None, *, dry_run: bool = False) -> str:
    policy = load_policy(policy_path)
    desired = desired_version(policy, version)
    tag = policy.get("tag", {}).get("template", "v{version}").format(version=desired)
    commit = _run(["git", "rev-parse", "HEAD"], capture=True)
    assets = collect_assets(policy.get("assets", {}).get("required", []), policy.get("assets", {}).get("optional", []))
    checksums = write_checksums(assets) if assets and policy.get("checksums", True) else None
    if dry_run:
        return tag
    _ensure_tag(tag, commit, dry_run=dry_run)
    release = _release(tag)
    if release and not release["isDraft"]:
        missing = missing_required_assets(policy, release.get("assets", []))
        if not missing:
            raise ReleaseError(f"{tag} is already public and complete; run audit instead")
        print(f"note: {tag} is public but incomplete; reopening as draft for repair")
        _run(["gh", "release", "edit", tag, "--draft"])
        release = _release(tag)
        if not release or not release["isDraft"]:
            raise ReleaseError(f"could not reopen {tag} as a draft")
    if not release and not dry_run:
        _publish_notes(policy, desired, tag, [*assets, *([checksums] if checksums else [])])
    _upload_idempotent(tag, [*assets, *([checksums] if checksums else [])], dry_run=dry_run)
    return tag


def plan(policy_path: str = ".release-policy.yml", version: str | None = None, *, force: bool = False, repair: bool = False) -> dict[str, str]:
    policy = load_policy(policy_path)
    desired = desired_version(policy, version)
    tag = policy.get("tag", {}).get("template", "v{version}").format(version=desired)
    release = _release(tag)
    required = set(policy.get("assets", {}).get("required", [])) | {"RELEASE-METADATA.json"}
    checksums_enabled = policy.get("checksums", True) and bool(policy.get("assets", {}).get("required"))
    if checksums_enabled:
        required.add("SHA256SUMS")
    remote = {asset["name"] for asset in (release or {}).get("assets", []) if int(asset.get("size", 0)) > 0}
    github_healthy = bool(release and not release["isDraft"] and _required_assets_present(required, remote))
    registries = policy.get("registries", {})
    pypi_enabled = "pypi" in registries
    if pypi_enabled:
        pypi_status = _pypi_status(_pypi_package(policy), desired)
    else:
        pypi_status = "not-configured"
    pypi_published = pypi_status == "published"
    pypi_missing = pypi_status == "missing"
    pypi_unknown = pypi_status == "unknown"
    healthy = bool(github_healthy and (not pypi_enabled or pypi_published))
    needs_repair = bool(release and not release["isDraft"] and not healthy)
    build = policy.get("build", {})
    matrix = build.get("matrix") or [{"runner": "ubuntu-latest", "command": build.get("command", ":"), "version_check": build.get("version_check", "")}]
    pubspecs = [Path("pubspec.yaml"), *Path(".").glob("packages/*/pubspec.yaml")]
    needs_flutter = any(path.exists() and "sdk: flutter" in path.read_text(errors="ignore") for path in pubspecs)
    needs_java = Path("android").is_dir()
    go_version = ""
    if Path("go.mod").exists():
        go_version = next((line.split()[1] for line in Path("go.mod").read_text().splitlines() if line.startswith("go ")), "")
    asset_patterns = policy.get("assets", {})
    ghcr = registries.get("ghcr", {})
    # A local git failure must surface; only a failed remote probe is tolerable
    # (offline CI) and means no drift signal, not a silent "everything is fine".
    head = _run(["git", "rev-parse", "HEAD"], capture=True)
    try:
        tag_commit = _remote_tag_commit(tag)
    except (ReleaseError, subprocess.CalledProcessError):
        tag_commit = None
    tag_drift = bool(tag_commit and tag_commit != head)
    retry_count = 0
    cooldown = False
    if os.environ.get("GITHUB_EVENT_NAME") == "schedule":
        try:
            runs = json.loads(_run(["gh", "run", "list", "--limit", "10", "--json", "conclusion,createdAt,event"], capture=True))
            failures = [run for run in runs if run.get("conclusion") == "failure"]
            retry_count = len(failures)
            if retry_count >= 3:
                last = dt.datetime.fromisoformat(failures[0]["createdAt"].replace("Z", "+00:00"))
                cooldown = dt.datetime.now(dt.UTC) - last < dt.timedelta(hours=6)
        except (ReleaseError, ValueError, KeyError):
            pass
    post_release = policy.get("release", {}).get("postRelease") or []
    post_state = post_release_health(policy_path, desired) if post_release else {
        "post_release_actions": 0, "post_release_health": "absent", "post_release_status": ""}
    post_release_pending = post_state.get("post_release_health") == "pending"
    should_release = force or (not tag_drift and (
        repair or (post_release_pending and not cooldown) or (not healthy and not needs_repair and not cooldown)))
    return {
        "should_release": str(should_release).lower(), "run_release": "1" if should_release else "0",
        "release_health": "healthy" if healthy else ("tag-drift" if tag_drift else ("partial" if pypi_unknown else ("repair" if needs_repair else "missing"))),
        "version": desired, "tag": tag, "retry_count": str(retry_count), "cooldown": str(cooldown).lower(),
        "needs_repair": str(needs_repair).lower(), "tag_drift": str(tag_drift).lower(),
        "test_command": policy.get("build", {}).get("test", ""), "build_command": policy.get("build", {}).get("command", ":"),
        "version_check": policy.get("build", {}).get("version_check", ""), "build_matrix": json.dumps({"include": matrix}, separators=(",", ":")),
        "has_assets": "1" if asset_patterns.get("required") or asset_patterns.get("optional") else "0",
        "checksums_enabled": str(checksums_enabled).lower(),
        "needs_flutter": "1" if needs_flutter else "0", "flutter_version": policy.get("build", {}).get("flutter_version", "3.32.8"),
        "needs_java": "1" if needs_java else "0",
        "needs_go": "1" if go_version else "0", "go_version": go_version,
        "needs_goreleaser": "1" if Path(".goreleaser.yml").exists() or Path(".goreleaser.yaml").exists() else "0",
        "pypi_enabled": str(pypi_enabled).lower(), "pypi_required": str(registries.get("pypi", {}).get("required", True)).lower(),
        "pypi_published": str(pypi_published).lower(), "pypi_status": pypi_status,
        "should_publish_pypi": "1" if (pypi_enabled and pypi_missing and should_release and not cooldown) else "0",
        "pypi_packages_dir": registries.get("pypi", {}).get("packages_dir", "dist/release"),
        "ghcr_enabled": str("ghcr" in registries).lower(), "ghcr_required": str(ghcr.get("required", True)).lower(),
        "ghcr_context": ghcr.get("context", "."), "ghcr_file": ghcr.get("file", "Dockerfile"),
        "ghcr_image": ghcr.get("image", ""), "ghcr_platforms": ghcr.get("platforms", "linux/amd64"),
        "npm_publish_enabled": "1" if registries.get("npm", {}).get("required", True) and registries.get("npm", {}).get("publish") else "0",
        "required_publish": " && ".join(config.get("publish", ":") for name, config in registries.items() if name != "ghcr" and config.get("required", True) and config.get("publish")),
        "required_verify": " && ".join(config.get("verify", ":") for config in registries.values() if config.get("required", True) and config.get("verify")),
        "optional_publish": "; ".join(f"({config['publish']}) || true" for name, config in registries.items() if name != "ghcr" and not config.get("required", True) and config.get("publish")),
        "optional_verify": "; ".join(f"({config['verify']}) || true" for config in registries.values() if not config.get("required", True) and config.get("verify")),
        "post_publish": policy.get("release", {}).get("post_publish", ""),
        "post_release_json": json.dumps(post_release, ensure_ascii=False) if post_release else "",
        "post_release_actions": str(post_state.get("post_release_actions", 0)),
        "post_release_health": post_state.get("post_release_health", "absent"),
        "post_release_status": post_state.get("post_release_status", ""),
    }


def capabilities(policy: dict) -> dict:
    """The release contract in force for this version.

    ReleaseGraph is capability-driven: only what this repository's policy
    requires is part of its health. A registry-only repository has no binaries
    and must never be judged as if it did.
    """
    required_assets = list(policy.get("assets", {}).get("required", []))
    matrix = policy.get("build", {}).get("matrix") or []
    registries = sorted(
        name for name, config in policy.get("registries", {}).items()
        if name != "github" and config.get("required", True)
    )
    release_github = policy.get("release", {}).get("github", True)
    binaries = bool(matrix) or bool(required_assets)
    declared = policy.get("artifacts", {}).get("binaries", {})
    if "enabled" in declared:
        binaries = bool(declared["enabled"])
    return {
        "github_release": bool(release_github),
        "binaries": binaries,
        "checksums": bool(policy.get("checksums", True)) and bool(required_assets),
        "registries": registries,
        "required_assets": required_assets,
    }


def _metadata(policy: dict, version: str, tag: str, assets: list[Path]) -> dict:
    started = os.environ.get("RELEASE_BUILD_STARTED_AT") or dt.datetime.now(dt.UTC).isoformat()
    sha = os.environ.get("GITHUB_SHA") or _run(["git", "rev-parse", "HEAD"], capture=True)
    workflow_url = f"{os.environ.get('GITHUB_SERVER_URL', 'https://github.com')}/{os.environ.get('GITHUB_REPOSITORY')}/actions/runs/{os.environ.get('GITHUB_RUN_ID')}"
    return {
        "repository": os.environ.get("GITHUB_REPOSITORY"), "version": version, "tag": tag, "commit_sha": sha,
        "build_run_id": os.environ.get("GITHUB_RUN_ID"), "build_run_url": workflow_url,
        "workflow_run_id": os.environ.get("GITHUB_RUN_ID"), "workflow_run_url": workflow_url,
        "releasegraph_version": os.environ.get("RELEASEGRAPH_VERSION", __version__),
        "policy_schema": 1,
        "capabilities": capabilities(policy),
        "release_infra_version": __version__, "policy_hash": policy["_hash"], "release_policy_hash": policy["_hash"], "build_started_at": started,
        "published_at": dt.datetime.now(dt.UTC).isoformat(), "assets": [path.name for path in assets],
        "asset_sha256": {path.name: sha256(path) for path in assets}, "registries": policy.get("registries", {}),
        "container_digest": os.environ.get("CONTAINER_DIGEST"), "package_versions": {name: version for name in policy.get("registries", {})},
        "source_commit": sha, "release_pr": os.environ.get("RELEASE_PR"), "retry_count": int(os.environ.get("RELEASE_RETRY_COUNT", "0")),
    }


def publish(policy_path: str = ".release-policy.yml", version: str | None = None, *, dry_run: bool = False) -> str:
    policy = load_policy(policy_path)
    desired = desired_version(policy, version)
    tag = policy.get("tag", {}).get("template", "v{version}").format(version=desired)
    assets = collect_assets(policy.get("assets", {}).get("required", []), policy.get("assets", {}).get("optional", []))
    Path("dist/release").mkdir(parents=True, exist_ok=True)
    metadata_path = Path("dist/release/RELEASE-METADATA.json")
    metadata_path.write_text(json.dumps(_metadata(policy, desired, tag, assets), indent=2) + "\n")
    _upload_idempotent(tag, [metadata_path], dry_run=dry_run)
    if dry_run:
        return tag
    current = _release(tag)
    if current is None:
        # stage() guarantees the draft exists, so an unreadable release here is a
        # transient API failure. Failing closed keeps a publish from slipping
        # past the artefact gate on a read that never happened.
        raise ReleaseError(f"cannot read {tag} from GitHub; refusing to publish an ungated release")
    draft = bool(current.get("isDraft"))
    if draft:
        # Promote-after-artifacts: the draft is the transaction, and it may only
        # become the official release once the real artifacts are on it. A
        # source-code-only page must never be what a download user lands on.
        missing = missing_required_assets(policy, current.get("assets", []))
        if missing:
            raise ReleaseError(
                f"refusing to publish {tag}: draft is missing required assets: {', '.join(sorted(missing))}"
            )
    prerelease = notes.is_prerelease(desired, policy)
    command = ["gh", "release", "edit", tag, "--draft=false", f"--prerelease={'true' if prerelease else 'false'}"]
    if prerelease:
        # A pre-release never takes the Latest badge; GitHub keeps Latest on the
        # newest published stable release.
        print(f"note: {tag} is a pre-release; Latest is left untouched")
    elif _is_newest(desired):
        command.append("--latest")
    else:
        # Same-version repair of an older release: keep Latest where it is, and
        # say so explicitly rather than relying on the API's default.
        print(f"note: a newer release is currently Latest; publishing {tag} without --latest")
        command.append("--latest=false")
    if draft:
        # Regenerate the body at promotion time so a draft created by an earlier
        # engine (or a repair run) still ends up with user-facing notes. Only a
        # draft is touched: a published release's body is immutable history.
        notes_path = _notes_file(policy, desired, tag, list(current.get("assets", [])))
        command += ["--notes-file", str(notes_path)]
    else:
        notes_path = None
    try:
        _run(command)
    finally:
        if notes_path is not None:
            notes_path.unlink(missing_ok=True)
    audit(policy_path, desired)
    # Label acknowledgement is owned by the version provider: the workflow
    # pre-reconciles before release-please and acknowledges after publish.
    # A second best-effort mutator here would only create a second writer of
    # derived state (see AGENTS.md rule 10).
    prune(policy_path)
    return tag


def audit(policy_path: str = ".release-policy.yml", version: str | None = None) -> None:
    policy = load_policy(policy_path)
    desired = desired_version(policy, version)
    tag = policy.get("tag", {}).get("template", "v{version}").format(version=desired)
    expected_commit = os.environ.get("GITHUB_SHA") or _run(["git", "rev-parse", "HEAD"], capture=True)
    if _remote_tag_commit(tag) != expected_commit:
        raise ReleaseError(f"tag commit mismatch for {tag}")
    release = _release(tag)
    if not release or release["isDraft"]:
        raise ReleaseError(f"public release missing for {tag}")
    remote = {asset["name"]: asset for asset in release["assets"]}
    required = set(policy.get("assets", {}).get("required", [])) | {"RELEASE-METADATA.json"}
    if policy.get("checksums", True) and policy.get("assets", {}).get("required"):
        required.add("SHA256SUMS")
    missing = [
        pattern for pattern in required
        if not any(fnmatch.fnmatch(name, pattern) and int(asset.get("size", 0)) > 0 for name, asset in remote.items())
    ]
    if missing:
        raise ReleaseError(f"release assets missing or empty: {', '.join(sorted(missing))}")
    if policy.get("checksums", True) and policy.get("assets", {}).get("required"):
        sums = _remote_text(tag, "SHA256SUMS")
        listed = {
            parts[1].lstrip("*").strip()
            for line in sums.splitlines()
            if (parts := line.split(None, 1)) and len(parts) == 2
        }
        uncovered = [
            pattern for pattern in policy["assets"]["required"]
            if not any(fnmatch.fnmatch(name, pattern) for name in listed)
        ]
        if uncovered:
            raise ReleaseError(f"SHA256SUMS does not cover: {', '.join(sorted(uncovered))}")
    if not release["isPrerelease"] and not release["isLatest"]:
        raise ReleaseError(f"{tag} is not latest")


def _remote_text(tag: str, asset: str) -> str:
    with tempfile.TemporaryDirectory() as directory:
        _run(["gh", "release", "download", tag, "--pattern", asset, "--dir", directory])
        return (Path(directory) / asset).read_text()


def prune(policy_path: str = ".release-policy.yml") -> None:
    """Retire drafts and pre-releases past their limits.

    Published *stable* releases are history, not cache: GitHub derives its
    changelog and comparison links from them, and a user following an old link
    must still land on a real page. So they are only pruned when the policy
    explicitly opts in with `retention.pruneStable: true`, and the release
    currently marked Latest is never pruned at all.
    """
    policy = load_policy(policy_path)
    retention = policy.get("retention", {})
    keep_stable = int(retention.get("stable", 1))
    keep_prerelease = int(retention.get("prerelease", 1))
    keep_drafts = int(retention.get("failed_draft", 2))
    prune_stable = bool(retention.get("pruneStable", False))
    releases = json.loads(_run(["gh", "release", "list", "--limit", "100", "--json", "tagName,isDraft,isPrerelease,isLatest,publishedAt,createdAt"], capture=True))
    # gh release list is latest-first for public releases; drafts have no
    # publishedAt and are appended, so sort each class explicitly.
    public = sorted(
        (r for r in releases if not r["isDraft"]),
        key=lambda r: r.get("publishedAt") or "", reverse=True,
    )
    stable = [r for r in public if not r["isPrerelease"]]
    prereleases = [r for r in public if r["isPrerelease"]]
    drafts = sorted(
        (r for r in releases if r["isDraft"]),
        key=lambda r: r.get("createdAt") or "", reverse=True,
    )
    expired = [
        *(stable[keep_stable:] if prune_stable else []),
        *prereleases[keep_prerelease:],
        *drafts[keep_drafts:],
    ]
    for release in expired:
        if release.get("isLatest"):
            print(f"note: {release['tagName']} is Latest and is kept regardless of retention")
            continue
        _run(["gh", "release", "delete", release["tagName"], "--yes"])


def assert_no_cleanup_tag(root: str | Path = ".") -> None:
    for path in Path(root).rglob("*"):
        if path.is_file() and (".github/workflows" in str(path) or "scripts" in path.parts):
            try:
                if "--cleanup-tag" in path.read_text(errors="ignore"):
                    raise ReleaseError(f"forbidden --cleanup-tag in {path}")
            except OSError:
                pass
