package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/credential"
	"github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/policy"
	"github.com/redtidev1918/releasegraph/internal/provider"
	"github.com/redtidev1918/releasegraph/internal/registry"
)

// providerCommand implements:
//
//	releasegraph provider inspect   --repo owner/name [--version X]
//	releasegraph provider reconcile --repo owner/name [--version X] [--apply]
//	releasegraph provider reconcile --all --manifest fleet.yaml [--apply]
//	releasegraph provider repair    --repo owner/name --version X [--apply]
//	releasegraph provider waive     --repo owner/name --version X
//
// Scope rules:
//
//	repository scope — the repository operating on itself (its own workflow and
//	                   its own GITHUB_TOKEN). It cannot touch another repository.
//	fleet scope      — the control plane. Requires RELEASEGRAPH_FLEET_TOKEN and
//	                   targets must be declared in fleet.yaml.
type providerOptions struct {
	format   string
	repo     string
	version  string
	path     string
	owner    string
	manifest string
	scope    string
	repos    []string
	all      bool
	apply    bool
	workflow string
}

func providerCommand(w io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: releasegraph provider [inspect|reconcile|acknowledge|repair|waive]")
	}
	sub := args[0]
	opts := providerOptions{}
	fs := flag.NewFlagSet("provider "+sub, flag.ContinueOnError)
	fs.StringVar(&opts.format, "output", "human", "human or json")
	fs.StringVar(&opts.format, "format", "human", "alias for --output")
	fs.StringVar(&opts.repo, "repo", "", "repository as owner/name")
	fs.StringVar(&opts.version, "version", "", "version to reconcile (defaults to the repository manifest)")
	fs.StringVar(&opts.path, "path", "", "local policy file to use instead of the target repository policy")
	fs.StringVar(&opts.owner, "owner", "", "deprecated: owner discovery no longer defines the fleet")
	fs.StringVar(&opts.manifest, "manifest", "fleet.yaml", "fleet manifest (the fleet authority)")
	fs.StringVar(&opts.scope, "scope", "", "force execution scope: repository or fleet")
	fs.Var((*repoList)(&opts.repos), "repos", "comma-separated owner/name list (repeatable)")
	fs.BoolVar(&opts.all, "all", false, "scan every managed repository of the fleet manifest")
	fs.BoolVar(&opts.apply, "apply", false, "apply mutations (default is a side-effect-free plan)")
	fs.StringVar(&opts.workflow, "workflow", "release.yml", "release workflow file to dispatch for repair")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	switch sub {
	case "inspect":
		return providerInspect(w, opts)
	case "reconcile", "acknowledge":
		return providerReconcile(w, opts)
	case "repair":
		return providerRepair(w, opts)
	case "waive":
		return providerWaive(w, opts)
	default:
		return fmt.Errorf("unknown provider subcommand %q", sub)
	}
}

// executionContext resolves the scope this invocation runs under. It fails fast
// when the scope requires a credential class that is not configured.
func executionContext(opts providerOptions) (domain.ExecutionContext, error) {
	self := os.Getenv("GITHUB_REPOSITORY")
	actor := os.Getenv("GITHUB_ACTOR")
	if actor == "" {
		actor = "operator"
	}

	switch opts.scope {
	case "repository":
		// Repository scope is bound to the repository the process runs in, never
		// to whatever --repo happens to name.
		if self == "" {
			return domain.ExecutionContext{}, fmt.Errorf("--scope repository requires GITHUB_REPOSITORY (run inside the repository)")
		}
		return domain.ExecutionContext{Scope: domain.ScopeRepository, Repository: self, Actor: actor, CredentialClass: domain.CredentialRepository}, nil
	case "fleet":
		if err := credential.RequireFleet("provider command"); err != nil {
			return domain.ExecutionContext{}, err
		}
		return domain.ExecutionContext{Scope: domain.ScopeFleet, Actor: actor, CredentialClass: domain.CredentialFleet}, nil
	case "":
	default:
		return domain.ExecutionContext{}, fmt.Errorf("--scope must be repository or fleet")
	}

	// Implicit rules: a repository acting on itself is repository scope with its
	// own token; anything cross-repository belongs to the control plane.
	if !opts.all && len(opts.repos) == 0 && opts.repo != "" && self != "" && strings.EqualFold(opts.repo, self) {
		return domain.ExecutionContext{Scope: domain.ScopeRepository, Repository: opts.repo, Actor: actor, CredentialClass: domain.CredentialRepository}, nil
	}
	if err := credential.RequireFleet("cross-repository provider command"); err != nil {
		return domain.ExecutionContext{}, err
	}
	return domain.ExecutionContext{Scope: domain.ScopeFleet, Actor: actor, CredentialClass: domain.CredentialFleet}, nil
}

// boundClient builds a scope-bound GitHub client. The fleet credential is only
// installed for fleet scope; repository scope keeps GITHUB_TOKEN.
func boundClient(execution domain.ExecutionContext) (*github.Bound, error) {
	if execution.Scope == domain.ScopeFleet {
		if err := credential.UseFleetCredential(); err != nil {
			return nil, err
		}
	}
	return github.New().Bind(execution), nil
}

func providerInspect(w io.Writer, opts providerOptions) error {
	scan, err := scanProvider(context.Background(), opts)
	if err != nil {
		return err
	}
	if opts.format == "json" || opts.format == "" {
		return write(w, "json", scan, nil)
	}
	for _, report := range scan.Reports {
		humanProviderReport(w, report)
	}
	for _, failure := range scan.Errors {
		fmt.Fprintln(w, "ERROR", failure)
	}
	if len(scan.Errors) > 0 {
		return fmt.Errorf("%d repository inspection(s) failed", len(scan.Errors))
	}
	return nil
}

func providerReconcile(w io.Writer, opts providerOptions) error {
	ctx := context.Background()
	client, err := scanClientOnly(opts)
	if err != nil {
		return err
	}
	scan, err := scanTargets(ctx, opts, client)
	if err != nil {
		return err
	}
	applied := []provider.Report{}
	for _, report := range scan.Reports {
		if !report.Verdict.ACKAllowed {
			continue
		}
		mutations, err := provider.Acknowledge(ctx, client, &report, !opts.apply)
		if err != nil {
			scan.Errors = append(scan.Errors, fmt.Sprintf("%s: %v", report.Context.Repository, err))
			continue
		}
		report.PlannedACK = mutations
		applied = append(applied, report)
	}
	out := map[string]any{
		"mode": mode(opts.apply), "scope": scan.Execution.Scope, "credentialClass": scan.Execution.CredentialClass,
		"reports": scan.Reports, "acknowledged": applied, "errors": scan.Errors,
	}
	if opts.format == "json" || opts.format == "" {
		return write(w, "json", out, nil)
	}
	for _, report := range scan.Reports {
		humanProviderReport(w, report)
	}
	for _, report := range applied {
		verb := "would acknowledge"
		if opts.apply {
			verb = "acknowledged"
		}
		fmt.Fprintf(w, "  %s %s %s (PR #%d)\n", verb, report.Context.Repository, report.Context.Version, report.Context.ReleasePR)
	}
	for _, failure := range scan.Errors {
		fmt.Fprintln(w, "ERROR", failure)
	}
	return nil
}

// providerRepair resumes an incomplete version by dispatching the target
// repository own release workflow. ReleaseGraph never publishes for another
// repository, so this is a control plane operation by construction.
func providerRepair(w io.Writer, opts providerOptions) error {
	ctx := context.Background()
	if opts.scope == "" {
		opts.scope = "fleet"
	}
	client, err := scanClientOnly(opts)
	if err != nil {
		return err
	}
	scan, err := scanTargets(ctx, opts, client)
	if err != nil {
		return err
	}
	type outcome struct {
		Repository string            `json:"repository"`
		Version    string            `json:"version"`
		Drift      provider.Drift    `json:"drift"`
		Allowed    bool              `json:"allowed"`
		Reason     string            `json:"reason"`
		Inputs     map[string]string `json:"inputs,omitempty"`
		Dispatched bool              `json:"dispatched"`
	}
	outcomes := []outcome{}
	for _, report := range scan.Reports {
		allowed, reason, inputs := provider.RepairPlan(&report)
		item := outcome{Repository: report.Context.Repository, Version: string(report.Context.Version), Drift: report.Verdict.Drift, Allowed: allowed, Reason: reason, Inputs: inputs}
		if allowed && opts.apply {
			applied, err := provider.Repair(ctx, client, &report, opts.workflow, false)
			if err != nil {
				scan.Errors = append(scan.Errors, fmt.Sprintf("%s: %v", report.Context.Repository, err))
			} else {
				item.Inputs = applied
				item.Dispatched = true
			}
		}
		outcomes = append(outcomes, item)
	}
	if opts.format == "json" || opts.format == "" {
		return write(w, "json", map[string]any{
			"mode": mode(opts.apply), "scope": scan.Execution.Scope, "workflow": opts.workflow,
			"action": "workflow_dispatch", "outcomes": outcomes, "errors": scan.Errors,
		}, nil)
	}
	for _, item := range outcomes {
		state := "planned"
		if item.Dispatched {
			state = "dispatched"
		}
		if !item.Allowed {
			state = "skipped"
		}
		fmt.Fprintf(w, "%-10s %s %s (%s): %s\n", state, item.Repository, item.Version, item.Drift, item.Reason)
	}
	for _, failure := range scan.Errors {
		fmt.Fprintln(w, "ERROR", failure)
	}
	return nil
}

func providerWaive(w io.Writer, opts providerOptions) error {
	if opts.repo == "" || opts.version == "" {
		return fmt.Errorf("provider waive requires --repo and --version")
	}
	ctx := context.Background()
	scan, err := scanClientOnly(opts)
	if err != nil {
		return err
	}
	number, err := provider.Waive(ctx, scan, opts.repo, opts.version)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "waived %s %s (release PR #%d) — record why in the PR conversation\n", opts.repo, opts.version, number)
	return nil
}

func mode(apply bool) string {
	if apply {
		return "apply"
	}
	return "dry-run"
}

func scanClientOnly(opts providerOptions) (*github.Bound, error) {
	execution, err := executionContext(opts)
	if err != nil {
		return nil, err
	}
	return boundClient(execution)
}

func scanProvider(ctx context.Context, opts providerOptions) (*provider.ScanResult, error) {
	client, err := scanClientOnly(opts)
	if err != nil {
		return nil, err
	}
	return scanTargets(ctx, opts, client)
}

func scanTargets(ctx context.Context, opts providerOptions, client *github.Bound) (*provider.ScanResult, error) {
	execution := client.Context()
	verifier := registry.New()
	scan := &provider.ScanResult{Reports: []provider.Report{}, Errors: []string{}, Execution: execution}

	targets := []string{}
	switch {
	case opts.all:
		manifest, err := fleet.LoadManifest(opts.manifest)
		if err != nil {
			return nil, err
		}
		targets = manifest.EnabledNames()
	case len(opts.repos) > 0:
		targets = append(targets, opts.repos...)
	case opts.repo != "":
		targets = append(targets, opts.repo)
	default:
		return nil, fmt.Errorf("--repo owner/name, --repos list, or --all --manifest is required")
	}

	// Fleet scope obeys the manifest authority; repository scope is self-bound.
	var manifest *fleet.Manifest
	if execution.Scope == domain.ScopeFleet {
		loaded, err := fleet.LoadManifest(opts.manifest)
		if err != nil {
			return nil, err
		}
		manifest = loaded
	}

	sort.Strings(targets)
	for _, repo := range targets {
		if manifest != nil {
			allowed, classification := manifest.Authority(repo)
			if !allowed {
				scan.Errors = append(scan.Errors, fmt.Sprintf("%s: not an operable managed repository (%s)", repo, classification))
				continue
			}
		} else if !execution.Allows(repo) {
			// A repository-scoped process reaching for another repository is a
			// hard failure, not a soft per-repository error.
			return nil, rgerrors.New(rgerrors.ScopeViolation, fmt.Sprintf(
				"scope=repository bound to %q may not operate on %q", execution.Repository, repo))
		}
		p, err := loadProviderPolicy(ctx, client, repo, opts.path)
		if err != nil {
			scan.Errors = append(scan.Errors, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		version := opts.version
		if version == "" {
			version, err = desiredVersionFor(ctx, client, repo, p)
			if err != nil {
				scan.Errors = append(scan.Errors, fmt.Sprintf("%s: %v", repo, err))
				continue
			}
		}
		report, err := provider.Inspect(ctx, client, verifier, p, repo, version)
		if err != nil {
			scan.Errors = append(scan.Errors, fmt.Sprintf("%s: %v", repo, err))
			continue
		}
		scan.Reports = append(scan.Reports, *report)
	}
	return scan, nil
}

func loadProviderPolicy(ctx context.Context, client *github.Bound, repo, path string) (*policy.Policy, error) {
	if path != "" {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("policy file %s not found", path)
		}
		return policy.Load(path)
	}
	raw, found, err := client.ReadFile(ctx, repo, ".release-policy.yml", "")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf(".release-policy.yml is missing")
	}
	return policy.Parse(raw)
}

func desiredVersionFor(ctx context.Context, client *github.Bound, repo string, p *policy.Policy) (string, error) {
	versioningMode := p.Versioning.Mode
	if versioningMode == "" {
		versioningMode = p.Versioning.Provider
	}
	if versioningMode != "release-please" {
		return policy.DesiredVersion(p, "", ".")
	}
	manifestPath := p.Versioning.Manifest
	if manifestPath == "" {
		manifestPath = ".release-please-manifest.json"
	}
	raw, found, err := client.ReadFile(ctx, repo, manifestPath, "")
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("release manifest %s is missing", manifestPath)
	}
	return policy.DesiredVersionFromManifest(p, raw)
}

// humanProviderReport prints the diagnosis layout used in the operator docs.
func humanProviderReport(w io.Writer, r provider.Report) {
	fmt.Fprintf(w, "%s %s\n", r.Context.Repository, r.Context.Version)
	fmt.Fprintf(w, "  Release PR:  #%d merged=%v\n", r.Context.ReleasePR, r.Context.ReleasePR != 0)
	if r.Context.PRMergeSHA != "" {
		fmt.Fprintf(w, "  Merge commit: %s\n", r.Context.PRMergeSHA)
	}
	fmt.Fprintf(w, "  Tag:         %s correct=%v\n", r.Context.Tag, r.Observed.Actual.TagExists && r.Observed.Actual.TagCommit == r.Observed.Actual.ExpectedCommit)
	fmt.Fprintf(w, "  Release:     exists=%v latest=%v assets=%v checksums=%v\n",
		r.Observed.Actual.ReleaseExists, r.Observed.Actual.Latest, r.Observed.Actual.AssetsComplete, r.Observed.Actual.ChecksumsVerified)
	fmt.Fprintf(w, "  Provider:    %s state=%s\n", r.Observed.Provider, r.Observed.ProviderState)
	fmt.Fprintf(w, "  Diagnosis:   %s\n", r.Verdict.Drift)
	fmt.Fprintf(w, "  Health:      %s\n", r.Verdict.Health)
	fmt.Fprintf(w, "  Repair:      %s\n", repairLabel(r.Verdict))
	if r.Verdict.Waived {
		fmt.Fprintf(w, "  Waived:      yes (historical version accepted as-is)\n")
	}
	for _, m := range r.PlannedACK {
		fmt.Fprintf(w, "  ACK:         %s %s\n", m.Action, m.Label)
	}
	if r.Verdict.Reason != "" {
		fmt.Fprintf(w, "  Reason:      %s\n", r.Verdict.Reason)
	}
}

func repairLabel(v provider.Verdict) string {
	switch {
	case v.HardFail:
		return "unsafe (TAG_CONFLICT: manual intervention required)"
	case v.ACKAllowed:
		return "safe (acknowledge provider)"
	case v.RepairSameVersion:
		return "safe (repair same version first)"
	default:
		return "none"
	}
}

// repoList collects a repeatable, comma-separated flag value.
type repoList []string

func (l *repoList) String() string { return strings.Join(*l, ",") }

func (l *repoList) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*l = append(*l, item)
		}
	}
	return nil
}
