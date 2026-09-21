"""User-facing GitHub Release bodies.

The GitHub Release body is read by the person downloading a build. It is not a
commit log and not a CI report. Conventional Commits are an *input signal*, never
the final copy: this module classifies them, drops everything that is developer
or automation internals, and renders the survivors as user phrasing in the
repository's own language.

`CHANGELOG.md` (if a repository keeps one) stays the fuller developer-facing
history. This module owns only what GitHub shows on the Release page.

Everything here is pure: `build_body` takes commits, assets and a policy and
returns a string. `release.py` is the only caller that touches git or the API.
See `docs/release-notes.md` for the human-override mechanism.
"""

from __future__ import annotations

import json
import os
import re
import subprocess
from dataclasses import dataclass
from pathlib import Path

# --------------------------------------------------------------------------
# Language
# --------------------------------------------------------------------------

CJK = re.compile(r"[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]")
_READMES = ("README.md", "README.rst", "README.txt", "readme.md", "Readme.md")
# A Chinese-first README mixes prose with code fences and command examples, so
# the test is "clearly Chinese", not "contains one character". Every Chinese
# README in the fleet scores > 380 CJK characters in its first 4000; the two
# English-first repositories in the fleet score 0.
CJK_THRESHOLD = 30

LANGUAGES = ("auto", "en", "zh")

# Every GitHub Release body is user-facing and every repository in this fleet
# has Chinese users, so a body with zero Chinese characters is never shipped.
# The auto-detected language still decides the main sections; this notice is
# the guarantee that the page contains Chinese no matter which language wins.
CHINESE_NOTICE = (
    "> 中文说明：本版本内容请以仓库 README 与下载页为准，中文用户可查看中文文档。"
)


def detect_language(root: str | Path = ".") -> str:
    """The repository's primary user-facing language, from its primary README.

    GitHub renders ``README.md`` as the repository's front page, so that file --
    not a translation mirror such as ``README.en.md`` -- decides the language of
    the notes. Defaults to English when there is no README to read.
    """
    base = Path(root)
    for name in _READMES:
        path = base / name
        if not path.is_file():
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="ignore")[:4000]
        except OSError:
            continue
        return "zh" if len(CJK.findall(text)) >= CJK_THRESHOLD else "en"
    return "en"


def resolve_language(policy: dict, root: str | Path = ".") -> str:
    """Policy override wins; otherwise the README decides."""
    declared = str(policy.get("release", {}).get("notes", {}).get("language", "auto"))
    if declared not in LANGUAGES:
        raise ValueError(f"release.notes.language must be one of: {', '.join(LANGUAGES)}")
    return detect_language(root) if declared == "auto" else declared


def ensure_chinese(body: str) -> str:
    """Return a Release body that always contains at least one Chinese character."""
    if CJK.search(body):
        return body
    return body.rstrip() + "\n\n" + CHINESE_NOTICE + "\n"


SECTIONS = {
    "en": {
        "new": "What's new",
        "fixes": "Fixes",
        "improvements": "Improvements",
        "breaking": "Breaking changes",
        "upgrade": "Upgrade notes",
        "downloads": "Downloads",
    },
    "zh": {
        "new": "新增功能",
        "fixes": "问题修复",
        "improvements": "体验改进",
        "breaking": "破坏性变更",
        "upgrade": "升级说明",
        "downloads": "下载",
    },
}

STRINGS = {
    "en": {
        "prerelease": "> **Pre-release.** This build is for testing; it is not the latest stable release.",
        "maintenance": "_This is a maintenance release with no user-facing changes._",
        "plain_source": "> This repository ships no downloadable binaries. See the repository documentation for installation.",
        "channels": "> This repository ships no downloadable binaries; it is delivered through {channels}. See the repository documentation for installation.",
        "container": "> This release is delivered as a container image: `{image}:{version}`.",
        "checksums": "Verify your download against `SHA256SUMS` on this release.",
        "breaking_upgrade": "- This release contains breaking changes; read the section above before upgrading.",
        "table_head": "| Platform | File | Size | Download |",
        "table_rule": "|---|---|---|---|",
        "download": "Download",
    },
    "zh": {
        "prerelease": "> **预发布版本。** 仅供测试，并非当前最新稳定版。",
        "maintenance": "_本次为维护版本，没有面向用户的变更。_",
        "plain_source": "> 本仓库不提供可下载的二进制；安装方式见仓库文档。",
        "channels": "> 本仓库不提供可下载的二进制，交付渠道为 {channels}；安装方式见仓库文档。",
        "container": "> 本次以容器镜像交付：`{image}:{version}`。",
        "checksums": "下载后可用本 Release 资产中的 `SHA256SUMS` 校验完整性。",
        "breaking_upgrade": "- 本次包含破坏性变更，升级前请先阅读上一节。",
        "table_head": "| 平台 | 文件 | 大小 | 下载 |",
        "table_rule": "|---|---|---|---|",
        "download": "下载",
    },
}

# --------------------------------------------------------------------------
# Classification
# --------------------------------------------------------------------------

CONVENTIONAL = re.compile(
    r"^(?P<type>[A-Za-z][A-Za-z0-9_-]*)(?:\((?P<scope>[^)]*)\))?(?P<breaking>!)?:\s*(?P<subject>.+?)\s*$"
)

# Conventional type -> user-facing section. ``None`` means "automation or
# repository internals": never shown to a download user, even though the commit
# is perfectly legitimate. An unmapped type is treated the same way, so a novel
# internal type can never leak by accident -- it has to earn a mapping here or
# carry an explicit `release-note:` fragment.
TYPE_CATEGORY = {
    "feat": "new",
    "feature": "new",
    "fix": "fixes",
    "bugfix": "fixes",
    "hotfix": "fixes",
    "security": "fixes",
    "revert": "fixes",
    "perf": "improvements",
    "refactor": "improvements",
    "improve": "improvements",
    "chore": None,
    "ci": None,
    "build": None,
    "release": None,
    "docs": None,
    "doc": None,
    "test": None,
    "tests": None,
    "style": None,
    "deps": None,
    "dep": None,
    "dependency": None,
    "governance": None,
    "ops": None,
    "infra": None,
}

# Phrases that name automation or repository internals, in *normalized* form
# (lower-case, separators folded to spaces). Applied to every candidate bullet,
# including ones that survived the type filter: a `feat` whose subject is about
# the release pipeline is still not news for a download user.
NOISE_PHRASES = (
    "release infra",
    "release infrastructure",
    "releasegraph",
    "provider reconciliation",
    "branch contract",
    "pull request lifecycle",
    "pr lifecycle",
    "generated metadata",
    "release please",
    "dependabot",
    "renovate",
    "dependency bot",
    "lockfile",
    "lock file",
    "workflow refactor",
    "github actions workflow",
    "governance",
    "changelog",
    "branch protection",
    "merge queue",
)

NOISE_WORDS = ("chore", "ci", "wip")

# A conventional-commit scope that names pure repository automation rather than
# a product surface. The scope itself is never rendered; it only decides whether
# the commit is user-facing at all. This set stays tiny on purpose: the type
# (chore/ci/build/release/...) and the author (dependabot/...) already remove the
# bulk of the noise, and a real product ships user-facing changes under scopes
# such as `release-notes`, `inventory` or `pipeline` — those MUST survive. Only a
# scope whose every token names automation disqualifies the commit.
SCOPE_STOPWORDS = {"ci", "chore", "governance", "deps", "dependency", "dependabot"}

_SCOPE_TOKEN = re.compile(r"[a-z0-9]+")

VERSION_BUMP = re.compile(
    # "release 1.7.0", "bump v1.2.3", a bare version at the head of a subject, or
    # any dependency bump. Anchored so a genuine subject that merely starts with
    # the word "release" survives.
    r"^(?:release|bump)\b[^a-z]{0,20}v?\d+\.\d+"
    r"|^v?\d+\.\d+\.\d+\b"
    r"|\bversion bump\b"
    r"|\bbump\b.{0,40}\bto\b",
    re.IGNORECASE,
)
MERGE_COMMIT = re.compile(r"^merge (?:branch|remote-tracking|pull request|commit)\b", re.IGNORECASE)

BOT_AUTHORS = ("dependabot[bot]", "renovate[bot]", "github-actions[bot]", "renovate-bot")

# Developer verbs at the start of a subject. Stripped before the user-facing verb
# is applied, so `fix: avoid crash ...` does not become `Fixed avoid crash ...`.
EN_VERBS = {
    "new": ("add", "adds", "added", "introduce", "introduces", "implement", "implements",
            "support", "supports", "provide", "provides", "expose", "exposes", "allow", "allows",
            "enable", "enables", "ship", "ships"),
    "fixes": ("fix", "fixes", "fixed", "avoid", "avoids", "prevent", "prevents", "correct",
              "corrects", "handle", "handles", "stop", "stops", "repair", "repairs",
              "restore", "restores", "keep", "keeps", "guard", "guards"),
    "improvements": ("improve", "improves", "optimize", "optimizes", "optimise", "optimises",
                     "simplify", "simplifies", "reduce", "reduces", "speed", "make", "makes",
                     "update", "updates", "refactor", "refactors", "clean", "cleans", "rename",
                     "renames", "move", "moves", "switch", "switches", "use", "uses", "migrate",
                     "migrates", "align", "aligns", "tighten", "tightens", "rework", "reworks",
                     "restructure", "restructures", "split", "splits", "merge", "merges",
                     "polish", "polishes", "document", "documents", "clarify", "clarifies"),
}

EN_LEAD = {"new": "Added", "fixes": "Fixed", "improvements": "Improved"}

# Chinese subjects are frequently written with the verb already in Chinese, so
# both the English verb table and a Chinese verb table are stripped.
ZH_VERBS = (
    "新增", "添加", "引入", "实现", "支持", "暴露", "允许", "启用", "发布",
    "修复", "修正", "避免", "防止", "恢复", "处理", "回滚", "兜底",
    "优化", "改进", "完善", "提升", "简化", "减少", "加速", "重构", "调整",
    "更新", "升级", "迁移", "重命名", "拆分", "合并", "移动", "切换到", "改用",
    "收紧", "对齐", "统一", "清理", "删除", "移除", "补充", "确保", "保证",
)

ZH_LEAD = {"new": "新增", "fixes": "修复", "improvements": "改进"}

CHANNEL_LABELS = {"npm": "npm", "pypi": "PyPI", "pub": "pub.dev", "ghcr": "GHCR"}

PR_REF = re.compile(r"\s*\(#\d+\)\s*$")

OVERRIDE_DIR = Path(".github/release-notes")

TRAILERS = {
    "release-note": "note",
    "release-note-breaking": "breaking",
    "upgrade-note": "upgrade",
}


# --------------------------------------------------------------------------
# Model
# --------------------------------------------------------------------------


@dataclass
class Commit:
    """One commit, as the note generator sees it."""

    subject: str
    body: str = ""
    sha: str = ""
    author: str = ""


@dataclass
class Entry:
    category: str
    text: str
    breaking: bool = False
    upgrade: str = ""


def entry_category(commit: Commit) -> str | None:
    """User-facing section for a commit, or ``None`` when it is not user-facing.

    The explicit fragment is the human override: a commit carrying
    ``release-note:`` is user-facing on a type that maps to a category, and is
    kept (as an improvement) on a type that does not -- a human decided it
    matters, and the machinery must not second-guess that decision.
    """
    fragments = parse_trailers(commit.body)
    match = CONVENTIONAL.match(commit.subject.strip())
    category = TYPE_CATEGORY.get(match.group("type").lower()) if match else None
    if "note" in fragments and category is None:
        return "improvements"
    return category


def parse_trailers(body: str) -> dict[str, str]:
    """Collect the opt-in user-facing fragments from a commit body.

    Recognised keys are case-insensitive and may be continued on a following
    indented or wrapped line::

        release-note: Fixed gallery delivery when some images exceeded
          Telegram's photo size limits.
        Upgrade-Note: Re-run `deploy.sh init` once after upgrading.

    A commit with no trailers costs its author nothing: omission is the default.
    """
    fragments: dict[str, str] = {}
    current: str | None = None
    for raw in body.splitlines():
        stripped = raw.strip()
        if not stripped:
            current = None
            continue
        key, separator, value = stripped.partition(":")
        kind = TRAILERS.get(key.strip().lower()) if separator else None
        if kind:
            fragments[kind] = " ".join(value.split())
            current = kind
        elif current and (raw[:1].isspace() or not separator):
            fragments[current] = f"{fragments[current]} {' '.join(stripped.split())}".strip()
        else:
            current = None
    return {key: value for key, value in fragments.items() if value}


def normalize(text: str) -> str:
    """Fold separators so `branch-contract` and `branch contract` are one token."""
    return re.sub(r"\s+", " ", text.lower().replace("-", " ").replace("_", " ")).strip()


def is_noise(text: str, match: re.Match | None = None) -> bool:
    """Whether a candidate string names automation or repository internals."""
    lowered = normalize(text)
    if VERSION_BUMP.search(text) or MERGE_COMMIT.search(text):
        return True
    if any(phrase in lowered for phrase in NOISE_PHRASES):
        return True
    if any(re.search(rf"\b{word}\b", lowered) for word in NOISE_WORDS):
        return True
    if match is not None and match.group("scope"):
        # A scope is the developer's module name (`release-notes`, `ci-official`).
        # It never reaches the body, but it still disqualifies the commit when it
        # names only automation. Tokenize on hyphen/underscore/digit boundaries —
        # NOT on whitespace, or `release-notes` would be one unmatchable token —
        # and require EVERY token to be an automation label, so a real module such
        # as `release-notes` or `pr-lifecycle` is kept, not silently dropped.
        tokens = set(_SCOPE_TOKEN.findall(normalize(match.group("scope"))))
        if tokens and tokens <= SCOPE_STOPWORDS:
            return True
    return False


# --------------------------------------------------------------------------
# Rendering
# --------------------------------------------------------------------------


def _clean_subject(subject: str) -> str:
    subject = PR_REF.sub("", subject.strip())
    return re.sub(r"\s+", " ", subject).strip()


def _strip_en_verb(subject: str, category: str) -> str:
    words = subject.split()
    if not words:
        return subject
    head = words[0].lower().strip(",.:;")
    if head in EN_VERBS[category]:
        return " ".join(words[1:]).lstrip(":,- ").strip() or subject
    return subject


def _strip_zh_verb(subject: str) -> str:
    for verb in ZH_VERBS:
        if subject.startswith(verb):
            return subject[len(verb):].lstrip("：: ，,-").strip() or subject
    return subject


def _sentence(text: str) -> str:
    text = text.strip()
    if not text:
        return text
    text = text[0].upper() + text[1:]
    return text if text.endswith((".", "!", "?", "。", "！", "？")) else f"{text}."


def render_bullet(subject: str, category: str, language: str) -> str:
    """Translate a developer subject into user phrasing.

    The conventional prefix and any scope are already gone by the time this
    runs, so `fix(media): refactor planner (#123)` can never reach a body. The
    leading developer verb is normalised into `Fixed` / `Added` / `Improved`
    (English) or a Chinese verb label (Chinese).
    """
    subject = _clean_subject(subject)
    if not subject:
        return ""
    if language == "zh":
        body = _strip_zh_verb(subject)
        if body == subject:
            body = _strip_en_verb(subject, category)
        return f"{ZH_LEAD[category]}：{body}"
    return _sentence(f"{EN_LEAD[category]} {_strip_en_verb(subject, category)}")


def collect_entries(commits: list[Commit], language: str) -> list[Entry]:
    """Classify, filter and translate a commit range into release entries."""
    entries: list[Entry] = []
    for commit in commits:
        if commit.author.strip().lower() in BOT_AUTHORS:
            continue
        category = entry_category(commit)
        if category is None:
            continue
        match = CONVENTIONAL.match(commit.subject.strip())
        subject = match.group("subject") if match else commit.subject
        if is_noise(subject, match) or is_noise(commit.subject):
            continue
        fragments = parse_trailers(commit.body)
        text = fragments.get("note") or render_bullet(subject, category, language)
        if not text or is_noise(text):
            continue
        breaking = bool(match and match.group("breaking")) or "breaking" in fragments
        if "breaking" in fragments:
            text = fragments["breaking"]
            breaking = True
        entries.append(Entry(category, text, breaking, fragments.get("upgrade", "")))
    return entries


def _platform(name: str, language: str) -> str:
    """Best-effort platform label from an asset filename."""
    lowered = name.lower()
    if "windows" in lowered or lowered.endswith((".exe", ".msi")):
        os_name = "Windows"
    elif "darwin" in lowered or "macos" in lowered or lowered.endswith(".dmg"):
        os_name = "macOS"
    elif "linux" in lowered or lowered.endswith((".deb", ".appimage")):
        os_name = "Linux"
    elif lowered.endswith(".apk"):
        os_name = "Android"
    else:
        os_name = "All platforms" if language == "en" else "通用"
    arch = next((a for a in ("arm64", "aarch64", "amd64", "x86_64", "x64", "arm", "386", "x86")
                 if a in lowered), "")
    if arch == "x86_64":
        arch = "x64"
    return f"{os_name} · {arch}" if arch else os_name


def _size(count: int) -> str:
    if count >= 1048576:
        return f"{count / 1048576:.1f} MB"
    if count >= 1024:
        return f"{count / 1024:.0f} KB"
    return f"{count} B"


def describe_channels(policy: dict) -> str:
    """The non-GitHub channels a repository actually delivers through."""
    names = [name for name in policy.get("registries", {}) if name != "github"]
    return ", ".join(CHANNEL_LABELS.get(name, name) for name in sorted(names))


def _distribution_note(policy: dict, language: str, version: str) -> str:
    """What to say instead of an empty Downloads section.

    A service, container or registry-only repository has no binaries to attach.
    Saying so explicitly is the point: an empty "downloads" area reads as a
    broken release to a user.
    """
    strings = STRINGS[language]
    image = policy.get("registries", {}).get("ghcr", {}).get("image", "")
    channels = describe_channels(policy)
    if image and channels == "GHCR":
        return strings["container"].format(image=image, version=version)
    if channels:
        return strings["channels"].format(channels=channels)
    return strings["plain_source"]


def downloads_block(policy: dict, language: str, version: str, assets: list[dict]) -> list[str]:
    """The Downloads section, or the honest replacement for it."""
    strings = STRINGS[language]
    download = strings["download"]
    rows: list[tuple[str, str, str, str]] = []
    for asset in assets:
        name = str(asset.get("name", ""))
        if not name or name.startswith(("SHA256SUMS", "RELEASE-METADATA", "RELEASEGRAPH-METADATA")):
            continue
        rows.append((_platform(name, language), name, _size(int(asset.get("size", 0))), str(asset.get("url", ""))))
    if not rows:
        # No binaries: an empty Downloads heading reads as a broken release, so
        # say what the repository actually delivers instead.
        return [_distribution_note(policy, language, version), ""]
    rows.sort(key=lambda row: (row[0], row[1]))
    lines = [f"## {SECTIONS[language]['downloads']}", "", strings["table_head"], strings["table_rule"]]
    for platform, name, size, url in rows:
        lines.append(f"| {platform} | `{name}` | {size} | [{download}]({url}) |")
    lines.append("")
    if any(str(asset.get("name", "")).startswith("SHA256SUMS") for asset in assets):
        lines += [strings["checksums"], ""]
    return lines


# --------------------------------------------------------------------------
# Assembly
# --------------------------------------------------------------------------


def render_body(
    policy: dict,
    version: str,
    tag: str,
    *,
    language: str = "en",
    entries: list[Entry] | None = None,
    assets: list[dict] | None = None,
    prerelease: bool = False,
) -> str:
    """Render one Release body. Empty sections are omitted, never emitted blank."""
    sections = SECTIONS[language]
    strings = STRINGS[language]
    entries = entries or []
    assets = assets or []
    lines: list[str] = []
    if prerelease:
        lines += [strings["prerelease"], ""]
    if not entries:
        lines += [strings["maintenance"], ""]
    order = (("new", "new"), ("fixes", "fixes"), ("improvements", "improvements"))
    for key, category in order:
        items = [entry.text for entry in entries if entry.category == category]
        if items:
            lines += [f"## {sections[key]}", *[f"- {item}" for item in items], ""]
    breaking = [entry.text for entry in entries if entry.breaking]
    if breaking:
        lines += [f"## {sections['breaking']}", *[f"- {item}" for item in breaking], ""]
    upgrade = [entry.upgrade for entry in entries if entry.upgrade]
    if breaking and not upgrade:
        upgrade = [strings["breaking_upgrade"]]
    if upgrade:
        lines += [f"## {sections['upgrade']}", *[f"- {note}" for note in upgrade], ""]
    lines += downloads_block(policy, language, version, assets)
    return "\n".join(lines).strip() + "\n"


def override_path(version: str, root: str | Path = ".") -> Path | None:
    """A hand-written body for a special release, if one exists.

    Checked in order: the version, the tag, then `current.md`. The directory is
    plain Markdown in the repository -- no schema, no tooling, no maintenance
    burden on ordinary commits.
    """
    base = Path(root) / OVERRIDE_DIR
    for name in (f"{version}.md", f"v{version}.md", "current.md"):
        path = base / name
        if path.is_file():
            return path
    return None


def is_prerelease(version: str, policy: dict) -> bool:
    """Whether a release must be published as a GitHub pre-release.

    A version with a SemVer pre-release component (`1.5.0-rc.1`, `2.0.0-beta.3`)
    *is* a pre-release; the policy flag can only force one, never demote one.
    Getting this wrong is what lets a pre-release take the Latest badge.
    """
    if policy.get("release", {}).get("prerelease", False):
        return True
    return bool(re.search(r"-[0-9A-Za-z.-]+$", version.removeprefix("v")))


def make_assets(names: list[str], *, tag: str, repository: str, sizes: dict[str, int] | None = None,
                server: str = "https://github.com") -> list[dict]:
    """Describe uploads as download rows, without a second API round trip."""
    sizes = sizes or {}
    return [
        {
            "name": name,
            "size": sizes.get(name, 0),
            "url": f"{server}/{repository}/releases/download/{tag}/{name}" if repository else "",
        }
        for name in sorted(set(names))
    ]


def repository_slug(root: str | Path = ".") -> str:
    slug = os.environ.get("GITHUB_REPOSITORY", "")
    if slug:
        return slug
    try:
        remote = subprocess.run(["git", "remote", "get-url", "origin"], cwd=root,
                                text=True, capture_output=True, check=True).stdout.strip()
    except (OSError, subprocess.CalledProcessError):
        return ""
    match = re.search(r"github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?$", remote)
    return f"{match.group(1)}/{match.group(2)}" if match else ""


def collect_commits(root: str | Path = ".", *, since: str | None = None, limit: int = 200) -> list[Commit]:
    """Read the commit range this release introduces.

    `since` is the previous release tag. Without one (first release, or a tag
    that was pruned) the newest `limit` commits are used, which is bounded work
    on any repository size.
    """
    revision = f"{since}..HEAD" if since else f"-n {limit}"
    command = ["git", "log", revision, "--no-merges", "--format=%H%x1f%s%x1f%b%x1f%an%x1e"]
    try:
        raw = subprocess.run(command, cwd=root, text=True, capture_output=True, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return []
    commits: list[Commit] = []
    for record in raw.split("\x1e"):
        fields = record.strip("\n").split("\x1f")
        if len(fields) < 4:
            continue
        commits.append(Commit(subject=fields[1].strip(), body=fields[2].strip(),
                              sha=fields[0].strip(), author=fields[3].strip()))
    return commits


def previous_tag(tag: str, root: str | Path = ".") -> str | None:
    """The tag this release is measured against.

    Local tags first (the release job checks out full history), then GitHub's
    release list, which still answers after a tag has been pruned.
    """
    for command in (["git", "describe", "--tags", "--abbrev=0", "--match", "v*", f"{tag}^"],
                    ["git", "describe", "--tags", "--abbrev=0", "--match", "v*", "HEAD^"]):
        try:
            found = subprocess.run(command, cwd=root, text=True, capture_output=True, check=True).stdout.strip()
        except (OSError, subprocess.CalledProcessError):
            continue
        if found and found != tag:
            return found
    try:
        raw = subprocess.run(["gh", "release", "list", "--limit", "10", "--json", "tagName,isDraft"],
                             cwd=root, text=True, capture_output=True, check=True).stdout
    except (OSError, subprocess.CalledProcessError):
        return None
    try:
        releases = json.loads(raw)
    except ValueError:
        return None
    return next((row["tagName"] for row in releases if not row.get("isDraft") and row["tagName"] != tag), None)


def build_for_release(
    policy: dict,
    version: str,
    tag: str,
    *,
    root: str | Path = ".",
    asset_names: list[str] | None = None,
    asset_sizes: dict[str, int] | None = None,
    commits: list[Commit] | None = None,
    repository: str = "",
) -> str:
    """Assemble the Release body for one version. The single entry point."""
    language = resolve_language(policy, root)
    override = override_path(version, root)
    slug = repository or repository_slug(root)
    assets = make_assets(asset_names or [], tag=tag, repository=slug, sizes=asset_sizes)
    prerelease = is_prerelease(version, policy)
    if override is not None:
        body = override.read_text(encoding="utf-8").strip() + "\n"
        if f"## {SECTIONS[language]['downloads']}" not in body:
            body += "\n" + "\n".join(downloads_block(policy, language, version, assets)).strip() + "\n"
        return ensure_chinese(body)
    if commits is None:
        commits = collect_commits(root, since=previous_tag(tag, root))
    entries = collect_entries(commits, language)
    return ensure_chinese(
        render_body(policy, version, tag, language=language, entries=entries, assets=assets,
                    prerelease=prerelease),
    )
