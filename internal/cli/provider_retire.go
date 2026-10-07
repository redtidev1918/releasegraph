package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/prlifecycle"
)

var stableReleaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
var retiredReleaseTitle = regexp.MustCompile(`release[ :]+v?(\d+\.\d+[\w.\-+]*)`)

func newerStableVersion(newer, older string) bool {
	if !stableReleaseVersion.MatchString(newer) || !stableReleaseVersion.MatchString(older) {
		return false
	}
	a, b := strings.Split(newer, "."), strings.Split(older, ".")
	for i := range a {
		x, ex := strconv.ParseUint(a[i], 10, 64)
		y, ey := strconv.ParseUint(b[i], 10, 64)
		if ex != nil || ey != nil {
			return false
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func retirementActions(repo string, pr github.OpenPullRequest, version, replacement string) ([]prlifecycle.Action, error) {
	m := retiredReleaseTitle.FindStringSubmatch(pr.Title)
	if m == nil || m[1] != version || !strings.HasPrefix(pr.Head.Ref, "release-please--") || !newerStableVersion(replacement, version) {
		return nil, fmt.Errorf("retirement requires an obsolete release-please PR and a strictly newer stable published version")
	}
	for _, label := range pr.Labels {
		if label.Name == "keep-open" {
			return nil, fmt.Errorf("keep-open forbids retiring this PR")
		}
	}
	reason := fmt.Sprintf("release %s is superseded by published %s; preserve the running baseline", version, replacement)
	body := fmt.Sprintf("%s\n\n%s\n\nOriginal PR: https://github.com/%s/pull/%d\nBranch: `%s`\nHead: `%s`\nBase: `%s`\n\nThe branch and history are retained. Resume only after comparing against the current production base; merging this obsolete release can roll the version backwards.\n", prlifecycle.IssueMarker(pr.Number), reason, repo, pr.Number, pr.Head.Ref, pr.Head.Sha, pr.Base.Ref)
	return []prlifecycle.Action{
		{Kind: prlifecycle.ActionEnsureIssue, Repository: repo, Number: pr.Number, Title: fmt.Sprintf("Archived release PR #%d: %s", pr.Number, pr.Title), Body: body, Reason: reason},
		{Kind: prlifecycle.ActionComment, Repository: repo, Number: pr.Number, Body: reason + ". Engineering context archived in " + prlifecycle.PlaceholderIssue + "; branch and history retained.", Reason: reason},
		{Kind: prlifecycle.ActionClosePullRequest, Repository: repo, Number: pr.Number, Reason: reason},
	}, nil
}

func providerRetire(w io.Writer, opts providerOptions) error {
	if opts.repo == "" || opts.version == "" || opts.pr <= 0 || opts.supersededBy == "" {
		return fmt.Errorf("provider retire requires --repo, --version, --pr and --superseded-by")
	}
	ctx := context.Background()
	client, err := scanClientOnly(opts)
	if err != nil {
		return err
	}
	inspection := opts
	inspection.version = opts.supersededBy
	scan, err := scanTargets(ctx, inspection, client)
	if err != nil {
		return err
	}
	if len(scan.Reports) != 1 {
		return fmt.Errorf("cannot inspect replacement release: %v", scan.Errors)
	}
	actual := scan.Reports[0].Observed.Actual
	if !actual.TagExists || !actual.ReleaseExists || actual.ReleaseDraft || !actual.AssetsComplete || !actual.ChecksumsVerified || !actual.RegistriesHealthy {
		return fmt.Errorf("replacement must already be publicly released with complete artifacts and registries")
	}
	prs, err := client.OpenPullRequests(ctx, opts.repo)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		if pr.Number != opts.pr {
			continue
		}
		actions, err := retirementActions(opts.repo, pr, opts.version, opts.supersededBy)
		if err != nil {
			return err
		}
		for _, action := range actions {
			fmt.Fprintf(w, "%s: %s PR #%d (%s)\n", mode(opts.apply), action.Kind, pr.Number, action.Reason)
		}
		if opts.apply {
			_, err = prLifecycleApply(ctx, client, opts.repo, actions)
		}
		return err
	}
	return fmt.Errorf("release PR #%d is not open", opts.pr)
}
