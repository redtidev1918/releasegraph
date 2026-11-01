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
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/prhealth"
)

func prHealthCommand(w io.Writer, args []string) error {
	if len(args) == 0 || args[0] != "audit" {
		return fmt.Errorf("pr-health requires audit")
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
