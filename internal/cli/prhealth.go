package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/redtidev1918/releasegraph/internal/credential"
	"github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/prhealth"
)

func prHealthCommand(w io.Writer, args []string) error {
	if len(args) == 0 || (args[0] != "audit" && args[0] != "alert") {
		return fmt.Errorf("pr-health requires audit or alert")
	}
	if args[0] == "alert" {
		return prHealthAlertCommand(w, args[1:])
	}
	var format, manifestPath, reportPath string
	fs := flags(&format)
	fs.StringVar(&manifestPath, "manifest", "fleet.yaml", "authoritative fleet manifest")
	fs.StringVar(&reportPath, "report", "pr-health.json", "JSON report path")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	manifest, err := fleet.LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	if err := credential.UseFleetCredential(); err != nil {
		return err
	}
	c := github.New().Bind(domain.ExecutionContext{Scope: domain.ScopeFleet, CredentialClass: domain.CredentialFleet})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	report := prhealth.Scan(ctx, c, manifest)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(reportPath, append(raw, '\n'), 0600); err != nil {
		return err
	}
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(prhealth.Summary(report))
		f.Close()
		if err != nil {
			return err
		}
	}
	if report.Status == "DEGRADED" {
		exitCode = 30
	} else if report.Status == "FAIL" {
		exitCode = 20
	}
	return write(w, format, report, func(w io.Writer, data any) { fmt.Fprint(w, prhealth.Summary(data.(prhealth.Report))) })
}

// prHealthAlertCommand persists a scan report's incidents on the single
// marked control-repository issue. It plans by default; --apply is the
// explicit act. The write credential is the repository-scoped GITHUB_TOKEN of
// the control repository — never the fleet token, so scanning and alerting
// credentials stay separate.
func prHealthAlertCommand(w io.Writer, args []string) error {
	var format, reportPath, repo string
	apply := false
	fs := flags(&format)
	fs.StringVar(&reportPath, "report", "pr-health.json", "JSON report produced by pr-health audit")
	fs.BoolVar(&apply, "apply", false, "write the alert issue (default: plan only)")
	fs.StringVar(&repo, "repo", os.Getenv("GITHUB_REPOSITORY"), "control repository persisting the alert issue")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if repo == "" {
		return fmt.Errorf("--repo or GITHUB_REPOSITORY is required: the alert issue lives in the control repository")
	}
	if credential.RepoToken() == "" {
		return rgerrors.New(rgerrors.FleetCredentialRequired,
			"pr-health alert requires the repository-scoped GITHUB_TOKEN of "+repo+"; it must not use the fleet token")
	}
	if fleetToken, _ := credential.Fleet(); fleetToken != "" && fleetToken == credential.RepoToken() {
		return rgerrors.New(rgerrors.InvariantViolation,
			"GITHUB_TOKEN carries the fleet credential; alerting and scanning credentials must stay separate")
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		return err
	}
	var report prhealth.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return err
	}
	if report.SchemaVersion != 1 || report.StartedAt.IsZero() {
		return fmt.Errorf("%s is not a pr-health audit report", reportPath)
	}
	c := github.New().Bind(domain.ExecutionContext{Scope: domain.ScopeRepository, Repository: repo, CredentialClass: domain.CredentialRepository})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	plan, err := prhealth.Alert(ctx, c, repo, report, apply)
	if err != nil {
		return err
	}
	summary := fmt.Sprintf("Fleet PR Health alert plan (apply=%t)\n  marked issue: %d\n  action: %s\n  active incidents: %d\n  new episodes: %d\n  recovered this scan: %d\n",
		apply, plan.Issue, plan.Action, plan.Active, len(plan.NewEpisodes), len(plan.Recovered))
	if path := os.Getenv("GITHUB_STEP_SUMMARY"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString("## " + summary)
		f.Close()
		if err != nil {
			return err
		}
	}
	if !apply {
		fmt.Fprintln(w, summary)
	}
	return nil
}
