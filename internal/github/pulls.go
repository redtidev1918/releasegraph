package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

// CheckState is the combined CI verdict for a commit, in provider vocabulary.
// The lifecycle contract maps it into its own vocabulary with
// prlifecycle.ChecksFromProvider, so the two never drift apart silently.
const (
	CheckStatePassing = "passing"
	CheckStateFailing = "failing"
	CheckStatePending = "pending"
	CheckStateNone    = "none"
)

// OpenPullRequest is one open pull request as the provider describes it. It is
// deliberately incomplete: the lifecycle observation is assembled from this
// plus CompareAheadBy and HeadCheckState, each of which is a separate call
// because the list endpoint carries neither.
type OpenPullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Draft  bool   `json:"draft"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []IssueLabel `json:"labels"`
	Head   struct {
		Ref string `json:"ref"`
		Sha string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// MergeableState is GitHub's mergeable_state: clean, dirty, blocked,
	// behind, draft, unstable, has_hooks or unknown. It is the authoritative
	// verdict on conflicts, required reviews and required checks.
	MergeableState string `json:"mergeable_state"`
}

// OpenPullRequests lists a repository's open pull requests, most recently
// updated first.
func (c *Client) OpenPullRequests(ctx context.Context, repo string) ([]OpenPullRequest, error) {
	path := fmt.Sprintf("repos/%s/pulls?state=open&sort=updated&direction=desc&per_page=100", repo)
	raw, err := c.Paginate(ctx, path)
	if err != nil {
		return nil, err
	}
	// Paginate returns generic maps; re-encode/decode into typed structs.
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var prs []OpenPullRequest
	if err := json.Unmarshal(buf, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

// Compare is the provider's answer for one base...head comparison.
type Compare struct {
	AheadBy int `json:"ahead_by"`
	// Files lists the changed files. An empty list alongside a non-zero
	// AheadBy means the commits differ but the content does not: the change
	// already landed by another route, which is one of the two shapes of an
	// obsolete pull request.
	Files []CompareFile `json:"files"`
}

// CompareFile is one entry of a comparison's file list.
type CompareFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
}

// CompareCommits compares head against base.
//
// found is false when the comparison cannot be made at all (a deleted branch,
// an unreachable ref). The lifecycle contract treats that as "no evidence",
// never as "already landed", because obsolescence is the one verdict that
// closes a pull request without archiving it first.
func (c *Client) CompareCommits(ctx context.Context, repo, base, head string) (Compare, bool, error) {
	var compare Compare
	// Ref names are interpolated, not escaped: branch names contain slashes
	// and the compare endpoint expects them literally in the basehead term.
	path := fmt.Sprintf("repos/%s/compare/%s...%s", repo, base, head)
	found, err := c.GetOptional(ctx, path, &compare)
	if err != nil || !found {
		return Compare{}, false, err
	}
	return compare, true, nil
}

// HeadCheckState combines a commit's check runs into one verdict.
//
// The legacy commit-status API is not consulted: every managed repository
// reports CI through Actions check runs, and mixing the two vocabularies is
// how "green" starts meaning two different things.
func (c *Client) HeadCheckState(ctx context.Context, repo, sha string) (string, error) {
	var payload struct {
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	path := fmt.Sprintf("repos/%s/commits/%s/check-runs?per_page=100", repo, sha)
	if err := c.Get(ctx, path, &payload); err != nil {
		return "", err
	}
	if len(payload.CheckRuns) == 0 {
		return CheckStateNone, nil
	}
	failing := false
	for _, run := range payload.CheckRuns {
		if run.Status != "completed" {
			return CheckStatePending, nil
		}
		switch run.Conclusion {
		case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
			failing = true
		}
	}
	if failing {
		return CheckStateFailing, nil
	}
	return CheckStatePassing, nil
}

// Issue is one issue as the provider describes it, including the marker the
// lifecycle contract greps for.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	// PullRequest is non-nil when the entry is really a pull request: the
	// issues endpoint returns both.
	PullRequest *struct{} `json:"pull_request"`
}

// OpenIssues lists a repository's open issues, most recently updated first.
//
// Archive-issue reuse reads the list rather than the search API: the list
// endpoint returns bodies directly, needs no indexing delay, and cannot be
// rate-limited separately from everything else here.
func (c *Client) OpenIssues(ctx context.Context, repo string) ([]Issue, error) {
	path := fmt.Sprintf("repos/%s/issues?state=open&sort=updated&direction=desc&per_page=100", repo)
	raw, err := c.Paginate(ctx, path)
	if err != nil {
		return nil, err
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var issues []Issue
	if err := json.Unmarshal(buf, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// CreateIssue opens an issue and returns its number.
func (c *Client) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (int, error) {
	payload := map[string]any{"title": title, "body": body}
	if len(labels) > 0 {
		payload["labels"] = labels
	}
	var created struct {
		Number int `json:"number"`
	}
	if err := c.mutateReturning(ctx, http.MethodPost, fmt.Sprintf("repos/%s/issues", repo), payload, &created); err != nil {
		return 0, err
	}
	return created.Number, nil
}

// CommentOnIssue posts a comment on an issue or pull request.
func (c *Client) CommentOnIssue(ctx context.Context, repo string, number int, body string) error {
	return c.mutate(ctx, http.MethodPost,
		fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
		map[string]string{"body": body})
}

// ClosePullRequest closes a pull request without merging it.
//
// This is the strongest mutation the lifecycle contract can perform. It
// deliberately has no counterpart: there is no merge, no branch deletion and
// no history rewrite anywhere in this client for the contract to reach for.
func (c *Client) ClosePullRequest(ctx context.Context, repo string, number int) error {
	return c.mutate(ctx, http.MethodPatch,
		fmt.Sprintf("repos/%s/pulls/%d", repo, number),
		map[string]string{"state": "closed"})
}

// mutateReturning performs a mutating call and decodes the response, for the
// endpoints whose result is needed to continue (a created issue's number).
func (c *Client) mutateReturning(ctx context.Context, method, path string, payload any, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return rgerrors.Wrap(rgerrors.InvariantViolation, "encode request", err)
	}
	respBody, status, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return rgerrors.New(rgerrors.Transient, fmt.Sprintf("GitHub %s %d: %s", method, status, strings.TrimSpace(string(respBody))))
	}
	if target == nil {
		return nil
	}
	return decodeJSON(respBody, target)
}
