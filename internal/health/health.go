// Package health is the Go half of the release-health contract.
//
// Health answers exactly one question: does the released state satisfy the
// policy? It is never an instruction. The Python half is
// release_infra/health.py, the published vocabulary is schemas/health-v1.json,
// and the value set is domain.RepoHealthValues. testdata/health/cases.json
// drives both implementations from one set of expectations, so the two
// languages cannot drift apart without a test failing in both CI lanes.
package health

import (
	"fmt"
	"strings"

	rgdomain "github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

// Every managed release carries this asset regardless of policy: it is the
// machine-readable record of the release transaction, so a release without it
// cannot be audited.
const MetadataAsset = "RELEASE-METADATA.json"

// Checksum manifest, required as soon as checksums are enabled (the default) and
// the policy declares required assets.
const ChecksumsAsset = "SHA256SUMS"

// Asset is one released asset. A required pattern is satisfied only by a
// matching asset with Size > 0, which is how a truncated upload looks and which
// the release planner also rejects.
type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Reason is why a health value was chosen, in machine-readable form.
type Reason struct {
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// Assessment is a health value plus the reasons that produced it.
type Assessment struct {
	Health        rgdomain.Health `json:"health"`
	Reasons       []Reason        `json:"reasons"`
	MissingAssets []string        `json:"missingAssets,omitempty"`
	EmptyAssets   []string        `json:"emptyAssets,omitempty"`
}

// OK reports whether the released state satisfies the policy.
func (a Assessment) OK() bool { return a.Health == rgdomain.HealthHealthy }

// Codes returns the reason codes in decision order.
func (a Assessment) Codes() []string {
	out := make([]string, 0, len(a.Reasons))
	for _, reason := range a.Reasons {
		out = append(out, reason.Code)
	}
	return out
}

// Observation is the facts a health decision is made from.
//
// The JSON names are snake_case so that testdata/health/cases.json drives this
// struct and release_infra.health.Observation verbatim, with no mapping layer to
// drift. Booleans are named so their zero value is the unremarkable case
// (TagDrift rather than "tag matches"), which is why the fixture states every
// field for every case instead of relying on defaults.
type Observation struct {
	Release           string  `json:"release"`
	DraftRelease      string  `json:"draft_release"`
	TagDrift          bool    `json:"tag_drift"`
	Assets            []Asset `json:"assets"`
	RunConclusion     string  `json:"run_conclusion"`
	HasPolicy         bool    `json:"has_policy"`
	PolicyParsable    bool    `json:"policy_parsable"`
	Archived          bool    `json:"archived"`
	Fork              bool    `json:"fork"`
	Unmanaged         bool    `json:"unmanaged"`
	APIError          string  `json:"api_error"`
	DependencyBlocked string  `json:"dependency_blocked"`
	CredentialMissing bool    `json:"credential_missing"`
	NeedsReview       bool    `json:"needs_review"`
}

// RequiredAssets is the required asset set, exactly as the release planner
// computes it: the policy's required assets, plus the mandatory metadata asset,
// plus the checksum manifest when checksums are enabled and the policy declares
// required assets. The Python planner and inventory compute the same set.
func RequiredAssets(p *policy.Policy) []string {
	if p == nil {
		return []string{MetadataAsset}
	}
	required := append([]string{}, p.Assets.Required...)
	required = appendUnique(required, MetadataAsset)
	if p.Checksums && len(p.Assets.Required) > 0 {
		required = appendUnique(required, ChecksumsAsset)
	}
	return required
}

func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// AssetGaps returns the required patterns that no released asset satisfies.
//
// A pattern is *missing* when no asset name matches it at all, and *empty* when
// it matches only zero-byte assets. The release planner collapses both into "not
// present" -- it builds its asset set from entries with Size > 0 -- so
// missing+empty is exactly its missing set, with the two different fixes kept
// apart: re-upload the asset, or fix an upload that produced nothing.
func AssetGaps(p *policy.Policy, assets []Asset) (missing, empty []string, err error) {
	missing, empty = []string{}, []string{}
	for _, pattern := range RequiredAssets(p) {
		if err := policy.ValidatePattern(pattern); err != nil {
			return nil, nil, err
		}
		matched := false
		nonEmpty := false
		for _, asset := range assets {
			ok, err := policy.MatchAsset(pattern, asset.Name)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				continue
			}
			matched = true
			if asset.Size > 0 {
				nonEmpty = true
				break
			}
		}
		switch {
		case !matched:
			missing = append(missing, pattern)
		case !nonEmpty:
			empty = append(empty, pattern)
		}
	}
	return missing, empty, nil
}

// okRunConclusions are workflow conclusions that do not indicate a failed
// release.
var okRunConclusions = map[string]bool{"": true, "success": true, "skipped": true}

// Assess decides health from a policy plus observed provider state.
//
// Precedence follows release_infra/health.py: an unreadable provider is BROKEN
// before anything else, an archived or forked repository is NO_RELEASE, and an
// unmanaged repository is UNMANAGED, because each makes every later judgement
// meaningless.
func Assess(p *policy.Policy, obs Observation) Assessment {
	if obs.APIError != "" {
		return Assessment{Health: rgdomain.HealthBroken, Reasons: []Reason{{Code: rgdomain.ReasonAPIError, Detail: obs.APIError}}}
	}
	if obs.Archived {
		return Assessment{Health: rgdomain.HealthNoRelease, Reasons: []Reason{{Code: rgdomain.ReasonRepositoryArchive}}}
	}
	if obs.Fork {
		return Assessment{Health: rgdomain.HealthNoRelease, Reasons: []Reason{{Code: rgdomain.ReasonRepositoryFork}}}
	}
	if obs.Unmanaged {
		return Assessment{Health: rgdomain.HealthUnmanaged, Reasons: []Reason{{Code: rgdomain.ReasonUnmanagedRepo}}}
	}
	if obs.CredentialMissing {
		return Assessment{Health: rgdomain.HealthBlocked, Reasons: []Reason{{Code: rgdomain.ReasonFleetCredential}}}
	}
	if obs.DependencyBlocked != "" {
		return Assessment{Health: rgdomain.HealthBlocked, Reasons: []Reason{{Code: rgdomain.ReasonDependencyUnhealthy, Detail: obs.DependencyBlocked}}}
	}

	reasons := []Reason{}
	if !obs.HasPolicy {
		reasons = append(reasons, Reason{Code: rgdomain.ReasonPolicyAbsent})
	} else if !obs.PolicyParsable {
		reasons = append(reasons, Reason{Code: rgdomain.ReasonPolicyUnparsable})
	}

	if obs.Release == "" {
		if obs.DraftRelease != "" {
			reasons = append(reasons, Reason{Code: rgdomain.ReasonReleaseDraft, Detail: obs.DraftRelease})
		} else {
			reasons = append(reasons, Reason{Code: rgdomain.ReasonReleaseAbsent})
		}
		return Assessment{Health: rgdomain.HealthNoRelease, Reasons: reasons}
	}

	missing, empty, err := AssetGaps(p, obs.Assets)
	if err != nil {
		// Mirrors release_infra.health: an unusable pattern is an unusable
		// policy, not a degraded release. Policies are validated when they are
		// loaded, so this only fires for a policy assembled in memory.
		return Assessment{Health: rgdomain.HealthBroken, Reasons: []Reason{{Code: rgdomain.ReasonPolicyUnparsable, Detail: err.Error()}}}
	}
	if len(missing) > 0 {
		reasons = append(reasons, Reason{Code: rgdomain.ReasonTargetAssetsMiss, Detail: strings.Join(missing, ", ")})
	}
	if len(empty) > 0 {
		reasons = append(reasons, Reason{Code: rgdomain.ReasonAssetEmpty, Detail: strings.Join(empty, ", ")})
	}
	if obs.TagDrift {
		reasons = append(reasons, Reason{Code: rgdomain.ReasonVersionDrift, Detail: obs.Release})
	}
	if obs.DraftRelease != "" {
		reasons = append(reasons, Reason{Code: rgdomain.ReasonReleaseDraft, Detail: obs.DraftRelease})
	}
	if !okRunConclusions[obs.RunConclusion] {
		reasons = append(reasons, Reason{Code: rgdomain.ReasonReleaseRunFailed, Detail: obs.RunConclusion})
	}

	if len(reasons) > 0 {
		return Assessment{Health: rgdomain.HealthDegraded, Reasons: reasons, MissingAssets: missing, EmptyAssets: empty}
	}
	if obs.NeedsReview {
		return Assessment{Health: rgdomain.HealthNeedsReview, Reasons: []Reason{{Code: rgdomain.ReasonManualReview}}}
	}
	return Assessment{Health: rgdomain.HealthHealthy, Reasons: []Reason{}}
}

// Describe renders an assessment for a human reader.
func Describe(a Assessment) string {
	if a.OK() {
		return string(a.Health)
	}
	parts := make([]string, 0, len(a.Reasons))
	for _, reason := range a.Reasons {
		if reason.Detail != "" {
			parts = append(parts, fmt.Sprintf("%s (%s)", reason.Code, reason.Detail))
			continue
		}
		parts = append(parts, reason.Code)
	}
	return fmt.Sprintf("%s: %s", a.Health, strings.Join(parts, "; "))
}
