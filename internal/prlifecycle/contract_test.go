package prlifecycle

import (
	"testing"
	"time"

	"github.com/redtidev1918/releasegraph/internal/policy"
)

var now = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

func contract() *policy.PRLifecycle {
	return &policy.PRLifecycle{
		Exempt: policy.PRExemptions{
			Branches: []string{"release-please--*"},
			Actors:   []string{"github-actions[bot]", "dependabot[bot]"},
			Labels:   []string{"keep-open"},
		},
	}
}

func disabledContract() *policy.PRLifecycle {
	enabled := false
	return &policy.PRLifecycle{Enabled: &enabled}
}

// pr builds an observation that is inactive, non-draft and green: the baseline
// every case below perturbs one field of.
func pr(mutate func(*PullRequest)) PullRequest {
	value := PullRequest{
		Number:             42,
		Title:              "fix(scheduler): dispatch HTTP triggers durably",
		HeadRef:            "fix/durable-slot-dispatch",
		HeadSHA:            "47210bd3",
		BaseRef:            "master",
		Author:             "hezzn",
		UpdatedAt:          now.AddDate(0, 0, -21),
		Mergeable:          MergeClean,
		Checks:             ChecksPassing,
		CommitsAheadOfBase: 5,
		ChangedFiles:       3,
		CompareKnown:       true,
	}
	if mutate != nil {
		mutate(&value)
	}
	return value
}

func TestEvaluateRuleChain(t *testing.T) {
	cases := []struct {
		name       string
		pull       PullRequest
		lifecycle  *policy.PRLifecycle
		want       State
		actionable bool
	}{
		{
			name: "release-please branch is a release queue entry",
			pull: pr(func(p *PullRequest) {
				p.HeadRef = "release-please--branches--master--components--PixivFlow"
				p.Draft = true
			}),
			lifecycle: contract(),
			want:      StateRelease,
		},
		{
			name:      "bot author is a release queue entry",
			pull:      pr(func(p *PullRequest) { p.Author = "github-actions[bot]" }),
			lifecycle: contract(),
			want:      StateRelease,
		},
		{
			name:      "keep-open label is never touched",
			pull:      pr(func(p *PullRequest) { p.Draft = true; p.Labels = []string{"keep-open"} }),
			lifecycle: contract(),
			want:      StateKeepOpen,
		},
		{
			name:      "running checks are still moving",
			pull:      pr(func(p *PullRequest) { p.Checks = ChecksPending; p.UpdatedAt = now.AddDate(0, 0, -30) }),
			lifecycle: contract(),
			want:      StateActive,
		},
		{
			name:      "recent activity is active even when ready",
			pull:      pr(func(p *PullRequest) { p.UpdatedAt = now.AddDate(0, 0, -2) }),
			lifecycle: contract(),
			want:      StateActive,
		},
		{
			name:      "green and mergeable waits for a human",
			pull:      pr(nil),
			lifecycle: contract(),
			want:      StateMergeReady,
		},
		{
			name:       "absorbed change is obsolete",
			pull:       pr(func(p *PullRequest) { p.CommitsAheadOfBase = 0 }),
			lifecycle:  contract(),
			want:       StateObsolete,
			actionable: true,
		},
		{
			// The other shape of obsolescence: the commits differ but the
			// content does not, which is what a change landed by another route
			// looks like.
			name:       "empty diff is obsolete",
			pull:       pr(func(p *PullRequest) { p.ChangedFiles = 0 }),
			lifecycle:  contract(),
			want:       StateObsolete,
			actionable: true,
		},
		{
			// The obsolescence rule outranks "ready to merge": a pull request
			// with nothing left to merge is not a merge candidate at all.
			name:       "obsolete outranks merge ready",
			pull:       pr(func(p *PullRequest) { p.CommitsAheadOfBase = 0; p.Mergeable = MergeClean; p.Checks = ChecksPassing }),
			lifecycle:  contract(),
			want:       StateObsolete,
			actionable: true,
		},
		{
			// Without a comparison there is no evidence that the change
			// landed, so the one classification that closes without
			// archiving must not be reachable.
			name:      "missing comparison is never obsolete",
			pull:      pr(func(p *PullRequest) { p.CommitsAheadOfBase = 0; p.CompareKnown = false }),
			lifecycle: contract(),
			want:      StateMergeReady,
		},
		{
			name:       "draft past the window parks",
			pull:       pr(func(p *PullRequest) { p.Draft = true }),
			lifecycle:  contract(),
			want:       StateParked,
			actionable: true,
		},
		{
			name:       "conflicting past the window parks",
			pull:       pr(func(p *PullRequest) { p.Mergeable = MergeConflicting }),
			lifecycle:  contract(),
			want:       StateParked,
			actionable: true,
		},
		{
			name:      "draft inside the window is still active",
			pull:      pr(func(p *PullRequest) { p.Draft = true; p.UpdatedAt = now.AddDate(0, 0, -1) }),
			lifecycle: contract(),
			want:      StateActive,
		},
		{
			name:      "conflict inside the window is still active",
			pull:      pr(func(p *PullRequest) { p.Mergeable = MergeConflicting; p.UpdatedAt = now.AddDate(0, 0, -1) }),
			lifecycle: contract(),
			want:      StateActive,
		},
		{
			// Failing checks are a fact about the code, not a reason to park
			// it: the contract refuses to close work a human must look at.
			name:      "failing checks need attention",
			pull:      pr(func(p *PullRequest) { p.Checks = ChecksFailing }),
			lifecycle: contract(),
			want:      StateAttention,
		},
		{
			name:      "blocked mergeability needs attention",
			pull:      pr(func(p *PullRequest) { p.Mergeable = MergeBlocked }),
			lifecycle: contract(),
			want:      StateAttention,
		},
		{
			name:      "undeclared contract reports but never acts",
			pull:      pr(func(p *PullRequest) { p.Draft = true }),
			lifecycle: nil,
			want:      StateParked,
		},
		{
			name:      "disabled contract reports but never acts",
			pull:      pr(func(p *PullRequest) { p.Draft = true }),
			lifecycle: disabledContract(),
			want:      StateParked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(Input{Repository: "redtidev1918/PixivFlow", PullRequest: tc.pull, Lifecycle: tc.lifecycle, Now: now})
			if got.State != tc.want {
				t.Fatalf("state=%s want=%s reason=%q", got.State, tc.want, got.Reason)
			}
			if got.Actionable != tc.actionable {
				t.Fatalf("actionable=%v want=%v state=%s", got.Actionable, tc.actionable, got.State)
			}
			if got.Reason == "" {
				t.Fatal("every verdict must carry the rule that decided it")
			}
			if !got.KeepBranch {
				t.Fatal("the contract never deletes a branch")
			}
		})
	}
}

func TestEvaluateWindowBoundary(t *testing.T) {
	// Seven days of inactivity is outside the seven day window; six is inside.
	inside := Evaluate(Input{PullRequest: pr(func(p *PullRequest) { p.UpdatedAt = now.AddDate(0, 0, -6) }), Lifecycle: contract(), Now: now})
	if inside.State != StateActive {
		t.Fatalf("6 days: state=%s reason=%q", inside.State, inside.Reason)
	}
	outside := Evaluate(Input{PullRequest: pr(func(p *PullRequest) { p.UpdatedAt = now.AddDate(0, 0, -7); p.Draft = true }), Lifecycle: contract(), Now: now})
	if outside.State != StateParked {
		t.Fatalf("7 days: state=%s reason=%q", outside.State, outside.Reason)
	}
	if outside.InactiveDays != 7 {
		t.Fatalf("inactiveDays=%d", outside.InactiveDays)
	}
}

func TestEvaluateCustomWindow(t *testing.T) {
	days := 30
	custom := contract()
	custom.ParkedAfterDays = &days
	got := Evaluate(Input{PullRequest: pr(func(p *PullRequest) { p.Draft = true }), Lifecycle: custom, Now: now})
	if got.State != StateActive {
		t.Fatalf("21 days inside a 30 day window: state=%s", got.State)
	}
}

func TestEvaluateNeverMutatesStateBeforeWindow(t *testing.T) {
	got := Evaluate(Input{Repository: "redtidev1918/PixivFlow", PullRequest: pr(nil), Lifecycle: contract(), Now: now})
	if got.Repository != "redtidev1918/PixivFlow" || got.Number != 42 || got.HeadSHA != "47210bd3" {
		t.Fatalf("result=%+v", got)
	}
}

func TestSummarizeCountsEveryState(t *testing.T) {
	results := []Result{
		{State: StateRelease},
		{State: StateRelease},
		{State: StateParked},
	}
	counts := Summarize(results)
	if len(counts) != len(States()) {
		t.Fatalf("counts=%v", counts)
	}
	if counts[StateRelease] != 2 || counts[StateParked] != 1 || counts[StateObsolete] != 0 {
		t.Fatalf("counts=%v", counts)
	}
}

func TestActionableFiltersAndPreservesOrder(t *testing.T) {
	results := []Result{
		{Number: 1, State: StateMergeReady, Actionable: false},
		{Number: 2, State: StateParked, Actionable: true},
		{Number: 3, State: StateObsolete, Actionable: true},
	}
	actionable := Actionable(results)
	if len(actionable) != 2 || actionable[0].Number != 2 || actionable[1].Number != 3 {
		t.Fatalf("actionable=%+v", actionable)
	}
}

// An unrecognised provider value must never be read as "clean": that would
// promote an unknown pull request straight to MERGE_READY.
func TestMergeabilityFromProvider(t *testing.T) {
	cases := map[string]Mergeability{
		"clean":     MergeClean,
		"dirty":     MergeConflicting,
		"blocked":   MergeBlocked,
		"behind":    MergeBehind,
		"draft":     MergeDraft,
		"unstable":  MergeUnstable,
		"has_hooks": MergeUnstable,
		"unknown":   MergeUnknown,
		"":          MergeUnknown,
		"garbage":   MergeUnknown,
		" CLEAN ":   MergeClean,
	}
	for input, want := range cases {
		if got := MergeabilityFromProvider(input); got != want {
			t.Errorf("mergeability(%q)=%s want=%s", input, got, want)
		}
	}
}

func TestLabeledIsCaseInsensitive(t *testing.T) {
	value := pr(func(p *PullRequest) { p.Labels = []string{"Keep-Open"} })
	if !value.Labeled([]string{"keep-open"}) {
		t.Fatal("label matching must ignore case")
	}
	if value.Labeled([]string{"other"}) {
		t.Fatal("unrelated labels must not match")
	}
}
