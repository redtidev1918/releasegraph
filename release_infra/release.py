from __future__ import annotations

import datetime as dt
import fnmatch
import json
import os
import subprocess
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from . import __version__
from .assets import collect_assets, sha256, write_checksums
from .github import GitHubError
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
        raise ReleaseError(f"{tag} is already public; run audit instead")
    if not release and not dry_run:
        _run(["gh", "release", "create", tag, "--verify-tag", "--draft", "--title", tag, "--generate-notes"])
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
    tag_drift = False
    try:
        head = _run(["git", "rev-parse", "HEAD"], capture=True)
        tag_commit = _remote_tag_commit(tag)
        tag_drift = bool(tag_commit and tag_commit != head)
    except (ReleaseError, subprocess.CalledProcessError):
        pass
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
    should_release = force or (not tag_drift and (repair or (not healthy and not needs_repair and not cooldown)))
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
        "required_publish": " && ".join(config.get("publish", ":") for name, config in registries.items() if name != "ghcr" and config.get("required", True) and config.get("publish")),
        "required_verify": " && ".join(config.get("verify", ":") for config in registries.values() if config.get("required", True) and config.get("verify")),
        "optional_publish": "; ".join(f"({config['publish']}) || true" for name, config in registries.items() if name != "ghcr" and not config.get("required", True) and config.get("publish")),
        "optional_verify": "; ".join(f"({config['verify']}) || true" for config in registries.values() if not config.get("required", True) and config.get("verify")),
        "post_publish": policy.get("release", {}).get("post_publish", ""),
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
    prerelease = bool(policy.get("release", {}).get("prerelease", False))
    command = ["gh", "release", "edit", tag, "--draft=false", f"--prerelease={'true' if prerelease else 'false'}"]
    if not prerelease:
        command.append("--latest")
    _run(command)
    audit(policy_path, desired)
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
    if not release["isPrerelease"] and not release["isLatest"]:
        raise ReleaseError(f"{tag} is not latest")


def prune(policy_path: str = ".release-policy.yml") -> None:
    policy = load_policy(policy_path)
    retention = policy.get("retention", {})
    keep_stable = int(retention.get("stable", 1))
    keep_prerelease = int(retention.get("prerelease", 1))
    releases = json.loads(_run(["gh", "release", "list", "--limit", "100", "--json", "tagName,isDraft,isPrerelease,publishedAt"], capture=True))
    stable = [release for release in releases if not release["isDraft"] and not release["isPrerelease"]]
    prereleases = [release for release in releases if not release["isDraft"] and release["isPrerelease"]]
    for release in stable[keep_stable:] + prereleases[keep_prerelease:]:
        _run(["gh", "release", "delete", release["tagName"], "--yes"])


def assert_no_cleanup_tag(root: str | Path = ".") -> None:
    for path in Path(root).rglob("*"):
        if path.is_file() and (".github/workflows" in str(path) or "scripts" in path.parts):
            try:
                if "--cleanup-tag" in path.read_text(errors="ignore"):
                    raise ReleaseError(f"forbidden --cleanup-tag in {path}")
            except OSError:
                pass
