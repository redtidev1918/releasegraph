package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

// Bound is a client restricted to an execution context. Repository-scoped
// processes are bound to a single repository: any call that names another
// repository fails with SCOPE_VIOLATION before an HTTP request is made.
//
// Fleet-wide operations (dispatching another repository's workflow) are only
// reachable through a fleet-scoped context.
type Bound struct {
	client  *Client
	context domain.ExecutionContext
}

// Bind attaches an execution context to a client.
func (c *Client) Bind(execution domain.ExecutionContext) *Bound {
	return &Bound{client: c, context: execution}
}

// Context returns the execution context this client is bound to.
func (b *Bound) Context() domain.ExecutionContext { return b.context }

// guardTarget rejects any repository outside the bound scope.
func (b *Bound) guardTarget(repo string) error {
	if repo == "" {
		return rgerrors.New(rgerrors.InvariantViolation, "repository argument is required")
	}
	if b.context.Allows(repo) {
		return nil
	}
	return rgerrors.New(rgerrors.ScopeViolation, fmt.Sprintf(
		"scope=%s bound to %q may not operate on %q", b.context.Scope, b.context.Repository, repo))
}

// guardPath extracts the repository from a REST path such as
// "repos/owner/name/releases/tags/v1" and enforces the scope.
func (b *Bound) guardPath(path string) error {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(path, "/"), "repos/")
	parts := strings.SplitN(trimmed, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		// Paths that do not address a repository (e.g. user/repos) are fleet
		// operations and are refused outside fleet scope.
		if b.context.Scope == domain.ScopeFleet {
			return nil
		}
		return rgerrors.New(rgerrors.ScopeViolation, fmt.Sprintf(
			"scope=%s may not call non-repository path %q", b.context.Scope, path))
	}
	return b.guardTarget(parts[0] + "/" + parts[1])
}

// Get performs a scoped GET.
func (b *Bound) Get(ctx context.Context, path string, target any) error {
	if err := b.guardPath(path); err != nil {
		return err
	}
	return b.client.Get(ctx, path, target)
}

// GetOptional performs a scoped GET that tolerates 404.
func (b *Bound) GetOptional(ctx context.Context, path string, target any) (bool, error) {
	if err := b.guardPath(path); err != nil {
		return false, err
	}
	return b.client.GetOptional(ctx, path, target)
}

// Paginate performs a scoped paginated GET.
func (b *Bound) Paginate(ctx context.Context, path string) ([]map[string]any, error) {
	if err := b.guardPath(path); err != nil {
		return nil, err
	}
	return b.client.Paginate(ctx, path)
}

// MergedPullRequests lists merged pull requests of one repository.
func (b *Bound) MergedPullRequests(ctx context.Context, repo string) ([]PullRequest, error) {
	if err := b.guardTarget(repo); err != nil {
		return nil, err
	}
	return b.client.MergedPullRequests(ctx, repo)
}

// TagCommit resolves a tag to the commit it points at.
func (b *Bound) TagCommit(ctx context.Context, repo, tag string) (string, error) {
	if err := b.guardTarget(repo); err != nil {
		return "", err
	}
	return b.client.TagCommit(ctx, repo, tag)
}

// ReleaseAssetText downloads a small release asset.
func (b *Bound) ReleaseAssetText(ctx context.Context, repo string, assetID int64) (string, bool, error) {
	if err := b.guardTarget(repo); err != nil {
		return "", false, err
	}
	return b.client.ReleaseAssetText(ctx, repo, assetID)
}

// Releases lists the releases of one repository.
func (b *Bound) Releases(ctx context.Context, repo string) ([]map[string]any, error) {
	if err := b.guardTarget(repo); err != nil {
		return nil, err
	}
	return b.client.Releases(ctx, repo)
}

// ReadFile reads a file from one repository.
func (b *Bound) ReadFile(ctx context.Context, repo, path, ref string) ([]byte, bool, error) {
	if err := b.guardTarget(repo); err != nil {
		return nil, false, err
	}
	return b.client.ReadFile(ctx, repo, path, ref)
}

// DefaultBranch returns the default branch of one repository.
func (b *Bound) DefaultBranch(ctx context.Context, repo string) (string, error) {
	if err := b.guardTarget(repo); err != nil {
		return "", err
	}
	return b.client.DefaultBranch(ctx, repo)
}

// AddIssueLabels adds provider labels. Allowed in repository and fleet scope
// because it is the acknowledgement path of the bound repository.
func (b *Bound) AddIssueLabels(ctx context.Context, repo string, number int, labels []string) error {
	if err := b.guardTarget(repo); err != nil {
		return err
	}
	return b.client.AddIssueLabels(ctx, repo, number, labels)
}

// RemoveIssueLabel removes one provider label.
func (b *Bound) RemoveIssueLabel(ctx context.Context, repo string, number int, label string) error {
	if err := b.guardTarget(repo); err != nil {
		return err
	}
	return b.client.RemoveIssueLabel(ctx, repo, number, label)
}

// OpenPullRequests lists the open pull requests of one repository.
func (b *Bound) OpenPullRequests(ctx context.Context, repo string) ([]OpenPullRequest, error) {
	if err := b.guardTarget(repo); err != nil {
		return nil, err
	}
	return b.client.OpenPullRequests(ctx, repo)
}

// CompareCommits compares head against base.
func (b *Bound) CompareCommits(ctx context.Context, repo, base, head string) (Compare, bool, error) {
	if err := b.guardTarget(repo); err != nil {
		return Compare{}, false, err
	}
	return b.client.CompareCommits(ctx, repo, base, head)
}

// HeadCheckState reports the combined check verdict for a commit.
func (b *Bound) HeadCheckState(ctx context.Context, repo, sha string) (string, error) {
	if err := b.guardTarget(repo); err != nil {
		return "", err
	}
	return b.client.HeadCheckState(ctx, repo, sha)
}

// OpenIssues lists the open issues of one repository.
func (b *Bound) OpenIssues(ctx context.Context, repo string) ([]Issue, error) {
	if err := b.guardTarget(repo); err != nil {
		return nil, err
	}
	return b.client.OpenIssues(ctx, repo)
}

// CreateIssue opens an issue in one repository.
//
// The lifecycle contract's mutations are allowed in repository and fleet scope
// for the same reason label acknowledgement is: they act on the repository the
// process is already responsible for. A repository-scoped process cannot reach
// another repository at all, because guardTarget refuses it before any request
// is made, so widening this to fleet scope grants no extra reach.
func (b *Bound) CreateIssue(ctx context.Context, repo, title, body string, labels []string) (int, error) {
	if err := b.guardTarget(repo); err != nil {
		return 0, err
	}
	return b.client.CreateIssue(ctx, repo, title, body, labels)
}

// CommentOnIssue posts a comment on an issue or pull request.
func (b *Bound) CommentOnIssue(ctx context.Context, repo string, number int, body string) error {
	if err := b.guardTarget(repo); err != nil {
		return err
	}
	return b.client.CommentOnIssue(ctx, repo, number, body)
}

// ClosePullRequest closes a pull request without merging it. This is the
// strongest mutation the lifecycle contract can reach: the client exposes no
// merge, no branch deletion and no history rewrite at all.
func (b *Bound) ClosePullRequest(ctx context.Context, repo string, number int) error {
	if err := b.guardTarget(repo); err != nil {
		return err
	}
	return b.client.ClosePullRequest(ctx, repo, number)
}

// DispatchWorkflow triggers another repository's workflow. This is a control
// plane capability: it is refused outside fleet scope.
func (b *Bound) DispatchWorkflow(ctx context.Context, repo, workflowFile, ref string, inputs map[string]string) error {
	if b.context.Scope != domain.ScopeFleet {
		return rgerrors.New(rgerrors.ScopeViolation, fmt.Sprintf(
			"scope=%s may not dispatch workflows in %s; cross-repository repair requires fleet scope", b.context.Scope, repo))
	}
	if err := b.guardTarget(repo); err != nil {
		return err
	}
	return b.client.DispatchWorkflow(ctx, repo, workflowFile, ref, inputs)
}
