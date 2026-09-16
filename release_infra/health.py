"""Single source of truth for release health.

Health answers exactly one question: *does the released state satisfy the
policy?* It is deliberately not an instruction. The action a human or a
workflow should take is a different axis, computed by the planner
(``release_health``) and by ``provider reconcile``.

Two properties make this module usable as a contract:

1. The asset comparison is the *same* comparison the planner uses
   (``fnmatch``, required patterns must each match at least one non-empty
   asset, plus the mandatory metadata asset). The fleet view and the
   release gate therefore cannot disagree about the same release.
2. The vocabulary is closed and shared with Go (``internal/domain/health.go``)
   and with ``schemas/health-v1.json``. Adding a value is a contract change
   covered by ``tests/test_health.py``.

History: this module exists because the two halves disagreed. The planner
already failed a release whose required assets were missing, while the fleet
inventory reported ``DEGRADED`` with no reason and never compared required
patterns against actual assets at all. ``target_assets_missing`` names that
state; the reason codes carry the specificity so the value set stays closed.
"""

from __future__ import annotations

import fnmatch
from dataclasses import dataclass, field
from typing import Any, Iterable, Mapping, Sequence

from .policy import PolicyError, validate_asset_pattern

# --- health values -----------------------------------------------------------
# Fleet-facing repository health. A strict subset of Go's internal/domain.Health
# (the rest of that type is plan/transaction node state, not repository health),
# and the enum in schemas/health-v1.json. tests/test_health.py asserts the three
# agree, so no layer can quietly invent a value.
#
# Deliberately absent: a "PARTIAL" value for "release exists, required assets do
# not". Go already reports that condition as DEGRADED, and inventing a second
# word for it would recreate the contradiction this contract exists to remove.
# The distinction lives in the reason code (target_assets_missing).
HEALTHY = "HEALTHY"
DEGRADED = "DEGRADED"
NO_RELEASE = "NO_RELEASE"
UNMANAGED = "UNMANAGED"
BROKEN = "BROKEN"
BLOCKED = "BLOCKED"
NEEDS_REVIEW = "NEEDS_REVIEW"

VALUES: tuple[str, ...] = (
    HEALTHY,
    DEGRADED,
    NO_RELEASE,
    UNMANAGED,
    BROKEN,
    BLOCKED,
    NEEDS_REVIEW,
)

#: Reason codes explaining a health value. A value is never emitted without at
#: least one reason unless it is HEALTHY.
CODE_UNMANAGED_REPO = "unmanaged_repo"
CODE_API_ERROR = "api_error"
CODE_RELEASE_ABSENT = "release_absent"
CODE_RELEASE_DRAFT = "release_draft"
CODE_REPOSITORY_ARCHIVED = "repository_archived"
CODE_REPOSITORY_FORK = "repository_fork"
CODE_VERSION_DRIFT = "version_drift"
CODE_TARGET_ASSETS_MISSING = "target_assets_missing"
CODE_ASSET_EMPTY = "asset_empty"
CODE_RELEASE_RUN_FAILED = "release_run_failed"
CODE_POLICY_ABSENT = "policy_absent"
CODE_POLICY_UNPARSABLE = "policy_unparsable"
CODE_DEPENDENCY_UNHEALTHY = "dependency_unhealthy"
CODE_FLEET_CREDENTIAL_REQUIRED = "fleet_credential_required"
CODE_MANUAL_REVIEW_REQUIRED = "manual_review_required"

CODES: tuple[str, ...] = (
    CODE_UNMANAGED_REPO,
    CODE_API_ERROR,
    CODE_RELEASE_ABSENT,
    CODE_RELEASE_DRAFT,
    CODE_REPOSITORY_ARCHIVED,
    CODE_REPOSITORY_FORK,
    CODE_VERSION_DRIFT,
    CODE_TARGET_ASSETS_MISSING,
    CODE_ASSET_EMPTY,
    CODE_RELEASE_RUN_FAILED,
    CODE_POLICY_ABSENT,
    CODE_POLICY_UNPARSABLE,
    CODE_DEPENDENCY_UNHEALTHY,
    CODE_FLEET_CREDENTIAL_REQUIRED,
    CODE_MANUAL_REVIEW_REQUIRED,
)

#: Every managed release carries this asset regardless of policy. It is the
#: machine-readable record of the release transaction, so a release without it
#: cannot be audited and is not healthy.
MANDATORY_ASSET = "RELEASE-METADATA.json"

#: Checksum manifest, required as soon as checksums are enabled (the default)
#: and the policy declares required assets.
CHECKSUMS_ASSET = "SHA256SUMS"

#: Workflow conclusions that do not indicate a failed release.
_OK_RUN_CONCLUSIONS = frozenset({"success", "skipped"})

#: The workflow-facing vocabulary emitted by `releasegraph workflow-plan`
#: (`release_infra/release.py`). `reusable-release.yml` compares
#: `release_health != 'healthy'` in eight places, so these strings are a frozen
#: production contract: they may gain values, never lose or rename one.
RELEASE_HEALTHY = "healthy"
RELEASE_TAG_DRIFT = "tag-drift"
RELEASE_REPAIR = "repair"
RELEASE_MISSING = "missing"
RELEASE_HEALTH_VALUES: tuple[str, ...] = (
    RELEASE_HEALTHY,
    RELEASE_TAG_DRIFT,
    RELEASE_REPAIR,
    RELEASE_MISSING,
)

#: Health value -> workflow-facing release_health. Values that need a human
#: decision (`BROKEN`/`UNMANAGED`/`NEEDS_REVIEW`) map to the loudest value, so a
#: workflow never mistakes an unknown state for a healthy one.
_TO_RELEASE_HEALTH = {
    HEALTHY: RELEASE_HEALTHY,
    DEGRADED: RELEASE_REPAIR,
    NO_RELEASE: RELEASE_MISSING,
    UNMANAGED: RELEASE_MISSING,
    BROKEN: RELEASE_MISSING,
    BLOCKED: RELEASE_REPAIR,
    NEEDS_REVIEW: RELEASE_REPAIR,
}


@dataclass(frozen=True)
class Reason:
    """Why a health value was chosen, in a machine-readable form."""

    code: str
    detail: str | None = None

    def as_dict(self) -> dict[str, str]:
        payload = {"code": self.code}
        if self.detail:
            payload["detail"] = self.detail
        return payload


@dataclass(frozen=True)
class Health:
    """A health value plus the reasons that produced it."""

    value: str
    reasons: tuple[Reason, ...] = ()
    missing_assets: tuple[str, ...] = ()
    empty_assets: tuple[str, ...] = ()

    @property
    def ok(self) -> bool:
        return self.value == HEALTHY

    @property
    def codes(self) -> tuple[str, ...]:
        return tuple(reason.code for reason in self.reasons)

    def as_dict(self) -> dict[str, Any]:
        payload: dict[str, Any] = {
            "health": self.value,
            "reasons": [reason.as_dict() for reason in self.reasons],
        }
        if self.missing_assets:
            payload["missing_assets"] = list(self.missing_assets)
        if self.empty_assets:
            payload["empty_assets"] = list(self.empty_assets)
        return payload

    def release_health(self) -> str:
        """The workflow-facing projection of this health value."""
        return to_release_health(self.value)


@dataclass(frozen=True)
class Observation:
    """The facts a health decision is made from.

    Everything here is derived from real provider state (GitHub releases, tags,
    assets, workflow runs). Nothing is taken from release-please labels: those
    are acknowledgement, never authority.
    """

    release: str | None = None
    draft_release: str | None = None
    # Named for the drift, not for the match, so its zero value is the
    # unremarkable case and testdata/health/cases.json can drive both this
    # implementation and the Go one with the same keys.
    tag_drift: bool = False
    assets: Sequence[Mapping[str, Any]] = field(default_factory=tuple)
    run_conclusion: str | None = None
    has_policy: bool = True
    policy_parsable: bool = True
    archived: bool = False
    fork: bool = False
    unmanaged: bool = False
    api_error: str | None = None
    dependency_blocked: str | None = None
    credential_missing: bool = False
    needs_review: bool = False


def required_assets(policy: Mapping[str, Any] | None) -> list[str]:
    """Required asset patterns, exactly as the release planner computes them.

    `release_infra/release.py` builds its required set as the policy's
    `assets.required`, unioned with the mandatory metadata asset, plus the
    checksum manifest whenever checksums are enabled (the default) and the
    policy declares required assets. This function reproduces that set word for
    word; `tests/test_health.py` fails if the two ever diverge. Order is
    preserved for stable reporting.
    """
    declared = list((policy or {}).get("assets", {}).get("required", []) or [])
    names = list(dict.fromkeys([*declared, MANDATORY_ASSET]))
    if (policy or {}).get("checksums", True) and declared:
        names = list(dict.fromkeys([*names, CHECKSUMS_ASSET]))
    return names


def asset_gaps(
    policy: Mapping[str, Any] | None, assets: Iterable[Mapping[str, Any]]
) -> tuple[list[str], list[str]]:
    """Return `(missing_patterns, empty_patterns)` for a release's assets.

    A pattern is *missing* when no released asset name matches it, and *empty*
    when it matches only zero-byte assets. The planner collapses both into "not
    present" (it builds its remote set from assets with `size > 0`), so
    `missing + empty` is exactly its missing set — one judgement, two different
    fixes: re-upload the asset, or fix an upload that produced nothing.

    Matching is `fnmatch`, the same function the planner uses.
    """
    manifest = [asset for asset in assets if asset.get("name")]
    missing: list[str] = []
    empty: list[str] = []
    for pattern in required_assets(policy):
        # Policies are validated when they are loaded, so this only fires for a
        # policy assembled in memory. It is checked anyway because a silently
        # different answer is worse than an error, and internal/health does the
        # same thing.
        validate_asset_pattern(pattern)
        matched = [a for a in manifest if fnmatch.fnmatch(str(a["name"]), pattern)]
        if not matched:
            missing.append(pattern)
        elif not any(int(a.get("size") or 0) > 0 for a in matched):
            empty.append(pattern)
    return missing, empty


def evaluate(
    policy: Mapping[str, Any] | None, observation: Observation
) -> Health:
    """Decide health from policy plus observed provider state.

    Precedence is deliberate: an unreadable provider is `BROKEN` before anything
    else, and an unmanaged repository is `UNMANAGED` before any release check,
    because both make every later judgement meaningless.
    """
    if observation.api_error:
        return Health(BROKEN, (Reason(CODE_API_ERROR, observation.api_error),))
    if observation.archived:
        return Health(NO_RELEASE, (Reason(CODE_REPOSITORY_ARCHIVED),))
    if observation.fork:
        return Health(NO_RELEASE, (Reason(CODE_REPOSITORY_FORK),))
    if observation.unmanaged:
        return Health(UNMANAGED, (Reason(CODE_UNMANAGED_REPO),))
    if observation.credential_missing:
        return Health(BLOCKED, (Reason(CODE_FLEET_CREDENTIAL_REQUIRED),))
    if observation.dependency_blocked:
        return Health(
            BLOCKED, (Reason(CODE_DEPENDENCY_UNHEALTHY, observation.dependency_blocked),)
        )

    reasons: list[Reason] = []
    if not observation.has_policy:
        reasons.append(Reason(CODE_POLICY_ABSENT))
    elif not observation.policy_parsable:
        reasons.append(Reason(CODE_POLICY_UNPARSABLE))

    if not observation.release:
        reasons.append(
            Reason(CODE_RELEASE_ABSENT)
            if not observation.draft_release
            else Reason(CODE_RELEASE_DRAFT, observation.draft_release)
        )
        return Health(NO_RELEASE, tuple(reasons))

    try:
        missing, empty = asset_gaps(policy, observation.assets)
    except PolicyError as exc:
        # Mirrors internal/health: an unusable pattern is an unusable policy,
        # not a degraded release.
        return Health(
            BROKEN,
            tuple(reasons) + (Reason(CODE_POLICY_UNPARSABLE, str(exc)),),
        )
    if missing:
        reasons.append(Reason(CODE_TARGET_ASSETS_MISSING, ", ".join(missing)))
    if empty:
        reasons.append(Reason(CODE_ASSET_EMPTY, ", ".join(empty)))
    if observation.tag_drift:
        reasons.append(Reason(CODE_VERSION_DRIFT, observation.release))
    if observation.draft_release:
        reasons.append(Reason(CODE_RELEASE_DRAFT, observation.draft_release))
    if (
        observation.run_conclusion
        and observation.run_conclusion not in _OK_RUN_CONCLUSIONS
    ):
        reasons.append(Reason(CODE_RELEASE_RUN_FAILED, observation.run_conclusion))

    if reasons:
        return Health(DEGRADED, tuple(reasons), tuple(missing), tuple(empty))
    if observation.needs_review:
        return Health(NEEDS_REVIEW, (Reason(CODE_MANUAL_REVIEW_REQUIRED),))
    return Health(HEALTHY)


def to_release_health(value: str) -> str:
    """Project a health value onto the workflow-facing vocabulary."""
    try:
        return _TO_RELEASE_HEALTH[value]
    except KeyError:  # pragma: no cover - guarded by tests
        raise ValueError(f"unknown health value: {value}") from None


def from_release_health(value: str) -> str:
    """Project the workflow-facing vocabulary back onto a health value.

    `repair` and `missing` are lossy on purpose: the workflow only needs to know
    whether to proceed, so the reverse map returns the value that carries the
    necessary action.
    """
    if value == RELEASE_HEALTHY:
        return HEALTHY
    if value == RELEASE_TAG_DRIFT:
        return DEGRADED
    if value == RELEASE_REPAIR:
        return DEGRADED
    if value == RELEASE_MISSING:
        return NO_RELEASE
    raise ValueError(f"unknown release_health value: {value}")
