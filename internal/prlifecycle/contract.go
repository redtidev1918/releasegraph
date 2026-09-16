// Package prlifecycle classifies open pull requests against a repository's
// pull-request lifecycle contract.
//
// An open pull request is a merge candidate, not a work tracker. This package
// answers exactly one question per pull request: is it still part of the merge
// queue? Everything that is not is either already landed (OBSOLETE) or paused
// (PARKED), and both of those leave the queue.
//
// Classification is a pure function of an observation, exactly like
// branchcontract: the provider layer builds the observation, this package
// decides, and plan.go turns decisions into a closed set of mutations. The
// package performs no I/O and holds no credentials, so every rule below is
// testable without a network.
package prlifecycle

import (
	"fmt"
	"strings"
	"time"

	"github.com/redtidev1918/releasegraph/internal/glob"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

// State is the lifecycle verdict for one open pull request.
type State string

const (
	// StateRelease is a managed publication pull request (release-please,
	// dependabot or another exempt actor). It is allowed to stay open: it is a
	// publish queue, not a merge queue.
	StateRelease State = "RELEASE"
	// StateKeepOpen is a pull request a human retained with an exempt label.
	// It is never touched; the label is the only escape hatch.
	StateKeepOpen State = "KEEP_OPEN"
	// StateActive is a pull request that is still being worked on, or whose
	// checks are still running.
	StateActive State = "ACTIVE"
	// StateMergeReady is a green, mergeable pull request waiting for a human
	// to merge or close it.
	StateMergeReady State = "MERGE_READY"
	// StateObsolete is a pull request whose commits are already contained in
	// its base branch: there is nothing left to merge.
	StateObsolete State = "OBSOLETE"
	// StateParked is a pull request that is paused: still a draft, or
	// conflicting, and inactive past the window.
	StateParked State = "PARKED"
	// StateAttention is a pull request that needs a human decision but is not
	// safe to act on automatically (for example a long-inactive pull request
	// whose checks are failing).
	StateAttention State = "ATTENTION"
)

// States lists every state, in report order. It exists so a report and its
// tests cannot silently disagree about the vocabulary.
func States() []State {
	return []State{StateRelease, StateKeepOpen, StateActive, StateMergeReady, StateObsolete, StateParked, StateAttention}
}

// Mergeability is the merge verdict GitHub reports for a pull request, in this
// package's vocabulary.
type Mergeability string

const (
	// MergeClean means the pull request has no conflict with its base.
	MergeClean Mergeability = "clean"
	// MergeConflicting means the head conflicts with the base branch.
	MergeConflicting Mergeability = "conflicting"
	// MergeBlocked means a required review or required check is missing.
	MergeBlocked Mergeability = "blocked"
	// MergeBehind means the base branch moved ahead; the head is still
	// mergeable but is not up to date.
	MergeBehind Mergeability = "behind"
	// MergeDraft means the pull request is a draft.
	MergeDraft Mergeability = "draft"
	// MergeUnstable means the head is mergeable but a check is failing or
	// still running.
	MergeUnstable Mergeability = "unstable"
	// MergeUnknown means GitHub has not computed (or cannot compute) the
	// merge verdict yet.
	MergeUnknown Mergeability = "unknown"
)

// MergeabilityFromProvider maps GitHub's mergeable_state vocabulary onto this
// package's. The provider vocabulary stays in the provider layer; this is the
// only place the two are related, so an unknown value can never be silently
// treated as clean.
func MergeabilityFromProvider(state string) Mergeability {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "clean":
		return MergeClean
	case "dirty":
		return MergeConflicting
	case "blocked":
		return MergeBlocked
	case "behind":
		return MergeBehind
	case "draft":
		return MergeDraft
	case "unstable", "has_hooks":
		return MergeUnstable
	default:
		return MergeUnknown
	}
}

// Checks is the combined CI verdict for a pull request head.
type Checks string

const (
	ChecksPassing Checks = "passing"
	ChecksFailing Checks = "failing"
	ChecksPending Checks = "pending"
	// ChecksNone means no verdict is available, either because no checks ran
	// or because the provider reported a value this contract does not know.
	// It is deliberately not a synonym for "passing": everything that depends
	// on checks being green also depends on GitHub's mergeable_state, which is
	// where required checks are actually enforced.
	ChecksNone Checks = "none"
)

// ChecksFromProvider maps the provider's check verdict onto this package's.
// Like MergeabilityFromProvider it is the only bridge between the two
// vocabularies, so a new provider value degrades to ChecksNone instead of
// silently becoming "green".
func ChecksFromProvider(state string) Checks {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "passing":
		return ChecksPassing
	case "failing":
		return ChecksFailing
	case "pending":
		return ChecksPending
	default:
		return ChecksNone
	}
}

// PullRequest is the observation a classification is a pure function of. It
// carries no provider payload: only what the rules below actually read.
type PullRequest struct {
	Number  int
	Title   string
	HeadRef string
	HeadSHA string
	Author  string
	BaseRef string
	Draft   bool
	Labels  []string
	// UpdatedAt is the last time the pull request changed at all (push,
	// comment, label, review). It is what "inactive" is measured from.
	UpdatedAt time.Time
	// Mergeable is the merge verdict.
	Mergeable Mergeability
	// Checks is the combined CI verdict for HeadSHA.
	Checks Checks
	// CommitsAheadOfBase counts the commits the head carries that the base
	// branch does not. ChangedFiles counts the files those commits actually
	// change: a non-zero commit count with no changed files means the content
	// is already in the base, which is the second shape of an obsolete pull
	// request. CompareKnown reports whether the comparison was available at
	// all: without it the zero values would wrongly mean "the change already
	// landed", which is the one classification that closes a pull request
	// without archiving it first.
	CommitsAheadOfBase int
	ChangedFiles       int
	CompareKnown       bool
}

// Labeled reports whether the pull request carries one of the labels.
func (pr PullRequest) Labeled(labels []string) bool {
	for _, label := range pr.Labels {
		for _, want := range labels {
			if strings.EqualFold(strings.TrimSpace(label), strings.TrimSpace(want)) {
				return true
			}
		}
	}
	return false
}

// Input is one pull request plus the contract to classify it against.
type Input struct {
	Repository  string
	PullRequest PullRequest
	// Lifecycle is the declared contract. nil means the repository declared no
	// contract: classification still runs (so fleet reports can show what
	// would happen) but every result is non-actionable, because plan.go only
	// mutates where a contract is declared and enabled.
	Lifecycle *policy.PRLifecycle
	Now       time.Time
}

// Result is the verdict for one pull request.
type Result struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	HeadRef    string `json:"headRef"`
	HeadSHA    string `json:"headSha"`
	BaseRef    string `json:"baseRef"`
	Author     string `json:"author"`
	Draft      bool   `json:"draft"`
	State      State  `json:"state"`
	// Reason is the specific rule that decided the state, in one line.
	Reason string `json:"reason"`
	// InactiveDays is how long the pull request has been untouched.
	InactiveDays int `json:"inactiveDays"`
	// Actionable reports whether this pull request should leave the merge
	// queue. It is true only for OBSOLETE and PARKED, and only when the
	// contract is declared and enabled.
	Actionable bool `json:"actionable"`
	// KeepBranch is always true and always reported: a pull request that
	// leaves the queue leaves its branch behind, so the work stays reachable.
	KeepBranch bool `json:"keepBranch"`
}

// Evaluate classifies one pull request. It never returns an error: an
// incomplete observation degrades to a more conservative state (ATTENTION or
// ACTIVE) rather than to an action.
func Evaluate(in Input) Result {
	pr := in.PullRequest
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	result := Result{
		Repository: in.Repository,
		Number:     pr.Number,
		Title:      pr.Title,
		HeadRef:    pr.HeadRef,
		HeadSHA:    pr.HeadSHA,
		BaseRef:    pr.BaseRef,
		Author:     pr.Author,
		Draft:      pr.Draft,
		KeepBranch: true,
	}
	if !pr.UpdatedAt.IsZero() {
		if elapsed := now.Sub(pr.UpdatedAt); elapsed > 0 {
			result.InactiveDays = int(elapsed.Hours() / 24)
		}
	}
	result.State, result.Reason = classify(in, result.InactiveDays)
	result.Actionable = (result.State == StateObsolete || result.State == StateParked) && in.Lifecycle.IsEnabled()
	return result
}

// classify applies the rule chain in order. The order is the contract: an
// exemption outranks every other rule, "still moving" outranks "done", and
// "already landed" outranks "ready to merge" because a pull request with
// nothing left to merge is not a merge candidate at all.
func classify(in Input, inactiveDays int) (State, string) {
	pr := in.PullRequest
	lifecycle := in.Lifecycle
	// ParkedAfter tolerates a nil contract, so an undeclared repository is
	// still classified with the documented default window.
	window := lifecycle.ParkedAfter()
	var exempt policy.PRExemptions
	if lifecycle != nil {
		exempt = lifecycle.Exempt
	}

	if pattern, ok := matchBranch(exempt.Branches, pr.HeadRef); ok {
		return StateRelease, fmt.Sprintf("head branch %q is exempt (%s)", pr.HeadRef, pattern)
	}
	if actor, ok := matchActor(exempt.Actors, pr.Author); ok {
		return StateRelease, fmt.Sprintf("author %q is exempt (%s)", pr.Author, actor)
	}
	if label, ok := matchLabel(exempt.Labels, pr); ok {
		return StateKeepOpen, fmt.Sprintf("labelled %q by a human; the contract never touches it", label)
	}
	if pr.Checks == ChecksPending {
		return StateActive, "checks are still running"
	}
	if inactiveDays < window {
		return StateActive, fmt.Sprintf("updated %d day(s) ago, inside the %d day window", inactiveDays, window)
	}
	if pr.CompareKnown && pr.CommitsAheadOfBase == 0 {
		return StateObsolete, fmt.Sprintf("no commits beyond %s: the change already landed", baseRef(pr))
	}
	if pr.CompareKnown && pr.ChangedFiles == 0 {
		return StateObsolete, fmt.Sprintf("no file difference against %s: the change already landed by another route", baseRef(pr))
	}
	if !pr.Draft && pr.Mergeable == MergeClean && pr.Checks != ChecksFailing {
		return StateMergeReady, "green and mergeable, waiting for a human to merge or close"
	}
	if pr.Draft {
		return StateParked, fmt.Sprintf("still a draft and inactive for %d day(s)", inactiveDays)
	}
	if pr.Mergeable == MergeConflicting {
		return StateParked, fmt.Sprintf("conflicts with %s and inactive for %d day(s)", baseRef(pr), inactiveDays)
	}
	return StateAttention, fmt.Sprintf("inactive for %d day(s) with %s mergeability and %s checks", inactiveDays, mergeLabel(pr.Mergeable), checkLabel(pr.Checks))
}

func baseRef(pr PullRequest) string {
	if pr.BaseRef == "" {
		return "the base branch"
	}
	return pr.BaseRef
}

func mergeLabel(value Mergeability) string {
	if value == "" {
		return string(MergeUnknown)
	}
	return string(value)
}

func checkLabel(value Checks) string {
	if value == "" {
		return string(ChecksNone)
	}
	return string(value)
}

func matchBranch(patterns []string, branch string) (string, bool) {
	if branch == "" {
		return "", false
	}
	for _, pattern := range patterns {
		// Patterns are validated when the policy loads (policy.Validate calls
		// glob.Check), so a match error cannot happen here; it is treated as a
		// non-match rather than as an exemption.
		if matched, err := glob.Match(pattern, branch); err == nil && matched {
			return pattern, true
		}
	}
	return "", false
}

func matchActor(actors []string, author string) (string, bool) {
	if author == "" {
		return "", false
	}
	for _, actor := range actors {
		if strings.EqualFold(actor, author) {
			return actor, true
		}
	}
	return "", false
}

func matchLabel(labels []string, pr PullRequest) (string, bool) {
	for _, label := range labels {
		for _, actual := range pr.Labels {
			if strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(label)) {
				return label, true
			}
		}
	}
	return "", false
}

// Summarize counts results per state, in report order.
func Summarize(results []Result) map[State]int {
	counts := map[State]int{}
	for _, state := range States() {
		counts[state] = 0
	}
	for _, result := range results {
		counts[result.State]++
	}
	return counts
}

// Actionable returns the results that should leave the merge queue, in the
// order they were observed (most recently updated first).
func Actionable(results []Result) []Result {
	actionable := []Result{}
	for _, result := range results {
		if result.Actionable {
			actionable = append(actionable, result)
		}
	}
	return actionable
}
