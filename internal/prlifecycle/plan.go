package prlifecycle

import (
	"fmt"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/policy"
)

// ActionKind is the closed set of mutations the lifecycle contract may plan.
//
// The set is deliberately narrow. There is no "merge", no "delete_branch", no
// "update_branch" and no "rebase": a control plane that can merge on a human's
// behalf, delete a branch or rewrite history is a different product with a
// different risk profile. The lifecycle contract only ever removes an entry
// from the merge queue, records why, and leaves everything else reachable.
//
// TestContractNeverAuthorizesMergeOrHistoryRewrite asserts this set, so adding
// a mutation is a deliberate, reviewed act rather than an accident.
type ActionKind string

const (
	// ActionEnsureIssue archives parked work as an issue. It always runs before
	// the matching close, so a pull request never leaves the queue before its
	// engineering context is recorded.
	ActionEnsureIssue ActionKind = "ensure_issue"
	// ActionComment explains the verdict on the pull request itself.
	ActionComment ActionKind = "comment"
	// ActionClosePullRequest removes the entry from the merge queue. The branch
	// is never deleted.
	ActionClosePullRequest ActionKind = "close_pull_request"
)

// ActionKinds returns the closed set of mutations, in the order a plan applies
// them.
func ActionKinds() []ActionKind {
	return []ActionKind{ActionEnsureIssue, ActionComment, ActionClosePullRequest}
}

// MutationKinds returns the kinds that change state, as opposed to the comment
// that explains it. They are the kinds a dry run must be able to enumerate
// without performing.
func MutationKinds() []ActionKind {
	return []ActionKind{ActionEnsureIssue, ActionClosePullRequest}
}

// PlaceholderIssue is replaced at execution time by a reference to the issue
// that archives the pull request. It is a placeholder rather than a resolved
// number because the issue does not exist yet when the plan is produced.
const PlaceholderIssue = "{{issue}}"

// Action is one planned mutation.
type Action struct {
	Kind       ActionKind `json:"kind"`
	Repository string     `json:"repository"`
	// Number is the pull request number.
	Number int `json:"number"`
	// IssueNumber is set when an existing archive issue is reused instead of
	// created.
	IssueNumber int    `json:"issueNumber,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body,omitempty"`
	// Reason is the classification rule behind the action.
	Reason string `json:"reason"`
}

// PlanInput is the set of verdicts to turn into actions.
type PlanInput struct {
	Repository string
	Results    []Result
	Lifecycle  *policy.PRLifecycle
}

// Plan turns verdicts into the actions that remove non-merge-candidates from
// the queue.
//
// Plan is pure: it performs no I/O and holds no credential. A repository that
// has not declared and enabled the contract produces no actions at all, which
// is why a read-only fleet audit is safe to run everywhere while mutations are
// limited to repositories that asked for them.
func Plan(in PlanInput) []Action {
	if !in.Lifecycle.IsEnabled() {
		return nil
	}
	actions := []Action{}
	for _, result := range in.Results {
		// Two independent gates: the contract must be enabled, and the verdict
		// must have been produced as actionable. A verdict computed without a
		// contract can never be acted on by a later run.
		if !result.Actionable {
			continue
		}
		switch result.State {
		case StateParked:
			actions = append(actions, parkedActions(in.Repository, result, in.Lifecycle)...)
		case StateObsolete:
			actions = append(actions, obsoleteActions(in.Repository, result)...)
		}
	}
	return actions
}

// parkedActions archives then closes. Archive always precedes close, and
// policy validation guarantees archiveParkedToIssue is true whenever parked
// pull requests are closed.
func parkedActions(repo string, result Result, lifecycle *policy.PRLifecycle) []Action {
	actions := []Action{}
	if lifecycle.ArchivesToIssue() {
		actions = append(actions, Action{
			Kind:       ActionEnsureIssue,
			Repository: repo,
			Number:     result.Number,
			Title:      ArchiveIssueTitle(result),
			Body:       ArchiveIssueBody(result),
			Reason:     result.Reason,
		})
	}
	if !lifecycle.ClosesParked() {
		return actions
	}
	actions = append(actions,
		Action{
			Kind:       ActionComment,
			Repository: repo,
			Number:     result.Number,
			Body:       ParkedComment(result),
			Reason:     result.Reason,
		},
		Action{
			Kind:       ActionClosePullRequest,
			Repository: repo,
			Number:     result.Number,
			Reason:     result.Reason,
		},
	)
	return actions
}

// obsoleteActions closes only. There is nothing to resume: the commits are
// already contained in the base branch, so an archive issue would be noise.
// The comment is what makes the close explainable after the fact.
func obsoleteActions(repo string, result Result) []Action {
	return []Action{
		{
			Kind:       ActionComment,
			Repository: repo,
			Number:     result.Number,
			Body:       ObsoleteComment(result),
			Reason:     result.Reason,
		},
		{
			Kind:       ActionClosePullRequest,
			Repository: repo,
			Number:     result.Number,
			Reason:     result.Reason,
		},
	}
}

// Render resolves the placeholders a plan carries. It is the only place plan
// text becomes provider text; issueNumber == 0 means no archive issue exists,
// which happens when the contract archives to an issue is disabled.
func Render(body string, issueNumber int) string {
	reference := "an issue in this repository"
	if issueNumber > 0 {
		reference = fmt.Sprintf("#%d", issueNumber)
	}
	return strings.ReplaceAll(body, PlaceholderIssue, reference)
}

// IssueMarker is the stable, greppable marker that links an archive issue back
// to the pull request it preserves. It is how a second run reuses the issue
// instead of creating a duplicate.
func IssueMarker(number int) string {
	return fmt.Sprintf("<!-- releasegraph:parked-pr:%d -->", number)
}

// ArchiveIssueTitle names the archived work.
func ArchiveIssueTitle(result Result) string {
	title := strings.TrimSpace(result.Title)
	if title == "" {
		return fmt.Sprintf("Archived pull request #%d", result.Number)
	}
	return fmt.Sprintf("%s (from PR #%d)", title, result.Number)
}

// ArchiveIssueBody records everything needed to resume the work later without
// reading the closed pull request, plus the marker used for reuse.
func ArchiveIssueBody(result Result) string {
	var b strings.Builder
	b.WriteString("Parked pull request archived by the ReleaseGraph PR lifecycle contract.\n\n")
	b.WriteString("An open pull request is a merge candidate, not a work tracker. This work was\n")
	b.WriteString("paused, so the pull request left the merge queue and the context moved here.\n\n")
	fmt.Fprintf(&b, "- Original pull request: #%d (%s)\n", result.Number, pullRequestURL(result))
	if result.HeadRef != "" {
		fmt.Fprintf(&b, "- Branch: `%s` (kept; the contract never deletes branches)\n", result.HeadRef)
	}
	if result.HeadSHA != "" {
		fmt.Fprintf(&b, "- Last commit: `%s`\n", result.HeadSHA)
	}
	if result.BaseRef != "" {
		fmt.Fprintf(&b, "- Base at the time: `%s`\n", result.BaseRef)
	}
	fmt.Fprintf(&b, "- Classified: %s (%s)\n", result.State, result.Reason)
	fmt.Fprintf(&b, "- Inactive: %d day(s)\n", result.InactiveDays)
	b.WriteString("\n### How to resume\n\n")
	b.WriteString("1. Branch from the current default branch: the parked branch is a reference, not a base.\n")
	b.WriteString("2. Replay the change, using the original pull request and its branch as the source of truth.\n")
	b.WriteString("3. Validate locally (build, typecheck, tests) before opening a pull request.\n")
	b.WriteString("4. Open the pull request only once it is a merge candidate.\n")
	b.WriteString("\n")
	b.WriteString(IssueMarker(result.Number))
	b.WriteString("\n")
	return b.String()
}

// ParkedComment is posted on the pull request itself. It carries the
// PlaceholderIssue because the archive issue is created in the same run, just
// before this comment is posted.
func ParkedComment(result Result) string {
	var b strings.Builder
	b.WriteString("Leaving the ReleaseGraph merge queue: this pull request is **PARKED**.\n\n")
	fmt.Fprintf(&b, "- Why: %s\n", result.Reason)
	fmt.Fprintf(&b, "- Inactive: %d day(s)\n", result.InactiveDays)
	fmt.Fprintf(&b, "- Archived in: %s\n", PlaceholderIssue)
	if result.HeadRef != "" {
		fmt.Fprintf(&b, "- Branch `%s` is kept, not deleted.\n", result.HeadRef)
	}
	b.WriteString("\nThe work is not discarded: the archive issue holds the design, the branch and the last commit.\n")
	b.WriteString("To resume, branch from the current default branch and reopen a pull request once it is merge-ready.\n")
	b.WriteString("\n<!-- releasegraph:pr-lifecycle -->\n")
	return b.String()
}

// ObsoleteComment explains a close that is not a pause.
func ObsoleteComment(result Result) string {
	var b strings.Builder
	b.WriteString("Closing: this pull request is **OBSOLETE**.\n\n")
	fmt.Fprintf(&b, "- Why: %s\n", result.Reason)
	if result.HeadRef != "" {
		fmt.Fprintf(&b, "- Branch `%s` is kept, not deleted.\n", result.HeadRef)
	}
	b.WriteString("\nNothing is lost: there is no change left to merge. Any follow-up work belongs in a new issue.\n")
	b.WriteString("\n<!-- releasegraph:pr-lifecycle -->\n")
	return b.String()
}

func pullRequestURL(result Result) string {
	if result.Repository == "" || result.Number == 0 {
		return "pull request"
	}
	return fmt.Sprintf("https://github.com/%s/pull/%d", result.Repository, result.Number)
}
