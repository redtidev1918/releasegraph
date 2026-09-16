package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/provider"
)

// repairCommand is the operator-facing repair entry point:
//
//	releasegraph repair --repo owner/name [--version X] [--plan-json]
//	releasegraph repair --repo owner/name [--version X] --apply
//
// Default is a side-effect-free plan; --apply executes the plan's actions
// through ReleaseGraph primitives only.
func repairCommand(w io.Writer, args []string) error {
	opts := statusOptions{}
	var apply, planJSON bool
	fs := flag.NewFlagSet("repair", flag.ContinueOnError)
	statusFlags(fs, &opts)
	fs.BoolVar(&apply, "apply", false, "execute the plan (default: plan only)")
	fs.BoolVar(&planJSON, "plan-json", false, "emit a plan document (apply with 'releasegraph apply-plan')")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx := context.Background()
	report, execution, err := observeReport(ctx, opts)
	if err != nil {
		return err
	}
	doc := provider.BuildPlanDoc(report, execution.Scope)
	if planJSON || opts.format == "json" {
		if err := write(w, "json", doc, nil); err != nil {
			return err
		}
	} else {
		humanPlan(w, doc)
	}
	exitCode = domain.ExitCodeFor(report.Verdict.Health)
	if !apply {
		return nil
	}
	if len(doc.Actions) == 0 {
		if opts.format != "json" && !planJSON {
			fmt.Fprintln(w, "\nnothing to do")
		}
		return nil
	}
	if err := applyPlanDoc(ctx, w, doc, report, execution, false); err != nil {
		return err
	}
	return nil
}

// applyPlanCommand revalidates a stored plan against fresh remote state before
// executing it. A plan that no longer matches reality is refused (RG_PLAN_STALE).
func applyPlanCommand(w io.Writer, args []string) error {
	var format, workflow string
	var apply bool
	fs := flags(&format)
	fs.BoolVar(&apply, "apply", false, "execute the plan (default: validate only)")
	fs.StringVar(&workflow, "workflow", "release.yml", "release workflow to dispatch for repair actions")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: releasegraph apply-plan <plan.json> [--apply]")
	}
	doc, err := provider.LoadPlan(rest[0])
	if err != nil {
		return err
	}

	// Re-observe with the plan's own scope before touching anything.
	opts := statusOptions{repo: doc.Repository, version: doc.Version, format: format}
	if doc.Scope == string(domain.ScopeFleet) {
		opts.scope = string(domain.ScopeFleet)
	}
	ctx := context.Background()
	report, execution, err := observeReport(ctx, opts)
	if err != nil {
		return err
	}
	if err := provider.ValidatePlan(doc, report); err != nil {
		exitCode = domain.ExitNeedsReview
		return err
	}
	if !apply {
		if format == "json" {
			return write(w, "json", map[string]any{"validated": true, "plan": doc, "mode": "dry-run"}, nil)
		}
		fmt.Fprintf(w, "plan validated against the current observation (%s)\n", doc.ObservedRevision)
		humanPlan(w, doc)
		fmt.Fprintln(w, "\ndry-run only; pass --apply to execute")
		return nil
	}
	return applyPlanDoc(ctx, w, doc, report, execution, false)
}

// applyPlanDoc executes a plan's actions through primitives and never does
// anything the plan did not list.
func applyPlanDoc(ctx context.Context, w io.Writer, doc provider.PlanDoc, report *provider.Report, execution domain.ExecutionContext, dryRun bool) error {
	allowed := map[string]bool{}
	for _, action := range doc.Actions {
		allowed[action.Type] = true
	}
	if allowed["provider_ack"] {
		mutations, err := provider.Acknowledge(ctx, reportClient(execution), report, dryRun)
		if err != nil {
			return err
		}
		if len(mutations) > 0 {
			fmt.Fprintf(w, "acknowledged %s %s (%d label change(s))\n", doc.Repository, doc.Version, len(mutations))
		}
	}
	if allowed["same_version_repair"] {
		inputs, err := provider.Repair(ctx, reportClient(execution), report, "release.yml", dryRun)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "dispatched same-version repair for %s %s (%v)\n", doc.Repository, doc.Version, inputs)
	}
	if !allowed["provider_ack"] && !allowed["same_version_repair"] {
		fmt.Fprintf(w, "no applicable action for %s %s\n", doc.Repository, doc.Version)
	}
	return nil
}

func humanPlan(w io.Writer, doc provider.PlanDoc) {
	fmt.Fprintf(w, "%s %s\n", doc.Repository, doc.Version)
	fmt.Fprintf(w, "observed revision  %s\n", doc.ObservedRevision)
	fmt.Fprintf(w, "health             %s (%s)\n", doc.Health, doc.Diagnosis)
	if len(doc.Actions) == 0 {
		fmt.Fprintln(w, "actions            none")
	} else {
		fmt.Fprintln(w, "actions:")
		for _, action := range doc.Actions {
			fmt.Fprintf(w, "  %-20s risk=%s\n", action.Type, action.Risk)
		}
	}
	if len(doc.Forbidden) > 0 {
		fmt.Fprintf(w, "forbidden          %v\n", doc.Forbidden)
	}
}

// reportClient binds a client for the execution context a plan was made under.
func reportClient(execution domain.ExecutionContext) *github.Bound {
	return github.New().Bind(execution)
}
