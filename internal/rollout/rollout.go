// Package rollout moves business repositories from one ReleaseGraph version to
// another using immutable refs.
//
// A mutable channel alias such as @v1 gives a new ReleaseGraph version to the
// whole fleet the moment it is pushed. Rollout replaces that with an explicit,
// reviewable pin: the canary repository is upgraded first, and the fleet only
// follows once the canary has completed a release lifecycle on the new version.
package rollout

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

// WorkflowPath is the release workflow businesses call and the file rollout edits.
const WorkflowPath = ".github/workflows/release.yml"

// MutableChannel matches channel aliases such as v1 or v2. These are convenient
// but give no blast-radius control, so they are reported, never silently kept.
var MutableChannel = regexp.MustCompile(`^v\d+$`)

// Pin is the ReleaseGraph version a repository currently calls.
type Pin struct {
	// Ref is the value after "@" in the uses: line. Empty when the workflow does
	// not call ReleaseGraph at all.
	Ref string `json:"ref"`
	// Uses is the full uses: reference for auditability.
	Uses string `json:"uses,omitempty"`
	// Mutable reports whether Ref is a channel alias instead of an exact version.
	Mutable bool `json:"mutable"`
	// Exact reports whether Ref looks like an immutable version tag.
	Exact bool `json:"exact"`
	// Version is the human-readable version recorded next to the pin, either
	// from an exact tag ref or from the trailing "# ReleaseGraph vX.Y.Z" comment
	// that commit pins carry.
	Version string `json:"version,omitempty"`
}

var usesPattern = regexp.MustCompile(`uses:\s*([^\s#]+)`)

// ParsePin extracts the ReleaseGraph pin from a release workflow definition.
func ParsePin(workflow []byte) Pin {
	for _, line := range strings.Split(string(workflow), "\n") {
		match := usesPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		uses := match[1]
		if !strings.Contains(uses, "releasegraph/") || !strings.Contains(uses, "reusable-release.yml") {
			continue
		}
		ref := ""
		if idx := strings.LastIndex(uses, "@"); idx >= 0 {
			ref = uses[idx+1:]
		}
		version := ""
		if comment := pinComment.FindStringSubmatch(line); comment != nil {
			version = comment[1]
		}
		if version == "" && semverTag.MatchString(ref) {
			version = ref
		}
		return Pin{Ref: ref, Uses: uses, Mutable: MutableChannel.MatchString(ref), Exact: semverTag.MatchString(ref), Version: version}
	}
	return Pin{}
}

var semverTag = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)

// pinComment reads the version recorded beside a commit pin.
var pinComment = regexp.MustCompile(`#\s*ReleaseGraph\s+(v\d+\.\d+\.\d+)`)

// Status of one repository in a rollout plan.
const (
	StatusCurrent      = "CURRENT"         // already on the target version
	StatusReady        = "READY"           // may be upgraded now
	StatusCanaryFirst  = "CANARY_REQUIRED" // the canary must pass before this one moves
	StatusBlocked      = "BLOCKED"         // the canary has not passed
	StatusNoPin        = "NO_PIN"          // no ReleaseGraph call found
	StatusUnmanaged    = "UNMANAGED"       // not declared in fleet.yaml
	StatusUnaffected   = "UNAFFECTED"      // release does not touch this repository's capabilities
	StatusMigration    = "MIGRATION_REQUIRED"
	StatusIncompatible = "INCOMPATIBLE"
)

// Entry is one repository's rollout state.
type Entry struct {
	Repository     string `json:"repository"`
	Classification string `json:"classification"`
	Canary         bool   `json:"canary,omitempty"`
	Current        Pin    `json:"current"`
	Target         string `json:"target"`
	// TargetCommit is the commit the target version resolves to; a commit pin
	// equal to it counts as "already on target".
	TargetCommit string `json:"targetCommit,omitempty"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	// Capabilities are the repository's own declared capabilities, read from its
	// policy. Empty means "could not be read", which is treated as affected.
	Capabilities []string `json:"capabilities,omitempty"`
	PolicySchema int      `json:"policySchema,omitempty"`
	Compat       string   `json:"compatibility,omitempty"`
}

// Plan is the full rollout decision for one target version.
type Plan struct {
	Target       string `json:"target"`
	Canary       string `json:"canary,omitempty"`
	CanaryPassed bool   `json:"canaryPassed"`
	CanaryReason string `json:"canaryReason,omitempty"`
	// Compatibility of the target ReleaseGraph release, when it declares one.
	Compatibility *Compatibility `json:"compatibility,omitempty"`
	Entries       []Entry        `json:"entries"`
	Ready         []string       `json:"ready"`
	Blocked       []string       `json:"blocked"`
	// Unaffected repositories are not upgraded: this release does not touch any
	// capability they declare.
	Unaffected []string `json:"unaffected"`
	// Migration and Incompatible repositories cannot simply take the new pin.
	Migration    []string `json:"migrationRequired"`
	Incompatible []string `json:"incompatible"`
}

// CanaryEvidence is what a canary repository proves before a fleet rollout.
type CanaryEvidence struct {
	Pinned    bool   `json:"pinned"`
	Lifecycle bool   `json:"lifecyclePassed"`
	Reason    string `json:"reason,omitempty"`
}

// BuildPlan decides, for every managed repository, whether it may move to target.
//
// Invariant: the fleet stays blocked until the canary repository runs the target
// version and its latest release lifecycle succeeded. Rollout never assumes that
// a published version is a verified version.
func BuildPlan(manifest *fleet.Manifest, entries []Entry, target, canary string, evidence CanaryEvidence, compat Compatibility) Plan {
	plan := Plan{Target: target, Canary: canary, CanaryPassed: evidence.Pinned && evidence.Lifecycle, CanaryReason: evidence.Reason, Compatibility: &compat, Entries: []Entry{}, Ready: []string{}, Blocked: []string{}, Unaffected: []string{}, Migration: []string{}, Incompatible: []string{}}

	for i := range entries {
		entry := &entries[i]
		switch entry.Classification {
		case fleet.ClassificationManaged:
		case fleet.ClassificationArchived, fleet.ClassificationDisabled:
			entry.Status = StatusUnmanaged
			entry.Reason = "not operable (" + entry.Classification + ")"
			plan.Entries = append(plan.Entries, *entry)
			continue
		default:
			entry.Status = StatusUnmanaged
			entry.Reason = "not declared in fleet.yaml"
			plan.Entries = append(plan.Entries, *entry)
			continue
		}

		// Capability-aware filtering: a fix that changes one capability must not
		// ask unrelated repositories to upgrade. Unknown capabilities are treated
		// as affected, never as an excuse to skip.
		if entry.PolicySchema > 0 || len(entry.Capabilities) > 0 {
			compatStatus, reason := Affected(entry.Capabilities, compat, entry.PolicySchema)
			entry.Compat = compatStatus
			switch compatStatus {
			case CompatIncompatible:
				entry.Status = StatusIncompatible
				entry.Reason = reason
				plan.Incompatible = append(plan.Incompatible, entry.Repository)
				plan.Entries = append(plan.Entries, *entry)
				continue
			case CompatMigrationRequired:
				entry.Status = StatusMigration
				entry.Reason = reason
				plan.Migration = append(plan.Migration, entry.Repository)
				plan.Entries = append(plan.Entries, *entry)
				continue
			case CompatUnaffected:
				entry.Status = StatusUnaffected
				entry.Reason = reason
				plan.Unaffected = append(plan.Unaffected, entry.Repository)
				plan.Entries = append(plan.Entries, *entry)
				continue
			}
		}

		switch {
		case entry.Current.Ref == "":
			entry.Status = StatusNoPin
			entry.Reason = "no ReleaseGraph reusable workflow call found"
		case entry.Current.PinnedTo(entry.Target, entry.TargetCommit):
			entry.Status = StatusCurrent
			entry.Reason = "already on " + entry.Target
		case entry.Canary:
			entry.Status = StatusReady
			entry.Reason = "canary upgrade is always allowed first"
		case !plan.CanaryPassed:
			entry.Status = StatusBlocked
			entry.Reason = "canary " + canary + " has not passed on " + entry.Target
			plan.Blocked = append(plan.Blocked, entry.Repository)
		default:
			entry.Status = StatusReady
			entry.Reason = "canary passed on " + entry.Target
		}
		if entry.Status == StatusReady {
			plan.Ready = append(plan.Ready, entry.Repository)
		}
		plan.Entries = append(plan.Entries, *entry)
	}
	return plan
}

// InspectPins reads every managed repository's release workflow pin.
func InspectPins(ctx context.Context, client *github.Bound, manifest *fleet.Manifest, managed []fleet.Resolved, target string) []Entry {
	entries := make([]Entry, 0, len(managed))
	for _, repo := range managed {
		entry := Entry{Repository: repo.Name, Classification: repo.Classification, Canary: repo.Canary, Target: target}
		// The repository's own contract decides whether this release affects it.
		if policyRaw, found, err := client.ReadFile(ctx, repo.Name, ".release-policy.yml", ""); err == nil && found {
			if parsed, err := policy.Parse(policyRaw); err == nil {
				entry.Capabilities = parsed.CapabilitiesOf().Names()
				entry.PolicySchema = 1
			}
		}
		raw, found, err := client.ReadFile(ctx, repo.Name, WorkflowPath, "")
		switch {
		case err != nil:
			entry.Current = Pin{}
			entry.Reason = err.Error()
		case !found:
			entry.Current = Pin{}
			entry.Reason = WorkflowPath + " is missing"
		default:
			entry.Current = ParsePin(raw)
		}
		entries = append(entries, entry)
	}
	return entries
}

// VersionOfRef normalises a tag or channel ref to a version string for display.
func VersionOfRef(ref string) string { return strings.TrimPrefix(ref, "v") }

var usesRefPattern = regexp.MustCompile(`(releasegraph/[^\s@#]*reusable-release\.yml@)([^\s#]+)(\s*#[^\n]*)?`)

// Repin rewrites the ReleaseGraph ref inside a workflow file. Anything that is
// not the ReleaseGraph reusable workflow call is left untouched, so comments and
// unrelated actions survive.
//
// The pin is the commit behind the version tag, with the version kept in a
// trailing comment: a commit ref always resolves for reusable workflows, while
// an annotated tag reference can fail the whole run at startup.
func Repin(workflow, version string) (string, error) {
	if !usesRefPattern.MatchString(workflow) {
		return "", fmt.Errorf("no ReleaseGraph reusable workflow reference found")
	}
	return RepinToCommit(workflow, version, "")
}

// RepinToCommit pins the ReleaseGraph call to an exact commit, recording the
// human-readable version in a comment.
func RepinToCommit(workflow, version, commit string) (string, error) {
	if !usesRefPattern.MatchString(workflow) {
		return "", fmt.Errorf("no ReleaseGraph reusable workflow reference found")
	}
	ref := version
	comment := ""
	if commit != "" {
		ref = commit
		comment = " # ReleaseGraph " + version
	}
	return usesRefPattern.ReplaceAllString(workflow, "${1}"+ref+comment), nil
}

// PinnedTo reports whether a pin targets a version, accepting either the version
// tag itself, the version recorded beside a commit pin, or the commit that the
// version resolves to.
func (p Pin) PinnedTo(version, commit string) bool {
	switch {
	case p.Ref == "":
		return false
	case p.Ref == version || p.Version == version:
		return true
	case commit != "" && p.Ref == commit:
		return true
	default:
		return false
	}
}
