// Package prhealth observes CI without applying the PR lifecycle contract.
package prhealth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
)

type Check struct {
	Health     string `json:"health"`
	CheckSuite struct {
		App struct {
			Slug string `json:"slug"`
		} `json:"app"`
		WorkflowRun *struct {
			Workflow struct {
				ID string `json:"id"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	DatabaseID int64  `json:"databaseId,omitempty"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	Context    string `json:"context,omitempty"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state,omitempty"`
	IsRequired bool   `json:"isRequired"`
	DetailsURL string `json:"detailsUrl,omitempty"`
	TargetURL  string `json:"targetUrl,omitempty"`
	Summary    string `json:"summary,omitempty"`
	Category   string `json:"category,omitempty"`
}
type Pull struct {
	BaseSHA        string           `json:"base_sha"`
	BaseBranch     string           `json:"base_branch"`
	ReviewDecision string           `json:"review_decision"`
	ChangedFiles   int              `json:"changed_files"`
	Number         int              `json:"number"`
	Title          string           `json:"title"`
	Author         string           `json:"author"`
	Branch         string           `json:"branch"`
	SHA            string           `json:"sha"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	Draft          bool             `json:"draft"`
	Mergeable      string           `json:"mergeable"`
	Status         string           `json:"status"`
	Checks         []Check          `json:"checks"`
	Required       []string         `json:"required"`
	Runs           []map[string]any `json:"runs"`
	FailedJobs     []map[string]any `json:"failed_jobs"`
	Error          string           `json:"error,omitempty"`
}
type Repository struct {
	Name              string `json:"name"`
	Complete          bool   `json:"complete"`
	Error             string `json:"error,omitempty"`
	Pulls             []Pull `json:"pulls"`
	Dependabot        string `json:"dependabot,omitempty"`
	DependabotPresent bool   `json:"dependabot_present"`
}
type Incident struct {
	SHA        string `json:"sha"`
	Identity   string `json:"identity"`
	Evidence   string `json:"evidence,omitempty"`
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	Check      string `json:"check"`
	Category   string `json:"category"`
	URL        string `json:"url,omitempty"`
}

func (i Incident) Fingerprint() string {
	raw, _ := json.Marshal([]any{i.Repository, i.Number, i.SHA, i.Identity})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

type Report struct {
	SchemaVersion int                 `json:"schema_version"`
	StartedAt     time.Time           `json:"started_at"`
	FinishedAt    time.Time           `json:"finished_at"`
	Status        string              `json:"status"`
	Complete      bool                `json:"complete"`
	Expected      []string            `json:"expected"`
	Repositories  []Repository        `json:"repositories"`
	Incidents     map[string]Incident `json:"incidents"`
}

func Scan(ctx context.Context, client *github.Bound, manifest *fleet.Manifest) Report {
	report := Report{SchemaVersion: 1, StartedAt: time.Now().UTC(), Complete: true, Status: "PASS", Expected: manifest.EnabledNames(), Incidents: map[string]Incident{}}
	for _, repo := range report.Expected {
		result := Repository{Name: repo, Complete: true, Pulls: []Pull{}}
		repoctx, repoCancel := context.WithTimeout(ctx, 5*time.Minute)
		count, countErr := openCount(repoctx, client, repo)
		pulls, err := client.OpenPullRequests(repoctx, repo)
		if countErr != nil {
			err = countErr
		} else if err == nil && count != len(pulls) {
			err = fmt.Errorf("PR pagination count mismatch")
		}
		if err != nil {
			result.Complete = false
			result.Error = FailureCode(err)
		} else {
			for _, pr := range pulls {
				p := Pull{Number: pr.Number, Title: pr.Title, Author: pr.User.Login, Branch: pr.Head.Ref, SHA: pr.Head.Sha, CreatedAt: pr.CreatedAt, UpdatedAt: pr.UpdatedAt, Draft: pr.Draft, Status: "UNKNOWN"}
				prctx, cancel := context.WithTimeout(repoctx, 2*time.Minute)
				err := observe(prctx, client, repo, pr.Base.Ref, &p)
				cancel()
				if err == nil && p.Status == "UNKNOWN" {
					err = fmt.Errorf("unrecognized check conclusion")
				}
				if err != nil {
					p.Status = "UNKNOWN"
					p.Error = FailureCode(err)
					result.Complete = false
				}
				for _, check := range p.Checks {
					if checkState(check) == "FAIL" {
						if check.Category == "" {
							check.Category = "UNCLASSIFIED"
						}
						addIncident(&report, Incident{Repository: repo, Number: p.Number, SHA: p.SHA, Identity: checkIdentity(check), Check: check.Name, Category: check.Category, URL: check.DetailsURL, Evidence: check.Summary})
					}
				}
				if p.Status == "MISSING" || p.Status == "UNKNOWN" || p.Status == "SKIPPED" {
					// Evidence carries the failure code so the alert body can
					// disclose monitoring capability gaps (e.g. API_PERMISSION).
					addIncident(&report, Incident{Repository: repo, Number: p.Number, SHA: p.SHA, Identity: "pr:" + p.Status, Check: p.Status, Category: "UNCLASSIFIED", Evidence: p.Error, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, p.Number)})
				}
				result.Pulls = append(result.Pulls, p)
			}
		}
		latest, latestErr := client.OpenPullRequests(repoctx, repo)
		heads := map[int]string{}
		for _, pr := range latest {
			heads[pr.Number] = pr.Head.Sha
		}
		if latestErr != nil || len(heads) != len(pulls) {
			result.Complete = false
			result.Error = "PR_DISCOVERY_CHANGED"
		}
		for _, pr := range pulls {
			if heads[pr.Number] != pr.Head.Sha {
				result.Complete = false
				result.Error = "PR_DISCOVERY_CHANGED"
			}
		}
		after, afterErr := openCount(repoctx, client, repo)
		if afterErr != nil || after != count {
			result.Complete = false
			result.Error = "PR_DISCOVERY_CHANGED"
		}
		raw, present, err := client.ReadFile(repoctx, repo, ".github/dependabot.yml", "")
		if err == nil && !present {
			raw, present, err = client.ReadFile(repoctx, repo, ".github/dependabot.yaml", "")
		}
		repoCancel()
		if err != nil {
			result.Complete = false
			result.Error = FailureCode(err)
		} else {
			result.Dependabot = string(raw)
			result.DependabotPresent = present
		}
		if !result.Complete {
			report.Complete = false
			addIncident(&report, Incident{Repository: repo, Identity: "SCAN_INCOMPLETE", Check: "SCAN_INCOMPLETE", Category: "UNCLASSIFIED", Evidence: result.Error})
		}
		report.Repositories = append(report.Repositories, result)
	}
	if len(report.Expected) == 0 {
		report.Complete = false
	}
	if len(report.Incidents) > 0 {
		report.Status = "FAIL"
	} else {
		for _, repo := range report.Repositories {
			for _, p := range repo.Pulls {
				if p.Status == "PENDING" {
					report.Status = "PENDING"
				}
			}
		}
	}
	if !report.Complete {
		report.Status = "DEGRADED"
	}
	report.FinishedAt = time.Now().UTC()
	return report
}
func addIncident(r *Report, i Incident) { r.Incidents[i.Fingerprint()] = i }

const query = `query PRHealth($owner:String!,$name:String!,$number:Int!,$cursor:String) {
 repository(owner:$owner,name:$name) { pullRequest(number:$number) {
 headRefOid baseRefOid baseRefName reviewDecision changedFiles mergeable
 commits(last:1) { nodes { commit { oid statusCheckRollup { contexts(first:100,after:$cursor) {
 totalCount pageInfo { hasNextPage endCursor } nodes {
 ... on CheckRun { id databaseId name status conclusion isRequired(pullRequestNumber:$number) detailsUrl checkSuite { app { slug } workflowRun { workflow { id } } } }
 ... on StatusContext { id context state isRequired(pullRequestNumber:$number) targetUrl description }
 } } } } } }
 } }
}`

// requiredContextsQuery reads branch protection separately from the check
// rollup: branchProtectionRule needs the administration permission, and a
// permission failure there must degrade only required-check evidence — the
// observed checks of the pull request stay in the report.
const requiredContextsQuery = `query RequiredContexts($owner:String!,$name:String!,$base:String!) {
 repository(owner:$owner,name:$name) { ref(qualifiedName:$base) {
 branchProtectionRule { requiredStatusCheckContexts }
 } }
}`

func observe(ctx context.Context, c *github.Bound, repo, base string, p *Pull) error {
	parts := strings.Split(repo, "/")
	vars := map[string]any{"owner": parts[0], "name": parts[1], "number": p.Number, "cursor": nil}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for {
		var data struct {
			Repository *struct {
				PullRequest *struct {
					BaseRefOid     string
					BaseRefName    string
					ReviewDecision string
					ChangedFiles   int
					HeadRefOid     string
					Mergeable      string
					Commits        struct {
						Nodes []struct {
							Commit struct {
								Oid               string
								StatusCheckRollup *struct {
									Contexts struct {
										TotalCount *int
										PageInfo   *struct {
											HasNextPage bool
											EndCursor   string
										}
										Nodes []Check
									}
								}
							}
						}
					}
				}
			}
		}
		if err := c.Query(ctx, repo, query, vars, &data); err != nil {
			return err
		}
		if data.Repository == nil || data.Repository.PullRequest == nil {
			return fmt.Errorf("PR unavailable")
		}
		pr := data.Repository.PullRequest
		if pr.HeadRefOid != p.SHA {
			return fmt.Errorf("PR head changed during scan")
		}
		p.Mergeable = pr.Mergeable
		p.BaseSHA = pr.BaseRefOid
		p.BaseBranch = pr.BaseRefName
		p.ReviewDecision = pr.ReviewDecision
		p.ChangedFiles = pr.ChangedFiles
		if len(pr.Commits.Nodes) != 1 || pr.Commits.Nodes[0].Commit.Oid != p.SHA {
			return fmt.Errorf("head commit unavailable")
		}
		rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup
		if rollup == nil {
			break
		}
		page := rollup.Contexts
		if page.TotalCount == nil || page.PageInfo == nil || page.Nodes == nil {
			return fmt.Errorf("missing check pagination metadata")
		}
		for _, check := range page.Nodes {
			if check.ID == "" || ids[check.ID] {
				return fmt.Errorf("missing or duplicate check ID")
			}
			ids[check.ID] = true
			if check.Context != "" {
				check.Name = check.Context
				check.Conclusion = check.State
				check.DetailsURL = check.TargetURL
				if check.State == "PENDING" {
					check.Status = "IN_PROGRESS"
				} else {
					check.Status = "COMPLETED"
				}
			}
			p.Checks = append(p.Checks, check)
		}
		if !page.PageInfo.HasNextPage {
			if len(p.Checks) != *page.TotalCount {
				return fmt.Errorf("incomplete check pagination")
			}
			break
		}
		cursor := page.PageInfo.EndCursor
		if cursor == "" || seen[cursor] || len(page.Nodes) == 0 {
			return fmt.Errorf("invalid check cursor")
		}
		seen[cursor] = true
		vars["cursor"] = cursor
	}
	var err error
	p.Runs, err = c.Collection(ctx, fmt.Sprintf("repos/%s/actions/runs?head_sha=%s", repo, p.SHA), "workflow_runs")
	if err != nil {
		return err
	}
	// The runs endpoint returns newest first. Older attempts of the same
	// workflow/event must not resurrect a failure already superseded on this SHA.
	seenRuns := map[string]bool{}
	for _, run := range p.Runs {
		if run["head_sha"] != p.SHA {
			return fmt.Errorf("workflow run head mismatch")
		}
		key := fmt.Sprint(run["workflow_id"]) + ":" + fmt.Sprint(run["event"])
		if seenRuns[key] {
			continue
		}
		seenRuns[key] = true
		conclusion := strings.ToUpper(fmt.Sprint(run["conclusion"]))
		state := strings.ToUpper(fmt.Sprint(run["status"]))
		synthetic := Check{Name: "workflow: " + fmt.Sprint(run["name"]), ID: "workflow:" + key, Status: state, Conclusion: conclusion, DetailsURL: fmt.Sprint(run["html_url"])}
		synthetic.CheckSuite.App.Slug = "github-actions"
		// Preserve workflow failures even if a check run has not been exposed.
		if checkState(synthetic) == "FAIL" {
			p.Checks = append(p.Checks, synthetic)
		}

		if strings.EqualFold(fmt.Sprint(run["conclusion"]), "failure") || strings.EqualFold(fmt.Sprint(run["conclusion"]), "timed_out") {
			jobs, err := c.Collection(ctx, fmt.Sprintf("repos/%s/actions/runs/%.0f/jobs?filter=latest", repo, run["id"]), "jobs")
			if err != nil {
				return err
			}
			for _, job := range jobs {
				if job["conclusion"] == "failure" || job["conclusion"] == "timed_out" {
					p.FailedJobs = append(p.FailedJobs, job)
				}
			}
		}
	}
	for i := range p.Checks {
		check := &p.Checks[i]
		if checkState(*check) == "FAIL" {
			// Check annotations carry diagnostic evidence without downloading raw logs
			// that could disclose application output. Jobs retain their GitHub log URL.
			check.Category = "UNCLASSIFIED"
			if check.DatabaseID > 0 {
				var detail struct {
					Output struct {
						Summary string
						Title   string
						Text    string
					}
				}
				if err := c.Get(ctx, fmt.Sprintf("repos/%s/check-runs/%d", repo, check.DatabaseID), &detail); err != nil {
					return err
				}
				check.Category, check.Summary = classifyEvidence(detail.Output.Title + "\n" + detail.Output.Summary + "\n" + detail.Output.Text)
				annotations, err := c.Paginate(ctx, fmt.Sprintf("repos/%s/check-runs/%d/annotations", repo, check.DatabaseID))
				if err != nil {
					return err
				}
				if check.Category == "UNCLASSIFIED" {
					for _, a := range annotations {
						category, evidence := classifyEvidence(fmt.Sprint(a["message"]))
						if category != "UNCLASSIFIED" {
							check.Category = category
							check.Summary = evidence
							break
						}
					}
				}
			}
			for _, job := range p.FailedJobs {
				if job["name"] == check.Name {
					check.Summary += "\nfailed job: " + fmt.Sprint(job["name"])
					break
				}
			}
		}
	}
	for i := range p.Checks {
		p.Checks[i].Health = checkState(p.Checks[i])
	}
	// Required-check evidence is collected after the checks themselves so a
	// permission failure here leaves the observed checks in the report while
	// the pull request degrades to UNKNOWN. Absence of evidence is never a
	// passing check.
	var required struct {
		Repository *struct {
			Ref *struct {
				BranchProtectionRule *struct {
					RequiredStatusCheckContexts []string
				}
			}
		}
	}
	if err := c.Query(ctx, repo, requiredContextsQuery, map[string]any{
		"owner": parts[0], "name": parts[1], "base": base,
	}, &required); err != nil {
		return err
	}
	if required.Repository == nil || required.Repository.Ref == nil {
		return fmt.Errorf("base branch unavailable")
	}
	if rule := required.Repository.Ref.BranchProtectionRule; rule != nil {
		p.Required = append(p.Required, rule.RequiredStatusCheckContexts...)
	}
	// Active rulesets supplement classic branch protection.
	rules, err := c.Paginate(ctx, fmt.Sprintf("repos/%s/rules/branches/%s", repo, url.PathEscape(base)))
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if rule["type"] == "required_status_checks" {
			raw, _ := json.Marshal(rule["parameters"])
			var params struct {
				RequiredStatusChecks []struct {
					Context string `json:"context"`
				} `json:"required_status_checks"`
			}
			if err := json.Unmarshal(raw, &params); err != nil {
				return err
			}
			for _, check := range params.RequiredStatusChecks {
				p.Required = append(p.Required, check.Context)
			}
		}
	}
	p.Status = Evaluate(p.Checks, p.Required)
	return nil
}

func checkState(c Check) string {
	switch c.Conclusion {
	case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
		return "FAIL"
	}
	switch c.Status {
	case "QUEUED", "IN_PROGRESS", "WAITING", "PENDING", "REQUESTED":
		return "PENDING"
	case "COMPLETED":
		switch c.Conclusion {
		case "SUCCESS":
			return "PASS"
		case "NEUTRAL", "SKIPPED":
			return "SKIPPED"
		}
	}
	return "UNKNOWN"
}
func Evaluate(checks []Check, required []string) string {
	if len(checks) == 0 {
		return "MISSING"
	}
	state := "PASS"
	executed := false
	skippedRequired := false
	present := map[string]bool{}
	for _, c := range checks {
		if c.IsRequired {
			present[c.Name] = true
		}
		s := checkState(c)
		if s == "PASS" {
			executed = true
		}
		if s == "SKIPPED" && c.IsRequired {
			skippedRequired = true
		}
		if s == "FAIL" {
			return "FAIL"
		}
		if s == "UNKNOWN" {
			state = "UNKNOWN"
		} else if s == "PENDING" && state != "UNKNOWN" {
			state = "PENDING"
		}
	}
	for _, name := range required {
		if !present[name] {
			return "MISSING"
		}
	}
	if state == "PASS" && (skippedRequired || !executed) {
		return "SKIPPED"
	}
	return state
}
func checkIdentity(c Check) string {
	workflow := ""
	if c.CheckSuite.WorkflowRun != nil {
		workflow = c.CheckSuite.WorkflowRun.Workflow.ID
	}
	return c.CheckSuite.App.Slug + ":" + workflow + ":" + c.Name
}
func Classify(evidence string) string { category, _ := classifyEvidence(evidence); return category }

// Persist only the matched diagnostic phrase; arbitrary log output is never exported.
func classifyEvidence(evidence string) (string, string) {
	text := strings.ToLower(evidence)
	rules := []struct {
		category string
		tokens   []string
	}{
		{"SECRET_BOUNDARY", []string{"secret is not available", "secrets are not available", "missing production secret", "production signing secrets required"}},
		{"TYPE_ERROR", []string{"error ts", "type is not assignable", "typecheck failed"}},
		{"CONFIG_DRIFT", []string{"docsite drift", "generated files are out of date", "template drift"}},
		{"CI_CONFIGURATION", []string{"invalid workflow file", "eslint couldn't find", "eslint.config", "configuration file not found"}},
		{"CONTRACT_MISMATCH", []string{"contract mismatch", "telepress version drift", "incompatible protocol version"}},
		{"INFRASTRUCTURE", []string{"econnreset", "etimedout", "runner lost communication", "service unavailable"}},
		{"DEPENDENCY_BREAKING", []string{"breaking api change", "unsupported dependency major version"}},
	}
	for _, rule := range rules {
		for _, token := range rule.tokens {
			if strings.Contains(text, token) {
				return rule.category, token
			}
		}
	}
	return "UNCLASSIFIED", "No deterministic diagnostic matched; inspect linked check/job."
}
func Summary(r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fleet PR Health: %s\n\nScan: %s — %s; coverage %d/%d; complete=%t\n\n", r.Status, r.StartedAt.Format(time.RFC3339), r.FinishedAt.Format(time.RFC3339), len(r.Repositories), len(r.Expected), r.Complete)
	for _, repo := range r.Repositories {
		fmt.Fprintf(&b, "- %s: %d open PRs; complete=%t\n", repo.Name, len(repo.Pulls), repo.Complete)
		for _, p := range repo.Pulls {
			fmt.Fprintf(&b, "  - [#%d](https://github.com/%s/pull/%d): %s\n", p.Number, repo.Name, p.Number, p.Status)
		}
	}
	keys := []string{}
	for key := range r.Incidents {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		i := r.Incidents[key]
		fmt.Fprintf(&b, "- %s #%d — %s — %s\n", i.Repository, i.Number, strings.ReplaceAll(i.Check, "\n", " "), i.Category)
	}
	return b.String()
}

func openCount(ctx context.Context, c *github.Bound, repo string) (int, error) {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid repository")
	}
	var data struct {
		Repository *struct{ PullRequests *struct{ TotalCount *int } }
	}
	err := c.Query(ctx, repo, `query OpenCount($owner:String!,$name:String!){repository(owner:$owner,name:$name){pullRequests(states:OPEN){totalCount}}}`, map[string]any{"owner": parts[0], "name": parts[1]}, &data)
	if err != nil {
		return 0, err
	}
	if data.Repository == nil || data.Repository.PullRequests == nil || data.Repository.PullRequests.TotalCount == nil {
		return 0, fmt.Errorf("PR count unavailable")
	}
	return *data.Repository.PullRequests.TotalCount, nil
}

// FailureCode deliberately excludes provider bodies and request URLs from reports.
func FailureCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "API_TIMEOUT"
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "rate limit") || strings.Contains(text, "429") {
		return "API_RATE_LIMIT"
	}
	if rgerrors.IsKind(err, rgerrors.Permission) || rgerrors.IsKind(err, rgerrors.Authentication) || strings.Contains(text, "resource not accessible") {
		return "API_PERMISSION"
	}
	if strings.Contains(text, "timeout") || strings.Contains(text, "timed out") {
		return "API_TIMEOUT"
	}
	return "SCAN_INCOMPLETE"
}
