package domain

// Repository release health.
//
// Health answers exactly one question: does the released state satisfy the
// policy? It is never an instruction — the action to take is a different axis.
//
// Scope, stated precisely, because two things here are easy to confuse:
//
//   - RepoHealthValues is the vocabulary of a *repository* health report: what
//     `releasegraph fleet` and the Python fleet inventory
//     (`release_infra/health.py`) emit. It is the value set of
//     schemas/health-v1.json, and `tests/test_health.py` asserts all three
//     agree, so no layer can quietly invent a value.
//   - The wider Health type is *transaction* state: READY, RUNNING, NOOP,
//     ACK_PENDING and WAIVED describe a node in a release transaction.
//     provider.Verdict.Health is that axis, not this one — it reports
//     RECOVERABLE, which names an available action ("repair the same version")
//     rather than a state. Never report a Verdict.Health as repository health.
//
// Repository health is computed by internal/health and used by internal/fleet,
// which reports it from `releasegraph fleet audit`. It agrees exactly with the
// Python inventory on the real fleet, including reason codes and asset gaps.
//
// Known gaps, recorded rather than papered over:
//
//   - provider.Verdict carries no reasons, while the fleet views do.
//
// There is deliberately no value for "the release exists but its required
// assets do not": that is HealthDegraded plus ReasonTargetAssetsMissing.
// internal/provider/capabilities.go already reports missing required binaries
// as DEGRADED, and a second word for it would contradict it.
var RepoHealthValues = []Health{
	HealthHealthy,
	HealthDegraded,
	HealthNoRelease,
	HealthUnmanaged,
	HealthBroken,
	HealthBlocked,
	HealthNeedsReview,
}

// Reason codes explain a health value. A repository is never reported as
// anything other than HealthHealthy without at least one reason, and the
// reasons carry the specificity that keeps the health vocabulary closed.
const (
	ReasonUnmanagedRepo       = "unmanaged_repo" // no release policy is declared
	ReasonAPIError            = "api_error"      // provider state could not be read
	ReasonReleaseAbsent       = "release_absent" // no non-draft release for the desired version
	ReasonReleaseDraft        = "release_draft"  // a draft release exists and was not published
	ReasonRepositoryArchive   = "repository_archived"
	ReasonRepositoryFork      = "repository_fork"
	ReasonVersionDrift        = "version_drift"         // the release tag is not the desired version
	ReasonTargetAssetsMiss    = "target_assets_missing" // required asset patterns matched nothing
	ReasonAssetEmpty          = "asset_empty"           // a required asset matched only zero-byte files
	ReasonReleaseRunFailed    = "release_run_failed"    // the release workflow run did not succeed
	ReasonPolicyAbsent        = "policy_absent"
	ReasonPolicyUnparsable    = "policy_unparsable"
	ReasonDependencyUnhealthy = "dependency_unhealthy"
	ReasonFleetCredential     = "fleet_credential_required"
	ReasonManualReview        = "manual_review_required"
)

// ReasonCodes lists every reason code, in schema order.
var ReasonCodes = []string{
	ReasonUnmanagedRepo,
	ReasonAPIError,
	ReasonReleaseAbsent,
	ReasonReleaseDraft,
	ReasonRepositoryArchive,
	ReasonRepositoryFork,
	ReasonVersionDrift,
	ReasonTargetAssetsMiss,
	ReasonAssetEmpty,
	ReasonReleaseRunFailed,
	ReasonPolicyAbsent,
	ReasonPolicyUnparsable,
	ReasonDependencyUnhealthy,
	ReasonFleetCredential,
	ReasonManualReview,
}

// IsRepoHealth reports whether a health value can describe a repository.
func IsRepoHealth(h Health) bool {
	for _, candidate := range RepoHealthValues {
		if candidate == h {
			return true
		}
	}
	return false
}
