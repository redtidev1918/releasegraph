package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"time"

	"github.com/redtidev1918/releasegraph/internal/assets"
	rgdomain "github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/graph"
	rgplan "github.com/redtidev1918/releasegraph/internal/plan"
	"github.com/redtidev1918/releasegraph/internal/policy"
	"github.com/redtidev1918/releasegraph/internal/reconcile"
	"github.com/redtidev1918/releasegraph/internal/static"
)

var version = "0.1.0-go-readonly"

type Envelope struct {
	SchemaVersion       int        `json:"schemaVersion"`
	ReleaseGraphVersion string     `json:"releasegraphVersion"`
	GeneratedAt         string     `json:"generatedAt"`
	Data                any        `json:"data,omitempty"`
	Error               *ErrorBody `json:"error,omitempty"`
}

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// exitCode carries a documented outcome code out of a reporting command so
// automation can distinguish healthy / repair available / retry / needs a human
// / broken invariant without parsing output.
var exitCode int

func Run(args []string, stdout, stderr io.Writer) int {
	exitCode = 0
	if len(args) == 0 {
		usage(stderr)
		return rgdomain.ExitUsage
	}
	var err error
	switch args[0] {
	case "fleet":
		err = fleetCommand(stdout, args[1:])
	case "branch-contract":
		err = branchContractCommand(stdout, args[1:])
	case "pr-lifecycle":
		err = prLifecycleCommand(stdout, args[1:])
	case "doctor":
		err = doctor(stdout, args[1:])
	case "graph":
		err = graphCommand(stdout, args[1:])
	case "audit":
		err = auditCommand(stdout, args[1:])
	case "inspect":
		err = inspect(stdout, args[1:])
	case "plan":
		err = planCommand(stdout, args[1:])
	case "provider":
		err = providerCommand(stdout, args[1:])
	case "rollout":
		err = rolloutCommand(stdout, args[1:])
	case "status":
		err = statusCommand(stdout, args[1:])
	case "explain":
		err = explainCommand(stdout, args[1:])
	case "agent-context":
		err = agentContextCommand(stdout, args[1:])
	case "repair":
		err = repairCommand(stdout, args[1:])
	case "apply-plan":
		err = applyPlanCommand(stdout, args[1:])
	case "init":
		err = initCommand(stdout, args[1:])
	case "migrate":
		err = migrateCommand(stdout, args[1:])
	case "static-check":
		err = staticCheck(stdout, args[1:])
	case "version":
		fmt.Fprintln(stdout, version)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		usage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "ERROR", err)
		return exitCodeForError(err)
	}
	return exitCode
}

// exitCodeForError maps a failure onto the documented exit code contract.
func exitCodeForError(err error) int {
	switch {
	case rgerrors.IsKind(err, rgerrors.ScopeViolation), rgerrors.IsKind(err, rgerrors.FleetCredentialRequired):
		return rgdomain.ExitNeedsReview
	case rgerrors.IsKind(err, rgerrors.Transient):
		return rgdomain.ExitTransientRetry
	case rgerrors.IsKind(err, rgerrors.TagConflict), rgerrors.IsKind(err, rgerrors.InvariantViolation):
		return rgdomain.ExitBroken
	case rgerrors.IsKind(err, rgerrors.PlanStale), rgerrors.IsKind(err, rgerrors.Permission):
		return rgdomain.ExitNeedsReview
	case rgerrors.IsKind(err, rgerrors.Policy), rgerrors.IsKind(err, rgerrors.VersionConflict):
		return rgdomain.ExitBlocked
	default:
		return 1
	}
}

func flags(outputFormat *string) *flag.FlagSet {
	fs := flag.NewFlagSet("", flag.ContinueOnError)
	fs.StringVar(outputFormat, "output", "human", "human, json, or mermaid")
	fs.StringVar(outputFormat, "format", "human", "alias for --output")
	return fs
}

func doctor(w io.Writer, args []string) error {
	var format string
	fs := flags(&format)
	if err := fs.Parse(args); err != nil {
		return err
	}
	info := map[string]any{"status": "ok", "mode": "read-only", "serverRequired": false, "databaseRequired": false, "commands": []string{"agent-context", "apply-plan", "audit", "branch-contract", "doctor", "explain", "fleet", "graph", "init", "inspect", "migrate", "plan", "pr-lifecycle", "provider", "repair", "rollout", "static-check", "status", "version"}}
	return write(w, format, info, humanDoctor)
}

func graphCommand(w io.Writer, args []string) error {
	var format, path string
	fs := flags(&format)
	fs.StringVar(&path, "file", "release-graph.yml", "release graph path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	g, err := graph.Load(path)
	if err != nil {
		return err
	}
	view, err := graph.BuildView(g)
	if err != nil {
		return err
	}
	if format == "mermaid" {
		text, err := graph.Mermaid(g)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(w, text)
		return err
	}
	return write(w, format, view, func(w io.Writer, _ any) {
		fmt.Fprintln(w, "READY")
		for _, id := range view.Ready {
			fmt.Fprintln(w, "  "+id)
		}
		if len(view.Blocked) > 0 {
			fmt.Fprintln(w, "BLOCKED")
			ids := make([]string, 0)
			for id := range view.Blocked {
				ids = append(ids, id)
			}
			for _, id := range ids {
				fmt.Fprintf(w, "  %s: %v\n", id, view.Blocked[id])
			}
		}
		fmt.Fprintln(w, "ORDER", view.Order)
	})
}

func auditCommand(w io.Writer, args []string) error {
	var format, policyPath, root string
	fs := flags(&format)
	fs.StringVar(&policyPath, "path", ".release-policy.yml", "policy path")
	fs.StringVar(&root, "root", "dist/release", "asset directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(policyPath)
	if err != nil {
		return err
	}
	gate, err := assets.Evaluate(p, root)
	if err != nil {
		return err
	}
	return write(w, format, gate, func(w io.Writer, _ any) {
		for _, asset := range gate.Required {
			fmt.Fprintf(w, "required %s %d %s\n", asset.Name, asset.Size, asset.SHA256)
		}
	})
}

func inspect(w io.Writer, args []string) error {
	var format, path string
	fs := flags(&format)
	fs.StringVar(&path, "path", ".release-policy.yml", "policy path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(path)
	if err != nil {
		return err
	}
	return write(w, format, p, func(w io.Writer, _ any) {
		fmt.Fprintf(w, "kind=%s versioning=%s requiredAssets=%d registries=%d policyHash=%s\n", p.Kind, effectiveMode(p), len(p.Assets.Required), len(p.Registries), p.Hash)
	})
}

func planCommand(w io.Writer, args []string) error {
	var format, policyPath, version, root, graphPath, statePath string
	var live bool
	fs := flags(&format)
	fs.StringVar(&policyPath, "path", ".release-policy.yml", "policy path")
	fs.StringVar(&version, "version", "", "explicit version")
	fs.StringVar(&root, "root", ".", "repository root")
	fs.StringVar(&graphPath, "graph", "", "release graph path")
	fs.StringVar(&statePath, "state", "", "actual project health JSON")
	fs.BoolVar(&live, "live", false, "inspect current GitHub state")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if graphPath != "" {
		g, err := graph.Load(graphPath)
		if err != nil {
			return err
		}
		if live && statePath != "" {
			return fmt.Errorf("--live and --state are mutually exclusive")
		}
		if live {
			out, err := reconcile.Inspect(context.Background(), github.New(), g)
			if err != nil {
				return err
			}
			return write(w, format, out, humanLivePlan)
		}
		health, err := rgplan.LoadHealth(statePath)
		if err != nil {
			return err
		}
		out, err := rgplan.Evaluate(g, health)
		if err != nil {
			return err
		}
		return write(w, format, out, func(w io.Writer, _ any) {
			fmt.Fprintln(w, "READY")
			for _, id := range out.Ready {
				fmt.Fprintln(w, "  "+id)
			}
			if len(out.Blocked) > 0 {
				fmt.Fprintln(w, "BLOCKED")
				ids := make([]string, 0)
				for id := range out.Blocked {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					fmt.Fprintf(w, "  %s: %v\n", id, out.Blocked[id])
				}
			}
			if len(out.Noop) > 0 {
				fmt.Fprintln(w, "NOOP")
				for _, id := range out.Noop {
					fmt.Fprintln(w, "  "+id)
				}
			}
		})
	}
	p, err := policy.Load(policyPath)
	if err != nil {
		return err
	}
	desired, err := policy.DesiredVersion(p, version, root)
	if err != nil {
		return err
	}
	node := rgdomain.NodePlan{ID: "local", Kind: rgdomain.NodeKindRelease, Health: rgdomain.HealthReady, Desired: &rgdomain.DesiredState{Version: rgdomain.Version(desired)}}
	out := rgdomain.Plan{Ready: []string{"local"}, Blocked: []string{}, Noop: []string{}, Nodes: []rgdomain.NodePlan{node}}
	return write(w, format, out, func(w io.Writer, _ any) {
		fmt.Fprintf(w, "READY\n  local desired=%s tag=%s requiredAssets=%d\n", desired, policy.ReleaseTag(p, desired), len(p.Assets.Required))
	})
}

func staticCheck(w io.Writer, args []string) error {
	root := "."
	fs := flag.NewFlagSet("static-check", flag.ContinueOnError)
	fs.StringVar(&root, "root", root, "repository root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := static.Check(root); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, "static check ok")
	return err
}

func write(w io.Writer, format string, data any, human func(io.Writer, any)) error {
	if format == "json" || format == "" {
		return json.NewEncoder(w).Encode(Envelope{SchemaVersion: 1, ReleaseGraphVersion: version, GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano), Data: data})
	}
	if format == "human" {
		human(w, data)
		return nil
	}
	return fmt.Errorf("unsupported output format %q", format)
}
func humanDoctor(w io.Writer, _ any) { fmt.Fprintln(w, "ReleaseGraph read-only core: ok") }
func humanLivePlan(w io.Writer, data any) {
	plan := data.(*rgdomain.Plan)
	for _, heading := range []struct {
		name string
		ids  []string
	}{{"READY", plan.Ready}, {"BLOCKED", plan.Blocked}, {"NOOP", plan.Noop}} {
		if len(heading.ids) == 0 {
			continue
		}
		fmt.Fprintln(w, heading.name)
		for _, id := range heading.ids {
			fmt.Fprintln(w, "  "+id)
		}
	}
}
func effectiveMode(p *policy.Policy) string {
	if p.Versioning.Mode != "" {
		return p.Versioning.Mode
	}
	return p.Versioning.Provider
}
func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: releasegraph [audit|branch-contract|doctor|fleet|graph|inspect|plan|pr-lifecycle|provider|rollout|static-check|version]")
}
func Main() { os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr)) }

// actorName records who is acting, for the audit trail.
func actorName() string {
	if actor := os.Getenv("GITHUB_ACTOR"); actor != "" {
		return actor
	}
	return "operator"
}

// runGit runs a read-only git command for local repository discovery.
func runGit(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	return string(out), err
}
