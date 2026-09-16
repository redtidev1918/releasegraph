package prlifecycle

import (
	"strings"
	"testing"
)

func parkedResult() Result {
	return Evaluate(Input{Repository: "redtidev1918/PixivFlow", PullRequest: pr(func(p *PullRequest) { p.Draft = true }), Lifecycle: contract(), Now: now})
}

func obsoleteResult() Result {
	return Evaluate(Input{Repository: "redtidev1918/PixivFlow", PullRequest: pr(func(p *PullRequest) { p.CommitsAheadOfBase = 0 }), Lifecycle: contract(), Now: now})
}

func TestPlanIsEmptyWithoutEnabledContract(t *testing.T) {
	results := []Result{parkedResult(), obsoleteResult()}
	if actions := Plan(PlanInput{Repository: "o/r", Results: results, Lifecycle: nil}); len(actions) != 0 {
		t.Fatalf("an undeclared contract must plan nothing: %+v", actions)
	}
	if actions := Plan(PlanInput{Repository: "o/r", Results: results, Lifecycle: disabledContract()}); len(actions) != 0 {
		t.Fatalf("a disabled contract must plan nothing: %+v", actions)
	}
}

// A verdict produced without a contract is not actionable, so even a later run
// with an enabled contract must not act on it.
func TestPlanRequiresActionableVerdicts(t *testing.T) {
	verdict := Evaluate(Input{Repository: "o/r", PullRequest: pr(func(p *PullRequest) { p.Draft = true }), Lifecycle: nil, Now: now})
	if verdict.Actionable {
		t.Fatal("a verdict without a contract cannot be actionable")
	}
	actions := Plan(PlanInput{Repository: "o/r", Results: []Result{verdict}, Lifecycle: contract()})
	if len(actions) != 0 {
		t.Fatalf("non-actionable verdict produced actions: %+v", actions)
	}
}

// Archive-before-close is the invariant that makes a close non-destructive.
func TestPlanParkedArchivesBeforeClosing(t *testing.T) {
	actions := Plan(PlanInput{Repository: "redtidev1918/PixivFlow", Results: []Result{parkedResult()}, Lifecycle: contract()})
	want := []ActionKind{ActionEnsureIssue, ActionComment, ActionClosePullRequest}
	if len(actions) != len(want) {
		t.Fatalf("actions=%+v", actions)
	}
	for i, kind := range want {
		if actions[i].Kind != kind {
			t.Fatalf("action %d is %s, want %s (%+v)", i, actions[i].Kind, kind, actions)
		}
	}
	issue := actions[0]
	if issue.Title == "" || !strings.Contains(issue.Body, IssueMarker(42)) {
		t.Fatalf("archive issue must be findable again: %+v", issue)
	}
	if !strings.Contains(actions[1].Body, PlaceholderIssue) {
		t.Fatal("the parked comment must reference the archive issue")
	}
	for _, action := range actions {
		if action.Number != 42 {
			t.Fatalf("action targets %d, want the pull request 42", action.Number)
		}
	}
}

func TestPlanObsoleteClosesWithoutAnIssue(t *testing.T) {
	actions := Plan(PlanInput{Repository: "redtidev1918/PixivFlow", Results: []Result{obsoleteResult()}, Lifecycle: contract()})
	if len(actions) != 2 || actions[0].Kind != ActionComment || actions[1].Kind != ActionClosePullRequest {
		t.Fatalf("actions=%+v", actions)
	}
	if strings.Contains(actions[0].Body, "How to resume") {
		t.Fatal("landed work has nothing to resume")
	}
}

func TestPlanArchiveOnlyNeverCloses(t *testing.T) {
	closeParked := false
	archiveOnly := contract()
	archiveOnly.CloseParked = &closeParked
	actions := Plan(PlanInput{Repository: "o/r", Results: []Result{parkedResult()}, Lifecycle: archiveOnly})
	if len(actions) != 1 || actions[0].Kind != ActionEnsureIssue {
		t.Fatalf("actions=%+v", actions)
	}
}

func TestPlanIgnoresStatesThatStayInTheQueue(t *testing.T) {
	results := []Result{
		Evaluate(Input{Repository: "o/r", PullRequest: pr(nil), Lifecycle: contract(), Now: now}),
		Evaluate(Input{Repository: "o/r", PullRequest: pr(func(p *PullRequest) { p.Checks = ChecksPending }), Lifecycle: contract(), Now: now}),
		Evaluate(Input{Repository: "o/r", PullRequest: pr(func(p *PullRequest) { p.Checks = ChecksFailing }), Lifecycle: contract(), Now: now}),
		Evaluate(Input{Repository: "o/r", PullRequest: pr(func(p *PullRequest) { p.HeadRef = "release-please--x" }), Lifecycle: contract(), Now: now}),
		Evaluate(Input{Repository: "o/r", PullRequest: pr(func(p *PullRequest) { p.Labels = []string{"keep-open"} }), Lifecycle: contract(), Now: now}),
	}
	for _, result := range results {
		if result.State == StateParked || result.State == StateObsolete {
			t.Fatalf("fixture %s should stay in the queue", result.State)
		}
	}
	if actions := Plan(PlanInput{Repository: "o/r", Results: results, Lifecycle: contract()}); len(actions) != 0 {
		t.Fatalf("actions=%+v", actions)
	}
}

// The safety invariants are structural: the closed set contains no way to
// merge, delete a branch or rewrite history, so no policy can enable one.
func TestContractNeverAuthorizesMergeOrHistoryRewrite(t *testing.T) {
	want := map[ActionKind]bool{
		ActionEnsureIssue:      true,
		ActionComment:          true,
		ActionClosePullRequest: true,
	}
	kinds := ActionKinds()
	if len(kinds) != len(want) {
		t.Fatalf("action kinds changed: %v", kinds)
	}
	forbidden := []string{"merge", "delete", "branch", "rebase", "force", "push", "reset"}
	for _, kind := range kinds {
		if !want[kind] {
			t.Fatalf("unexpected action kind %q", kind)
		}
		for _, word := range forbidden {
			if strings.Contains(strings.ToLower(string(kind)), word) {
				t.Fatalf("action kind %q violates a safety invariant", kind)
			}
		}
	}
	for _, kind := range MutationKinds() {
		if kind == ActionClosePullRequest {
			continue
		}
		if !want[kind] {
			t.Fatalf("unexpected mutation kind %q", kind)
		}
	}
}

func TestRenderResolvesTheArchiveReference(t *testing.T) {
	if got := Render("Archived in: "+PlaceholderIssue, 56); got != "Archived in: #56" {
		t.Fatalf("render=%q", got)
	}
	if got := Render("Archived in: "+PlaceholderIssue, 0); strings.Contains(got, PlaceholderIssue) {
		t.Fatalf("render=%q", got)
	}
}

func TestIssueMarkerIsStable(t *testing.T) {
	if got := IssueMarker(42); got != "<!-- releasegraph:parked-pr:42 -->" {
		t.Fatalf("marker=%q", got)
	}
}

func TestArchiveIssueRecordsHowToResume(t *testing.T) {
	result := parkedResult()
	body := ArchiveIssueBody(result)
	for _, fragment := range []string{
		"#42",
		"https://github.com/redtidev1918/PixivFlow/pull/42",
		"`" + result.HeadRef + "`",
		"`47210bd3`",
		"`master`",
		"How to resume",
		IssueMarker(42),
	} {
		if !strings.Contains(body, fragment) {
			t.Errorf("archive issue body is missing %q", fragment)
		}
	}
	if strings.Contains(body, PlaceholderIssue) {
		t.Error("the archive issue is the thing being created; it cannot reference itself")
	}
}

func TestKeepOpenPoliciesStillClassifyParkedWork(t *testing.T) {
	// A keep-open label must win even when everything else says PARKED,
	// otherwise the escape hatch is useless exactly when it is needed.
	value := pr(func(p *PullRequest) {
		p.Draft = true
		p.Mergeable = MergeConflicting
		p.Labels = []string{"keep-open"}
		p.UpdatedAt = now.AddDate(0, 0, -365)
	})
	result := Evaluate(Input{Repository: "o/r", PullRequest: value, Lifecycle: contract(), Now: now})
	if result.State != StateKeepOpen || result.Actionable {
		t.Fatalf("result=%+v", result)
	}
	if actions := Plan(PlanInput{Repository: "o/r", Results: []Result{result}, Lifecycle: contract()}); len(actions) != 0 {
		t.Fatalf("keep-open pull requests must never be acted on: %+v", actions)
	}
}

func TestExplicitDisabledContractDeclaresWithoutActing(t *testing.T) {
	enabled := false
	declared := contract()
	declared.Enabled = &enabled
	result := Evaluate(Input{Repository: "o/r", PullRequest: pr(func(p *PullRequest) { p.Draft = true }), Lifecycle: declared, Now: now})
	if result.State != StateParked || result.Actionable {
		t.Fatalf("result=%+v", result)
	}
}
