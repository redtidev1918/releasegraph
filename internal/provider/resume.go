package provider

import (
	"context"
	"fmt"

	"github.com/redtidev1918/releasegraph/internal/github"
)

// ResumeDraftPlan uses the current workflow with the immutable release source.
// This opt-in path is only for an interrupted draft, never a running publisher.
func ResumeDraftPlan(ctx context.Context, client *github.Bound, r *Report, workflow string) (string, map[string]string, error) {
	a := r.Observed.Actual
	if r.Verdict.HardFail || a.Waived || !a.ReleaseDraft || a.ReleaseExists || r.Context.Version == "" {
		return "", nil, fmt.Errorf("resume requires an unwaived interrupted draft without a tag conflict")
	}
	if !a.TagExists || a.ExpectedCommit == "" || a.TagCommit != a.ExpectedCommit {
		return "", nil, fmt.Errorf("resume requires the existing tag to match the inspected release commit")
	}
	commit, err := client.TagCommit(ctx, r.Context.Repository, r.Context.Tag)
	if err != nil {
		return "", nil, err
	}
	if commit != a.TagCommit {
		return "", nil, fmt.Errorf("release tag changed since inspection")
	}
	if workflow == "" {
		workflow = "release.yml"
	}
	runs, err := client.RecentWorkflowRuns(ctx, r.Context.Repository, workflow, 100)
	if err != nil {
		return "", nil, err
	}
	failed := false
	for _, run := range runs {
		if run.Event == "pull_request" {
			continue
		}
		if run.Status != "completed" {
			return "", nil, fmt.Errorf("release workflow %d is still %s", run.ID, run.Status)
		}
		if run.HeadSHA == commit && (run.Conclusion == "failure" || run.Conclusion == "cancelled" || run.Conclusion == "timed_out") {
			failed = true
		}
	}
	if !failed {
		return "", nil, fmt.Errorf("no failed release workflow found for the existing tag commit")
	}
	ref, err := client.DefaultBranch(ctx, r.Context.Repository)
	if err != nil {
		return "", nil, err
	}
	return ref, map[string]string{"version": string(r.Context.Version), "force": "true", "repair": "true", "source_ref": commit}, nil
}

// ResumeDraft revalidates the recovery plan immediately before dispatch.
func ResumeDraft(ctx context.Context, client *github.Bound, r *Report, workflow string, dryRun bool) (map[string]string, error) {
	ref, inputs, err := ResumeDraftPlan(ctx, client, r, workflow)
	if err != nil || dryRun {
		return inputs, err
	}
	if workflow == "" {
		workflow = "release.yml"
	}
	return inputs, client.DispatchWorkflow(ctx, r.Context.Repository, workflow, ref, inputs)
}
