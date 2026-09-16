package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

// IssueLabel is one label on a pull request or issue.
type IssueLabel struct {
	Name string `json:"name"`
}

// PullRequest is the subset of a PR the provider layer needs.
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	// MergedAt is how the list endpoint signals a merged PR; the "merged"
	// boolean only exists on the single-PR endpoint and is always absent here.
	MergedAt       *string      `json:"merged_at"`
	MergeCommitSHA string       `json:"merge_commit_sha"`
	Labels         []IssueLabel `json:"labels"`
}

// Merged reports whether the pull request was merged.
func (p PullRequest) Merged() bool { return p.MergedAt != nil && *p.MergedAt != "" }

// Tag is one git tag as returned by the list-tags endpoint.
type Tag struct {
	Name   string `json:"name"`
	Commit struct {
		Sha string `json:"sha"`
	} `json:"commit"`
}

// MergedPullRequests lists a repository's merged PRs (most recent first).
func (c *Client) MergedPullRequests(ctx context.Context, repo string) ([]PullRequest, error) {
	var prs []PullRequest
	path := fmt.Sprintf("repos/%s/pulls?state=closed&sort=updated&direction=desc&per_page=100", repo)
	raw, err := c.Paginate(ctx, path)
	if err != nil {
		return nil, err
	}
	// Paginate returns generic maps; re-encode/decode into typed structs.
	buf, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(buf, &prs); err != nil {
		return nil, err
	}
	merged := prs[:0]
	for _, pr := range prs {
		if pr.Merged() {
			merged = append(merged, pr)
		}
	}
	return merged, nil
}

// TagCommit resolves a tag name to the commit it points at. Lightweight tags
// resolve directly; annotated tags are peeled via the tag object. Returns ""
// (no error) when the tag does not exist.
func (c *Client) TagCommit(ctx context.Context, repo, tag string) (string, error) {
	var tagObj struct {
		Object struct {
			Type string `json:"type"`
			Sha  string `json:"sha"`
		} `json:"object"`
	}
	path := fmt.Sprintf("repos/%s/git/ref/tags/%s", repo, tag)
	found, err := c.GetOptional(ctx, path, &tagObj)
	if err != nil {
		return "", err
	}
	if !found {
		return "", nil
	}
	if tagObj.Object.Type == "tag" {
		var annotated struct {
			Object struct {
				Sha string `json:"sha"`
			} `json:"object"`
		}
		if err := c.Get(ctx, fmt.Sprintf("repos/%s/git/tags/%s", repo, tagObj.Object.Sha), &annotated); err != nil {
			return "", err
		}
		return annotated.Object.Sha, nil
	}
	return tagObj.Object.Sha, nil
}

// AddIssueLabels adds labels to an issue/PR (idempotent server-side).
func (c *Client) AddIssueLabels(ctx context.Context, repo string, number int, labels []string) error {
	return c.mutate(ctx, http.MethodPost,
		fmt.Sprintf("repos/%s/issues/%d/labels", repo, number),
		map[string][]string{"labels": labels})
}

// RemoveIssueLabel removes a single label. A missing label is not an error.
func (c *Client) RemoveIssueLabel(ctx context.Context, repo string, number int, label string) error {
	_, status, err := c.request(ctx, http.MethodDelete,
		fmt.Sprintf("repos/%s/issues/%d/labels/%s", repo, number, label), nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusNotFound {
		return rgerrors.New(rgerrors.Transient, fmt.Sprintf("GitHub %d removing label", status))
	}
	return nil
}

func (c *Client) mutate(ctx context.Context, method, path string, payload any) error {
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
	return nil
}

// request performs a mutating HTTP call with one transient retry.
func (c *Client) request(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			default:
			}
		}
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/"+strings.TrimPrefix(path, "/"), reader)
		if err != nil {
			return nil, 0, rgerrors.Wrap(rgerrors.Transient, "build request", err)
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			last = rgerrors.Wrap(rgerrors.Transient, "GitHub request", err)
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			return nil, resp.StatusCode, rgerrors.New(rgerrors.Authentication, githubErrorDetail("GitHub authentication failed", resp.StatusCode, data))
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, resp.StatusCode, rgerrors.New(rgerrors.Permission, githubErrorDetail("GitHub permission denied", resp.StatusCode, data))
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			last = rgerrors.New(rgerrors.Transient, fmt.Sprintf("GitHub %s", resp.Status))
			continue
		}
		return data, resp.StatusCode, nil
	}
	return nil, 0, last
}

// DefaultBranch returns a repository's default branch.
func (c *Client) DefaultBranch(ctx context.Context, repo string) (string, error) {
	var meta struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := c.Get(ctx, fmt.Sprintf("repos/%s", repo), &meta); err != nil {
		return "", err
	}
	return meta.DefaultBranch, nil
}

// DispatchWorkflow triggers a workflow_dispatch run. This is how same-version
// repair re-enters a repository's own release pipeline instead of fabricating
// release objects from the outside.
func (c *Client) DispatchWorkflow(ctx context.Context, repo, workflowFile, ref string, inputs map[string]string) error {
	payload := map[string]any{"ref": ref}
	if len(inputs) > 0 {
		payload["inputs"] = inputs
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return rgerrors.Wrap(rgerrors.InvariantViolation, "encode dispatch", err)
	}
	respBody, status, err := c.request(ctx, http.MethodPost,
		fmt.Sprintf("repos/%s/actions/workflows/%s/dispatches", repo, workflowFile), body)
	if err != nil {
		return err
	}
	// 204 No Content is the documented success response.
	if status != http.StatusNoContent && (status < 200 || status >= 300) {
		return rgerrors.New(rgerrors.Transient, fmt.Sprintf("dispatch %s %d: %s", repo, status, strings.TrimSpace(string(respBody))))
	}
	return nil
}

// mustJSON encodes a payload for a mutating request.
func mustJSON(payload any) []byte {
	body, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return body
}

// decodeJSON decodes a response body, ignoring malformed content.
func decodeJSON(body []byte, target any) error { return json.Unmarshal(body, target) }
