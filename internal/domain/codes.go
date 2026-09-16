package domain

// Stable machine-readable outcome codes. Human output may be friendly; JSON and
// exit codes must stay stable so automation never has to grep prose.
const (
	CodeHealthy                = "RG_HEALTHY"
	CodeSafeReconcileAvailable = "RG_SAFE_RECONCILE_AVAILABLE"
	CodeRepairAvailable        = "RG_REPAIR_AVAILABLE"
	CodeTransientRetry         = "RG_TRANSIENT_RETRY"
	CodeBlocked                = "RG_BLOCKED"
	CodeNeedsReview            = "RG_NEEDS_REVIEW"
	CodeBroken                 = "RG_BROKEN_INVARIANT"
	CodeScopeViolation         = "RG_SCOPE_VIOLATION"
	CodeFleetCredential        = "RG_FLEET_CREDENTIAL_REQUIRED"
	CodePolicyInvalid          = "RG_POLICY_INVALID"
	CodePolicyMigration        = "RG_POLICY_MIGRATION_REQUIRED"
	CodeTagConflict            = "RG_TAG_CONFLICT"
	CodeReleaseIncomplete      = "RG_RELEASE_INCOMPLETE"
	CodeProviderACKMissing     = "RG_PROVIDER_ACK_MISSING"
	CodeProviderFalseACK       = "RG_PROVIDER_FALSE_ACK"
	CodePlanStale              = "RG_PLAN_STALE"
	CodeRegistryConflict       = "RG_REGISTRY_CONFLICT"
	CodePreviousBlocking       = "RG_PREVIOUS_RELEASE_BLOCKING"
	CodeCanaryFailed           = "RG_CANARY_FAILED"
	CodeRolloutBlocked         = "RG_ROLLOUT_BLOCKED"
	// CodeBranchContractViolated means a production-operation branch failed
	// the production-operation branch contract (wrong base target, ancestry
	// not rooted at the current production-base HEAD, or out-of-scope change).
	CodeBranchContractViolated = "RG_BRANCH_CONTRACT_VIOLATED"
)

// Exit codes are part of the CLI contract: automation distinguishes "healthy",
// "repair available", "retry later", "needs a human" and "invariant broken"
// without parsing output.
const (
	ExitHealthy        = 0
	ExitSafeReconcile  = 10
	ExitTransientRetry = 20
	ExitBlocked        = 30
	ExitNeedsReview    = 40
	ExitBroken         = 50
	ExitUsage          = 2
)

// ExitCodeFor maps a health verdict to the documented exit code.
func ExitCodeFor(health Health) int {
	switch health {
	case HealthHealthy, HealthNoop, HealthWaived:
		return ExitHealthy
	case HealthRunning:
		return ExitTransientRetry
	case HealthACKPending:
		return ExitSafeReconcile
	case HealthRecoverable, HealthReady:
		return ExitBlocked
	case HealthDegraded:
		return ExitNeedsReview
	case HealthBroken:
		return ExitBroken
	case HealthNeedsReview:
		return ExitNeedsReview
	default:
		return ExitSafeReconcile
	}
}
