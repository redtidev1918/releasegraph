package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/redtidev1918/releasegraph/internal/credential"
	rgdomain "github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/policy"
	"github.com/redtidev1918/releasegraph/internal/prlifecycle"
)

// prLifecyclePolicyPath is where a repository declares the contract. It is read
// from the default branch, because the contract describes how the repository
// wants its own pull requests treated, not how one branch was treated.
const prLifecyclePolicyPath = ".release-policy.yml"

// prLifecycleOutcome is the result of one audit or apply pass over one
// repository.
type prLifecycleOutcome struct {
	Repository string `json:"repository"`
	// Status is evaluated, undeclared (no contract in the repository) or
	// unreachable (the repository could not be read at all).
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	Enabled     bool   `json:"enabled"`
	Open        int    `json:"open"`
	InQueue     int    `json:"inQueue"`
	ShouldLeave int    `json:"shouldLeave"`

	Counts  map[prlifecycle.State]int `json:"counts"`
	Results []prlifecycle.Result      `json:"results,omitempty"`
	// Actions is present for apply and for dry runs: a plan is only trustworthy
	// if it can be read before it is executed.
	Actions []prlifecycle.Action `json:"actions,omitempty"`
	Applied []string             `json:"applied,omitempty"`
	Note    string               `json:"note,omitempty"`
}

// prLifecycleReport is the whole pass.
//
// schema_version and generated_at exist for the one consumer that reads this as
// a file rather than as terminal output: the fleet dashboard. The dashboard
// owns the rendered field; this document is only its input, so it states which
// shape of input it is and when it was produced.
type prLifecycleReport struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedAt   string               `json:"generated_at"`
	Mode          string               `json:"mode"`
	Scope         string               `json:"scope"`
	Manifest      string               `json:"manifest,omitempty"`
	Repositories  []prLifecycleOutcome `json:"repositories"`
	Open          int                  `json:"open"`
	InQueue       int                  `json:"inQueue"`
	ShouldLeave   int                  `json:"shouldLeave"`
	Applied       int                  `json:"applied"`
	Note          string               `json:"note,omitempty"`
}

func prLifecycleCommand(w io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("pr-lifecycle requires a subcommand: audit or apply")
	}
	switch args[0] {
	case "audit":
		return prLifecycleRun(w, args[1:], false)
	case "apply":
		return prLifecycleRun(w, args[1:], true)
	default:
		return fmt.Errorf("unknown pr-lifecycle subcommand %q (want audit or apply)", args[0])
	}
}

func prLifecycleRun(w io.Writer, args []string, mutate bool) error {
	var format, repo, manifestPath, policyPath, scopeFlag, reportPath string
	var dryRun bool
	var limit int
	fs := flags(&format)
	fs.StringVar(&repo, "repo", "", "owner/name to evaluate instead of the fleet manifest")
	fs.StringVar(&manifestPath, "manifest", "fleet.yaml", "fleet manifest path")
	fs.StringVar(&policyPath, "path", "", "local policy file instead of the repository's own")
	fs.StringVar(&scopeFlag, "scope", "", "auto, repository or fleet")
	// Not --output: that name already means "output format" for every command.
	fs.StringVar(&reportPath, "report", "", "also write the JSON report to this path")
	fs.BoolVar(&dryRun, "dry-run", false, "plan without mutating (apply only)")
	fs.IntVar(&limit, "limit", 0, "maximum pull requests that may leave the queue per repository (0 = no limit)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !mutate && dryRun {
		return fmt.Errorf("--dry-run is only meaningful with apply; audit never mutates")
	}

	ctx := context.Background()
	execution, note, err := prLifecycleExecution(scopeFlag, repo, manifestPath)
	if err != nil {
		return err
	}
	applying := mutate && !dryRun
	if execution.Scope == rgdomain.ScopeFleet {
		if credential.FleetAvailable() {
			if err := credential.UseFleetCredential(); err != nil {
				return err
			}
		} else if applying {
			// A fleet-wide mutation without a fleet credential is refused
			// before any request, not discovered as a 403 halfway through.
			return credential.RequireFleet("pr-lifecycle apply")
		} else {
			note = credential.FleetTokenEnv + " is not set; reading public data only and mutating nothing"
		}
	}

	targets, err := prLifecycleTargets(repo, manifestPath)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return rgerrors.New(rgerrors.InvariantViolation, "no repositories to evaluate")
	}

	client := github.New().Bind(execution)
	report := prLifecycleReport{
		SchemaVersion: prLifecycleSchemaVersion,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Scope:         string(execution.Scope),
		Note:          note,
	}
	switch {
	case !mutate:
		report.Mode = "audit"
	case dryRun:
		report.Mode = "dry-run"
	default:
		report.Mode = "apply"
	}
	if repo == "" {
		report.Manifest = manifestPath
	}

	for _, target := range targets {
		outcome := prLifecycleEvaluateRepository(ctx, client, target, policyPath, mutate, limit, &report)
		report.Repositories = append(report.Repositories, outcome)
	}

	if reportPath != "" {
		if err := writePRLifecycleSidecar(reportPath, report); err != nil {
			return err
		}
	}

	return write(w, format, report, humanPRLifecycle)
}

// prLifecycleSchemaVersion is the shape of the JSON sidecar. It is bumped when
// a field is renamed or given a different meaning, never when one is added.
const prLifecycleSchemaVersion = 1

// writePRLifecycleSidecar writes the machine-readable report next to the
// dashboard that consumes it.
//
// The dashboard's rendered field is owned by release_infra (one owner per
// field); this file is only its input. Writing it is therefore a plain dump of
// the same document that was printed, not a second rendering of the verdicts.
func writePRLifecycleSidecar(path string, report prLifecycleReport) error {
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return rgerrors.Wrap(rgerrors.InvariantViolation, "encode pr lifecycle report", err)
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return rgerrors.Wrap(rgerrors.Transient, "write pr lifecycle report", err)
	}
	return nil
}

// prLifecycleExecution decides which authority the pass runs under. Naming one
// repository that is not the ambient one is a cross-repository read, which is
// a fleet operation; running against the repository you are inside is not.
func prLifecycleExecution(scopeFlag, repo, manifestPath string) (rgdomain.ExecutionContext, string, error) {
	actor := actorName()
	switch scopeFlag {
	case "repository":
		self := os.Getenv("GITHUB_REPOSITORY")
		if self == "" {
			return rgdomain.ExecutionContext{}, "", fmt.Errorf("--scope repository requires GITHUB_REPOSITORY (run inside the repository)")
		}
		return rgdomain.ExecutionContext{Scope: rgdomain.ScopeRepository, Repository: self, Actor: actor, CredentialClass: rgdomain.CredentialRepository}, "", nil
	case "fleet":
		return rgdomain.ExecutionContext{Scope: rgdomain.ScopeFleet, Actor: actor, CredentialClass: rgdomain.CredentialFleet}, "", nil
	case "":
		if repo == "" {
			return rgdomain.ExecutionContext{Scope: rgdomain.ScopeFleet, Actor: actor, CredentialClass: rgdomain.CredentialFleet}, "", nil
		}
		if self := os.Getenv("GITHUB_REPOSITORY"); self != "" && strings.EqualFold(self, repo) {
			return rgdomain.ExecutionContext{Scope: rgdomain.ScopeRepository, Repository: self, Actor: actor, CredentialClass: rgdomain.CredentialRepository}, "", nil
		}
		return rgdomain.ExecutionContext{Scope: rgdomain.ScopeFleet, Actor: actor, CredentialClass: rgdomain.CredentialFleet}, "scope=fleet because " + repo + " is not the ambient repository", nil
	default:
		return rgdomain.ExecutionContext{}, "", fmt.Errorf("--scope must be repository or fleet")
	}
}

// prLifecycleTargets resolves the repository set. The manifest is the
// authority, exactly as it is for the rest of the fleet tooling: a repository
// is in scope because it is declared, never because it happens to exist.
func prLifecycleTargets(repo, manifestPath string) ([]string, error) {
	if repo != "" {
		return []string{repo}, nil
	}
	manifest, err := fleet.LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	managed, _ := fleet.Resolve(manifest, nil)
	targets := append([]string{}, fleet.Operable(managed)...)
	sort.Strings(targets)
	return targets, nil
}

func prLifecycleEvaluateRepository(ctx context.Context, client *github.Bound, repo, policyPath string, mutate bool, limit int, report *prLifecycleReport) prLifecycleOutcome {
	outcome := prLifecycleOutcome{Repository: repo, Status: "evaluated"}

	lifecycle, declared, err := prLifecycleContract(ctx, client, repo, policyPath)
	if err != nil {
		outcome.Status = "unreachable"
		outcome.Reason = err.Error()
		return outcome
	}
	outcome.Enabled = lifecycle.IsEnabled()
	if !declared {
		outcome.Status = "undeclared"
		outcome.Reason = "no " + prLifecyclePolicyPath + " in the repository"
	}
	outcome.Note = ""

	pulls, err := client.OpenPullRequests(ctx, repo)
	if err != nil {
		outcome.Status = "unreachable"
		outcome.Reason = err.Error()
		return outcome
	}
	outcome.Open = len(pulls)

	now := time.Now().UTC()
	results := make([]prlifecycle.Result, 0, len(pulls))
	for _, pull := range pulls {
		results = append(results, prLifecycleObserve(ctx, client, repo, pull, lifecycle, now))
	}
	results = prLifecycleApplyLimit(results, limit, &outcome)

	outcome.Counts = prlifecycle.Summarize(results)
	outcome.Results = results
	actionable := prlifecycle.Actionable(results)
	outcome.ShouldLeave = len(actionable)
	outcome.InQueue = outcome.Open - outcome.ShouldLeave

	report.Open += outcome.Open
	report.InQueue += outcome.InQueue
	report.ShouldLeave += outcome.ShouldLeave

	if !mutate {
		return outcome
	}

	actions := prlifecycle.Plan(prlifecycle.PlanInput{Repository: repo, Results: results, Lifecycle: lifecycle})
	outcome.Actions = actions
	if !lifecycle.IsEnabled() {
		if len(actionable) > 0 {
			outcome.Note = "the contract is not declared or not enabled here; reporting only"
		}
		return outcome
	}
	applied, err := prLifecycleApply(ctx, client, repo, actions)
	outcome.Applied = applied
	report.Applied += len(applied)
	if err != nil {
		outcome.Status = "failed"
		outcome.Reason = err.Error()
	}
	return outcome
}

// prLifecycleContract reads one repository's contract. A repository without a
// policy file is not an error: it simply has not asked for the contract.
func prLifecycleContract(ctx context.Context, client *github.Bound, repo, policyPath string) (*policy.PRLifecycle, bool, error) {
	raw := []byte(nil)
	if policyPath != "" {
		content, err := os.ReadFile(policyPath)
		if err != nil {
			return nil, false, err
		}
		raw = content
	} else {
		content, found, err := client.ReadFile(ctx, repo, prLifecyclePolicyPath, "")
		if err != nil {
			return nil, false, err
		}
		if !found {
			return nil, false, nil
		}
		raw = []byte(content)
	}
	loaded, err := policy.Parse(raw)
	if err != nil {
		return nil, true, err
	}
	return loaded.Repository.PullRequests.Lifecycle, true, nil
}

// prLifecycleObserve builds the pure observation the contract classifies.
//
// Two of the reads are separate calls because the list endpoint carries
// neither. A failed check read degrades to "no verdict" rather than to
// "passing": checks never decide that work is parked, so the degradation
// cannot move a pull request out of the queue.
func prLifecycleObserve(ctx context.Context, client *github.Bound, repo string, pull github.OpenPullRequest, lifecycle *policy.PRLifecycle, now time.Time) prlifecycle.Result {
	checks := github.CheckStateNone
	if pull.Head.Sha != "" {
		if state, err := client.HeadCheckState(ctx, repo, pull.Head.Sha); err == nil {
			checks = state
		}
	}
	ahead, changed, known := 0, 0, false
	if pull.Base.Ref != "" && pull.Head.Sha != "" {
		if compare, ok, err := client.CompareCommits(ctx, repo, pull.Base.Ref, pull.Head.Sha); err == nil {
			ahead, changed, known = compare.AheadBy, len(compare.Files), ok
		}
	}
	labels := make([]string, 0, len(pull.Labels))
	for _, label := range pull.Labels {
		labels = append(labels, label.Name)
	}
	return prlifecycle.Evaluate(prlifecycle.Input{
		Repository: repo,
		PullRequest: prlifecycle.PullRequest{
			Number:             pull.Number,
			Title:              pull.Title,
			HeadRef:            pull.Head.Ref,
			HeadSHA:            pull.Head.Sha,
			BaseRef:            pull.Base.Ref,
			Author:             pull.User.Login,
			Draft:              pull.Draft,
			Labels:             labels,
			UpdatedAt:          pull.UpdatedAt,
			Mergeable:          prlifecycle.MergeabilityFromProvider(pull.MergeableState),
			Checks:             prlifecycle.ChecksFromProvider(checks),
			CommitsAheadOfBase: ahead,
			ChangedFiles:       changed,
			CompareKnown:       known,
		},
		Lifecycle: lifecycle,
		Now:       now,
	})
}

// prLifecycleApplyLimit bounds the blast radius of one pass. Pull requests
// beyond the limit are reported but left in the queue, so a first run against
// a backlog can never empty it in one go.
func prLifecycleApplyLimit(results []prlifecycle.Result, limit int, outcome *prLifecycleOutcome) []prlifecycle.Result {
	if limit <= 0 {
		return results
	}
	kept := 0
	for i := range results {
		if !results[i].Actionable {
			continue
		}
		kept++
		if kept > limit {
			results[i].Actionable = false
			results[i].Reason += " (left in the queue by --limit)"
			outcome.Note = fmt.Sprintf("--limit %d left additional pull requests in the queue", limit)
		}
	}
	return results
}

// prLifecycleApply executes a plan in order. Archive always precedes the close
// for the same pull request, so a pull request cannot leave the queue before
// its context is recorded.
func prLifecycleApply(ctx context.Context, client *github.Bound, repo string, actions []prlifecycle.Action) ([]string, error) {
	applied := []string{}
	issues := []github.Issue(nil)
	loaded := false
	resolvedIssue := map[int]int{}

	for _, action := range actions {
		switch action.Kind {
		case prlifecycle.ActionEnsureIssue:
			if !loaded {
				found, err := client.OpenIssues(ctx, repo)
				if err != nil {
					return applied, err
				}
				issues, loaded = found, true
			}
			if existing, ok := prLifecycleArchiveIssue(issues, prlifecycle.IssueMarker(action.Number)); ok {
				resolvedIssue[action.Number] = existing
				applied = append(applied, fmt.Sprintf("issue #%d reused for pull request #%d", existing, action.Number))
				continue
			}
			number, err := client.CreateIssue(ctx, repo, action.Title, action.Body, nil)
			if err != nil {
				return applied, err
			}
			resolvedIssue[action.Number] = number
			issues = append(issues, github.Issue{Number: number, Title: action.Title, Body: action.Body, State: "open"})
			applied = append(applied, fmt.Sprintf("issue #%d archives pull request #%d", number, action.Number))
		case prlifecycle.ActionComment:
			body := prlifecycle.Render(action.Body, resolvedIssue[action.Number])
			if err := client.CommentOnIssue(ctx, repo, action.Number, body); err != nil {
				return applied, err
			}
			applied = append(applied, fmt.Sprintf("commented on pull request #%d", action.Number))
		case prlifecycle.ActionClosePullRequest:
			if err := client.ClosePullRequest(ctx, repo, action.Number); err != nil {
				return applied, err
			}
			applied = append(applied, fmt.Sprintf("closed pull request #%d (branch kept)", action.Number))
		default:
			return applied, rgerrors.New(rgerrors.InvariantViolation, "unknown lifecycle action "+string(action.Kind))
		}
	}
	return applied, nil
}

// prLifecycleArchiveIssue finds an existing archive issue by its marker.
func prLifecycleArchiveIssue(issues []github.Issue, marker string) (int, bool) {
	for _, issue := range issues {
		if issue.PullRequest != nil {
			continue
		}
		if strings.Contains(issue.Body, marker) {
			return issue.Number, true
		}
	}
	return 0, false
}

func humanPRLifecycle(w io.Writer, data any) {
	report, ok := data.(prLifecycleReport)
	if !ok {
		return
	}
	fmt.Fprintf(w, "pr lifecycle (%s)\n\n", report.Mode)
	for _, repo := range report.Repositories {
		header := fmt.Sprintf("%s  open=%d  should-leave=%d", repo.Repository, repo.Open, repo.ShouldLeave)
		if !repo.Enabled {
			header += "  contract=absent"
		}
		fmt.Fprintln(w, header)
		if repo.Status != "evaluated" {
			fmt.Fprintf(w, "  %s: %s\n", repo.Status, repo.Reason)
		}
		for _, result := range repo.Results {
			marker := " "
			if result.Actionable {
				marker = "→"
			}
			fmt.Fprintf(w, "  #%-5d %-12s %s %s\n", result.Number, result.State, marker, result.Reason)
		}
		for _, line := range repo.Applied {
			fmt.Fprintf(w, "  applied: %s\n", line)
		}
		if repo.Note != "" {
			fmt.Fprintf(w, "  note: %s\n", repo.Note)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "totals: open=%d in-queue=%d should-leave=%d applied=%d\n", report.Open, report.InQueue, report.ShouldLeave, report.Applied)
	if report.Note != "" {
		fmt.Fprintf(w, "note: %s\n", report.Note)
	}
}
