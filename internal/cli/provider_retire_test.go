package cli

import (
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/prlifecycle"
	"testing"
)

func TestRetireReleasePreservesContext(t *testing.T) {
	pr := github.OpenPullRequest{Number: 17, Title: "chore(master): release 3.1.0"}
	pr.Head.Ref = "release-please--branches--master"
	pr.Head.Sha = "abc"
	pr.Base.Ref = "master"
	actions, err := retirementActions("acme/app", pr, "3.1.0", "3.1.1")
	if err != nil {
		t.Fatal(err)
	}
	for i, kind := range []prlifecycle.ActionKind{prlifecycle.ActionEnsureIssue, prlifecycle.ActionComment, prlifecycle.ActionClosePullRequest} {
		if actions[i].Kind != kind {
			t.Fatalf("unsafe order: %v", actions)
		}
	}
	for _, replacement := range []string{"3.1.0", "3.0.9", "3.2.0-rc.1", "bad"} {
		if _, err := retirementActions("acme/app", pr, "3.1.0", replacement); err == nil {
			t.Fatalf("accepted %s", replacement)
		}
	}
	pr.Labels = []github.IssueLabel{{Name: "keep-open"}}
	if _, err := retirementActions("acme/app", pr, "3.1.0", "3.1.1"); err == nil {
		t.Fatal("ignored keep-open")
	}
}
