// Package provider reconciles version providers (release-please, manual, tag)
// against actual release state.
//
// Invariant: actual GitHub/registry state is the source of truth; provider
// acknowledgement state (e.g. the release-please "autorelease: tagged" label)
// is derived and is never allowed to mask an incomplete release.
package provider

import (
	"fmt"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

// Kind identifies a version provider.
type Kind string

const (
	KindReleasePlease Kind = "release-please"
	KindManual        Kind = "manual"
	KindTag           Kind = "tag"
)

// State is the provider-side acknowledgement state of one version.
type State string

const (
	// StatePending: provider still believes the merged release PR is untagged.
	StatePending State = "PENDING"
	// StateTriggered: provider fired a release trigger that has not settled.
	StateTriggered State = "TRIGGERED"
	// StateTagged: provider acknowledged the release as tagged/published.
	StateTagged State = "TAGGED"
	// StateNone: manual/tag providers carry no acknowledgement state.
	StateNone State = "NONE"
	// StateUnknown: provider state could not be read.
	StateUnknown State = "UNKNOWN"
)

// Drift is the classified relationship between actual and provider state.
type Drift string

const (
	DriftInSync                Drift = "PROVIDER_IN_SYNC"
	DriftACKMissing            Drift = "PROVIDER_ACK_MISSING"
	DriftFalseACK              Drift = "PROVIDER_FALSE_ACK"
	DriftTransactionIncomplete Drift = "RELEASE_TRANSACTION_INCOMPLETE"
	DriftTagMissing            Drift = "TAG_MISSING"
	DriftTagConflict           Drift = "TAG_CONFLICT"
	DriftReleaseMissing        Drift = "RELEASE_MISSING"
	DriftRegistryIncomplete    Drift = "REGISTRY_INCOMPLETE"
	DriftRegistryConflict      Drift = "REGISTRY_CONFLICT"
	DriftStateUnknown          Drift = "PROVIDER_STATE_UNKNOWN"
	// DriftHistoricalWaived is a human decision: the historical version will
	// never be repaired and must not block newer versions.
	DriftHistoricalWaived Drift = "HISTORICAL_WAIVED"
	// DriftReleaseInProgress means a draft release exists: the transaction is
	// still running, so neither ACK nor repair is appropriate yet.
	DriftReleaseInProgress Drift = "RELEASE_IN_PROGRESS"
)

// Actual is the observed real-world state of one version's release.
type Actual struct {
	// TagExists reports whether the version tag exists on the remote.
	TagExists bool
	// TagCommit is the commit the remote tag resolves to (peeled for annotated).
	TagCommit string
	// ExpectedCommit is the merge/release commit the tag must point at.
	ExpectedCommit string

	// ReleaseExists reports whether a non-draft GitHub Release exists.
	ReleaseExists bool
	// ReleaseDraft reports a draft release for this version. A draft means the
	// transaction is in flight: reporting it as incomplete would race the
	// release that is currently being published.
	ReleaseDraft bool
	// Latest is whether the release (when required) is marked latest.
	Latest bool

	// AssetsComplete reports all required assets present and non-empty.
	AssetsComplete bool
	// ChecksumsVerified reports the SHA256SUMS cover and match the assets.
	ChecksumsVerified bool

	// RegistriesHealthy reports all required registries published & verified.
	RegistriesHealthy bool

	// NoRegistryRequired means registry checks do not apply to this project.
	NoRegistryRequired bool

	// Waived is set when the merged release PR carries an explicit, auditable
	// human waiver label for a historical version.
	Waived bool
}

// Healthy reports whether the actual release satisfies the repository's own
// contract. It is capability-driven: a project without binaries, without a
// GitHub Release or without registries is healthy without them. Absence of an
// unrequired artifact is never a release failure.
func (a Actual) Healthy(caps policy.Capabilities) bool {
	if !a.TagExists || a.TagCommit != a.ExpectedCommit || a.ExpectedCommit == "" {
		return false
	}
	if caps.GitHubRelease {
		if !a.ReleaseExists || !a.Latest {
			return false
		}
	}
	if len(caps.Assets) > 0 && !a.AssetsComplete {
		return false
	}
	if caps.Checksums && !a.ChecksumsVerified {
		return false
	}
	if len(caps.Registries) > 0 && !a.RegistriesHealthy {
		return false
	}
	return true
}

// Observed bundles actual state with the provider's current acknowledgement and
// the release contract that state is judged against.
type Observed struct {
	Provider      Kind
	Version       domain.Version
	Actual        Actual
	ProviderState State
	// Capabilities is the contract: the current policy, or the contract recorded
	// in the release metadata when judging a historical release.
	Capabilities policy.Capabilities
}

// Verdict is the result of classifying one observed version.
type Verdict struct {
	Drift  Drift         `json:"drift"`
	Health domain.Health `json:"health"`
	// ACKAllowed is true only when the actual transaction is fully healthy.
	ACKAllowed bool `json:"ackAllowed"`
	// RepairSameVersion is true when the same version must be repaired before
	// any newer version may start.
	RepairSameVersion bool `json:"repairSameVersion"`
	// HardFail is true when history would have to be rewritten to recover.
	HardFail bool `json:"hardFail"`
	// Waived is true when a human accepted this historical version as-is.
	Waived bool   `json:"waived"`
	Reason string `json:"reason,omitempty"`
}

// Classify implements the drift state machine. Actual state always wins:
// a provider may never acknowledge an incomplete transaction, and a provider
// "tagged" label can never make an incomplete release look healthy.
func Classify(o Observed) Verdict {
	a := o.Actual

	// 1. Tag conflicts are permanent: never move/force an existing tag.
	if a.TagExists && a.ExpectedCommit != "" && a.TagCommit != a.ExpectedCommit {
		return Verdict{Drift: DriftTagConflict, Health: domain.HealthBroken, HardFail: true,
			Reason: fmt.Sprintf("tag points at %s, expected %s", a.TagCommit, a.ExpectedCommit)}
	}

	// 1a. A draft release means the transaction is in flight. Never ACK, never
	// repair, never start a new version: observe again later.
	if a.ReleaseDraft {
		return Verdict{Drift: DriftReleaseInProgress, Health: domain.HealthRunning,
			Reason: "a draft release exists; the release transaction is in flight"}
	}

	// 1b. A human waiver is explicit and auditable: it stops the version from
	// blocking newer ones, but it is never reported as HEALTHY.
	if a.Waived {
		return Verdict{Drift: DriftHistoricalWaived, Health: domain.HealthWaived, Waived: true,
			Reason: "waived by an explicit releasegraph: historical-waived label"}
	}

	// 2. Providers without acknowledgement state are in sync iff actual is healthy.
	caps := o.Capabilities
	if o.Provider == KindManual || o.Provider == KindTag || o.ProviderState == StateNone {
		if a.Healthy(caps) {
			return Verdict{Drift: DriftInSync, Health: domain.HealthHealthy, ACKAllowed: false}
		}
		return incomplete(a, caps)
	}

	if o.ProviderState == StateUnknown {
		// Do not guess. If the actual release is already healthy this is a
		// recoverable ACK miss; otherwise repair the transaction first.
		if a.Healthy(caps) {
			return Verdict{Drift: DriftStateUnknown, Health: domain.HealthACKPending, ACKAllowed: false,
				RepairSameVersion: true, Reason: "provider state unreadable; re-inspect then ACK"}
		}
		return incomplete(a, caps)
	}

	acknowledged := o.ProviderState == StateTagged
	healthy := a.Healthy(caps)

	switch {
	case healthy && acknowledged:
		return Verdict{Drift: DriftInSync, Health: domain.HealthHealthy}
	case healthy && !acknowledged:
		// The entire real release finished but the provider was never told.
		return Verdict{Drift: DriftACKMissing, Health: domain.HealthACKPending, ACKAllowed: true,
			RepairSameVersion: true}
	case !healthy && acknowledged:
		// Provider claims tagged while the real transaction is incomplete.
		return Verdict{Drift: DriftFalseACK, Health: domain.HealthDegraded, RepairSameVersion: true}
	default:
		return incomplete(a, caps)
	}
}

// incomplete maps an unfinished actual transaction to its specific drift. Each
// check is gated by the repository's own contract.
func incomplete(a Actual, caps policy.Capabilities) Verdict {
	recoverable := func(drift Drift) Verdict {
		return Verdict{Drift: drift, Health: domain.HealthRecoverable, RepairSameVersion: true}
	}
	switch {
	case !a.TagExists:
		return recoverable(DriftTagMissing)
	case caps.GitHubRelease && !a.ReleaseExists:
		return recoverable(DriftReleaseMissing)
	case len(caps.Assets) > 0 && !a.AssetsComplete:
		return recoverable(DriftTransactionIncomplete)
	case caps.Checksums && !a.ChecksumsVerified:
		return recoverable(DriftTransactionIncomplete)
	case len(caps.Registries) > 0 && !a.RegistriesHealthy:
		return recoverable(DriftRegistryIncomplete)
	case caps.GitHubRelease && !a.Latest:
		return recoverable(DriftTransactionIncomplete)
	default:
		return recoverable(DriftTransactionIncomplete)
	}
}

// LabelMutation is one idempotent change to a provider PR's labels.
type LabelMutation struct {
	Action string `json:"action"` // "add" or "remove"
	Label  string `json:"label"`
}

// PlanACK computes the idempotent label changes that acknowledge a healthy
// release. It returns no mutations (and no error) when already acknowledged.
// release-please labels come from its documented defaults; adapters may pass
// overrides for customised deployments.
func PlanACK(current []string, pendingLabels, releaseLabels []string) []LabelMutation {
	if releaseLabels == nil {
		releaseLabels = []string{"autorelease: tagged"}
	}
	if pendingLabels == nil {
		pendingLabels = []string{"autorelease: pending", "autorelease: triggered"}
	}
	have := map[string]bool{}
	for _, l := range current {
		have[l] = true
	}
	var mutations []LabelMutation
	for _, l := range pendingLabels {
		if have[l] {
			mutations = append(mutations, LabelMutation{Action: "remove", Label: l})
		}
	}
	for _, l := range releaseLabels {
		if !have[l] {
			mutations = append(mutations, LabelMutation{Action: "add", Label: l})
		}
	}
	return mutations
}

// CanProgressToNextVersion enforces serial progression: a newer version must
// never start while any recent prior version is not healthy and acknowledged.
func CanProgressToNextVersion(prev []Verdict) (bool, string) {
	for _, v := range prev {
		if v.Health == domain.HealthHealthy || v.Waived {
			continue
		}
		if v.HardFail {
			return false, "previous release has TAG_CONFLICT; manual intervention required"
		}
		return false, fmt.Sprintf("previous version is %s (%s); repair same version first", v.Health, v.Drift)
	}
	return true, ""
}
