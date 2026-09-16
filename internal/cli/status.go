package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/policy"
	"github.com/redtidev1918/releasegraph/internal/provider"
	"github.com/redtidev1918/releasegraph/internal/registry"
	"github.com/redtidev1918/releasegraph/internal/rollout"
)

// Agent-facing status schema. Stable and documented: agents must never have to
// reconstruct release state from human output.
const statusSchemaVersion = 1

// Action is one safe, primitive-backed next step.
type Action struct {
	Type    string `json:"type"`
	Version string `json:"version,omitempty"`
	Command string `json:"command,omitempty"`
	Risk    string `json:"risk"`
}

// ObserveSummary is the observed remote state, flattened for machines.
type ObserveSummary struct {
	Tag           string `json:"tag"`
	TagCorrect    bool   `json:"tagCorrect"`
	GitHubRelease bool   `json:"githubRelease"`
	Latest        bool   `json:"latest"`
	Assets        bool   `json:"assets"`
	Checksums     bool   `json:"checksums"`
	Registries    bool   `json:"registries"`
	Provider      string `json:"provider"`
	ProviderState string `json:"providerState"`
	// ProviderEvidence names the signal that identified the provider state:
	// title, manifest, label, or no-pending-label.
	ProviderEvidence string `json:"providerEvidence,omitempty"`
	PolicyHashMatch  bool   `json:"policyHashMatches"`
}

// StatusReport is the machine-readable answer to "where does this stand?".
type StatusReport struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Command       string              `json:"command"`
	Repository    string              `json:"repository"`
	Version       string              `json:"version"`
	Provider      string              `json:"provider"`
	Scope         string              `json:"executionScope"`
	Credential    string              `json:"credentialClass"`
	Capabilities  policy.Capabilities `json:"capabilities"`
	Observed      ObserveSummary      `json:"observed"`
	Diagnosis     string              `json:"diagnosis"`
	Health        string              `json:"health"`
	Code          string              `json:"code"`
	ExitCode      int                 `json:"exitCode"`
	Reason        string              `json:"reason,omitempty"`
	SafeActions   []Action            `json:"safeActions"`
	Forbidden     []string            `json:"forbiddenActions"`
}

type statusOptions struct {
	format   string
	repo     string
	version  string
	path     string
	scope    string
	manifest string
	workflow string
}

func statusFlags(fs *flag.FlagSet, opts *statusOptions) {
	fs.StringVar(&opts.format, "output", "human", "human or json")
	fs.StringVar(&opts.format, "format", "human", "alias for --output")
	fs.StringVar(&opts.repo, "repo", "", "repository as owner/name (defaults to the current checkout)")
	fs.StringVar(&opts.version, "version", "", "version to inspect")
	fs.StringVar(&opts.path, "path", "", "policy file (defaults to the local .release-policy.yml)")
	fs.StringVar(&opts.scope, "scope", "", "force execution scope: repository or fleet")
	fs.StringVar(&opts.manifest, "manifest", "fleet.yaml", "fleet manifest path")
	fs.StringVar(&opts.workflow, "workflow", ".github/workflows/release.yml", "release workflow used to read the infrastructure pin")
}

// statusCommand reports the observed release state and the safe next actions.
func statusCommand(w io.Writer, args []string) error {
	opts := statusOptions{}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	statusFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	report, err := observeStatus(context.Background(), opts)
	if err != nil {
		return err
	}
	exitCode = report.ExitCode
	return emitStatus(w, opts.format, report, false)
}

// explainCommand answers "why is this the diagnosis?".
func explainCommand(w io.Writer, args []string) error {
	opts := statusOptions{}
	fs := flag.NewFlagSet("explain", flag.ContinueOnError)
	statusFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	report, err := observeStatus(context.Background(), opts)
	if err != nil {
		return err
	}
	exitCode = report.ExitCode
	return emitStatus(w, opts.format, report, true)
}

func emitStatus(w io.Writer, format string, report StatusReport, explain bool) error {
	if format == "json" || format == "" {
		return write(w, "json", report, nil)
	}
	humanStatus(w, report, explain)
	return nil
}

// observeStatus performs the shared OBSERVE → CLASSIFY → PLAN pipeline.
func observeStatus(ctx context.Context, opts statusOptions) (StatusReport, error) {
	report, execution, err := observeReport(ctx, opts)
	if err != nil {
		return StatusReport{}, err
	}
	_ = execution
	return statusFromReport(report, execution), nil
}

// observeReport returns the raw observation plus the execution context it was
// made under, so plan/apply can fingerprint and revalidate it.
func observeReport(ctx context.Context, opts statusOptions) (*provider.Report, domain.ExecutionContext, error) {
	providerOpts := providerOptions{repo: opts.repo, version: opts.version, path: opts.path, scope: opts.scope, manifest: opts.manifest}
	execution, err := executionContext(providerOpts)
	if err != nil {
		return nil, execution, err
	}
	client, err := boundClient(execution)
	if err != nil {
		return nil, execution, err
	}
	repository, p, err := resolveLocalPolicy(ctx, client, opts, execution)
	if err != nil {
		return nil, execution, err
	}
	version := opts.version
	if version == "" {
		version, err = desiredVersionLocal(ctx, client, repository, p)
		if err != nil {
			return nil, execution, err
		}
	}

	report, err := provider.Inspect(ctx, client, registry.New(), p, repository, version)
	if err != nil {
		return nil, execution, err
	}
	return report, execution, nil
}

// statusFromReport renders an observation as the agent-facing status document.
func statusFromReport(report *provider.Report, execution domain.ExecutionContext) StatusReport {
	caps := report.Observed.Capabilities
	status := StatusReport{
		SchemaVersion: statusSchemaVersion,
		Command:       "status",
		Repository:    report.Context.Repository,
		Version:       string(report.Context.Version),
		Provider:      string(report.Observed.Provider),
		Scope:         string(execution.Scope),
		Credential:    string(execution.CredentialClass),
		Capabilities:  caps,
		Observed: ObserveSummary{
			Tag:              report.Context.Tag,
			TagCorrect:       report.Observed.Actual.TagExists && report.Observed.Actual.TagCommit == report.Observed.Actual.ExpectedCommit,
			GitHubRelease:    report.Observed.Actual.ReleaseExists,
			Latest:           report.Observed.Actual.Latest,
			Assets:           report.Observed.Actual.AssetsComplete,
			Checksums:        report.Observed.Actual.ChecksumsVerified,
			Registries:       report.Observed.Actual.RegistriesHealthy,
			Provider:         string(report.Observed.Provider),
			ProviderState:    string(report.Observed.ProviderState),
			ProviderEvidence: report.Context.ProviderEvidence,
			PolicyHashMatch:  report.Context.PolicyHashMatches,
		},
		Diagnosis: string(report.Verdict.Drift),
		Health:    string(report.Verdict.Health),
		Code:      codeFor(report.Verdict),
		Reason:    report.Verdict.Reason,
	}
	status.ExitCode = domain.ExitCodeFor(report.Verdict.Health)
	status.SafeActions = safeActions(report, execution)
	status.Forbidden = forbiddenActions(report)
	return status
}

// resolveLocalPolicy prefers the checkout's own policy so the CLI works inside a
// business repository without any fleet credential.
func resolveLocalPolicy(ctx context.Context, client *github.Bound, opts statusOptions, execution domain.ExecutionContext) (string, *policy.Policy, error) {
	self := os.Getenv("GITHUB_REPOSITORY")
	repository := opts.repo
	if repository == "" {
		repository = self
	}
	// The checkout's own policy only describes the checkout's own repository.
	// Asking about another repository must read that repository's policy.
	localRepo := currentRepository("")
	local := opts.repo == "" || (localRepo != "" && strings.EqualFold(localRepo, opts.repo))
	path := opts.path
	if path == "" && local {
		candidate := ".release-policy.yml"
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			path = candidate
		}
	}
	if path != "" {
		loaded, err := policy.Load(path)
		if err != nil {
			return "", nil, err
		}
		if repository == "" {
			repository = repositoryFromGit(ctx)
		}
		return repository, loaded, nil
	}
	if repository == "" {
		return "", nil, fmt.Errorf("no local policy and no --repo given")
	}
	loaded, err := loadProviderPolicy(ctx, client, repository, "")
	if err != nil {
		return "", nil, err
	}
	return repository, loaded, nil
}

// repositoryFromGit derives owner/name from the local git remote.
func repositoryFromGit(ctx context.Context) string {
	out, err := runGit(ctx, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	url := strings.TrimSpace(out)
	url = strings.TrimSuffix(url, ".git")
	if idx := strings.LastIndex(url, "github.com"); idx >= 0 {
		rest := strings.TrimLeft(url[idx+len("github.com"):], ":/")
		parts := strings.Split(rest, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
	}
	return ""
}

// desiredVersionLocal reads the version from the local manifest when possible.
func desiredVersionLocal(ctx context.Context, client *github.Bound, repository string, p *policy.Policy) (string, error) {
	mode := p.Versioning.Mode
	if mode == "" {
		mode = p.Versioning.Provider
	}
	if mode != "release-please" {
		return policy.DesiredVersion(p, "", ".")
	}
	manifest := p.Versioning.Manifest
	if manifest == "" {
		manifest = ".release-please-manifest.json"
	}
	if raw, err := os.ReadFile(manifest); err == nil {
		return policy.DesiredVersionFromManifest(p, raw)
	}
	return desiredVersionFor(ctx, client, repository, p)
}

// codeFor maps a verdict to a stable machine-readable outcome code.
func codeFor(v provider.Verdict) string {
	switch {
	case v.HardFail:
		return domain.CodeTagConflict
	case v.Waived:
		return domain.CodeNeedsReview
	case v.ACKAllowed:
		return domain.CodeSafeReconcileAvailable
	case v.Health == domain.HealthRunning:
		return domain.CodeTransientRetry
	case v.Health == domain.HealthHealthy:
		return domain.CodeHealthy
	case v.RepairSameVersion:
		switch v.Drift {
		case provider.DriftACKMissing:
			return domain.CodeProviderACKMissing
		case provider.DriftFalseACK:
			return domain.CodeProviderFalseACK
		case provider.DriftTagMissing:
			return domain.CodeReleaseIncomplete
		default:
			return domain.CodeRepairAvailable
		}
	default:
		return domain.CodeBlocked
	}
}

func safeActions(report *provider.Report, execution domain.ExecutionContext) []Action {
	actions := []Action{}
	version := string(report.Context.Version)
	switch {
	case report.Verdict.Health == domain.HealthRunning:
		actions = append(actions, Action{
			Type:    "wait_and_reobserve",
			Version: version,
			Command: fmt.Sprintf("releasegraph status --repo %s --version %s", report.Context.Repository, version),
			Risk:    "none",
		})
	case report.Verdict.ACKAllowed:
		actions = append(actions, Action{
			Type:    "provider_ack",
			Version: version,
			Command: fmt.Sprintf("releasegraph provider reconcile --repo %s --version %s --apply", report.Context.Repository, version),
			Risk:    "low",
		})
	case report.Verdict.RepairSameVersion && !report.Verdict.HardFail:
		verb := "--apply"
		if execution.Scope == domain.ScopeRepository {
			verb = "(run from the control plane: fleet scope + RELEASEGRAPH_FLEET_TOKEN)"
		}
		actions = append(actions, Action{
			Type:    "same_version_repair",
			Version: version,
			Command: fmt.Sprintf("releasegraph provider repair --repo %s --version %s %s", report.Context.Repository, version, verb),
			Risk:    "medium",
		})
	}
	return actions
}

func forbiddenActions(report *provider.Report) []string {
	forbidden := []string{"move_existing_tag", "fabricate_historical_release"}
	if report.Verdict.Health == domain.HealthRunning {
		// Never race an in-flight transaction.
		return append(forbidden, "repair_in_flight_transaction", "provider_ack_while_draft")
	}
	if report.Verdict.Health != domain.HealthHealthy {
		forbidden = append(forbidden, "create_new_version")
	}
	if report.Verdict.HardFail {
		forbidden = append(forbidden, "automatic_repair")
	}
	if !report.Verdict.ACKAllowed {
		forbidden = append(forbidden, "manual_provider_label_edit")
	}
	return forbidden
}

func humanStatus(w io.Writer, report StatusReport, explain bool) {
	fmt.Fprintf(w, "Repository       %s\n", report.Repository)
	fmt.Fprintf(w, "Version          %s\n", report.Version)
	fmt.Fprintf(w, "Provider         %s\n", report.Provider)
	fmt.Fprintf(w, "Scope            %s (%s credential)\n", report.Scope, report.Credential)
	fmt.Fprintln(w, "Contract         "+contractLine(report.Capabilities))
	fmt.Fprintf(w, "Tag              %s\n", marker(report.Observed.TagCorrect))
	if report.Capabilities.GitHubRelease {
		fmt.Fprintf(w, "GitHub Release   %s%s\n", marker(report.Observed.GitHubRelease && report.Observed.Latest), latestNote(report))
	} else {
		fmt.Fprintln(w, "GitHub Release   not required")
	}
	if len(report.Capabilities.Assets) > 0 {
		fmt.Fprintf(w, "Assets           %s\n", marker(report.Observed.Assets))
	} else {
		fmt.Fprintln(w, "Assets           not required")
	}
	if len(report.Capabilities.Registries) > 0 {
		fmt.Fprintf(w, "Registries       %s (%s)\n", marker(report.Observed.Registries), strings.Join(report.Capabilities.Registries, ", "))
	} else {
		fmt.Fprintln(w, "Registries       not required")
	}
	fmt.Fprintf(w, "Provider ACK     %s\n", providerACKLine(report))
	fmt.Fprintf(w, "\n%s\n", report.Health)
	fmt.Fprintf(w, "Code             %s (exit %d)\n", report.Code, report.ExitCode)
	if report.Reason != "" {
		fmt.Fprintf(w, "Reason           %s\n", report.Reason)
	}
	if explain {
		fmt.Fprintf(w, "Diagnosis        %s\n", report.Diagnosis)
		if report.Observed.PolicyHashMatch {
			fmt.Fprintln(w, "Contract source  current policy")
		} else {
			fmt.Fprintln(w, "Contract source  the release's own metadata (policy changed since)")
		}
	}
	if len(report.SafeActions) > 0 {
		fmt.Fprintln(w, "\nSafe actions:")
		for _, action := range report.SafeActions {
			fmt.Fprintf(w, "  %-20s %s\n", action.Type, action.Command)
		}
	}
	if len(report.Forbidden) > 0 {
		fmt.Fprintf(w, "\nForbidden: %s\n", strings.Join(report.Forbidden, ", "))
	}
}

func contractLine(caps policy.Capabilities) string {
	parts := []string{}
	if caps.GitHubRelease {
		parts = append(parts, "github-release")
	}
	if caps.Binaries {
		parts = append(parts, "binaries")
	}
	if len(caps.Assets) > 0 {
		parts = append(parts, fmt.Sprintf("%d asset pattern(s)", len(caps.Assets)))
	}
	if caps.Checksums {
		parts = append(parts, "checksums")
	}
	for _, name := range caps.Registries {
		parts = append(parts, name)
	}
	if len(parts) == 0 {
		return "tag only"
	}
	return strings.Join(parts, ", ")
}

func latestNote(report StatusReport) string {
	if report.Capabilities.GitHubRelease && !report.Observed.Latest {
		return " (not Latest)"
	}
	return ""
}

func providerACKLine(report StatusReport) string {
	if report.Observed.ProviderState == "NONE" {
		return "not applicable"
	}
	return fmt.Sprintf("%s (%s)", marker(report.Observed.ProviderState == "TAGGED"), report.Observed.ProviderState)
}

func marker(ok bool) string {
	if ok {
		return "✓"
	}
	return "✗"
}

// agentContextCommand prints everything an agent needs before acting.
func agentContextCommand(w io.Writer, args []string) error {
	opts := statusOptions{}
	fs := flag.NewFlagSet("agent-context", flag.ContinueOnError)
	statusFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}

	type rules struct {
		CrossRepoMutation       bool `json:"crossRepoMutation"`
		ManualTagMove           bool `json:"manualTagMove"`
		ManualReleaseCreation   bool `json:"manualReleaseCreation"`
		ManualProviderLabelling bool `json:"manualProviderLabelling"`
	}
	type context struct {
		SchemaVersion int                 `json:"schemaVersion"`
		Repository    string              `json:"repository"`
		Managed       *bool               `json:"managed"`
		Manifest      string              `json:"fleetManifest"`
		Scope         string              `json:"executionScope"`
		Credential    string              `json:"credentialClass"`
		Canary        bool                `json:"canary,omitempty"`
		PolicyPath    string              `json:"policyPath,omitempty"`
		PolicySchema  int                 `json:"policySchema"`
		Capabilities  policy.Capabilities `json:"capabilities"`
		PinnedRef     string              `json:"releasegraphPin,omitempty"`
		PinnedVersion string              `json:"releasegraphVersion,omitempty"`
		SafeCommands  []string            `json:"safeCommands"`
		Rules         rules               `json:"rules"`
	}

	managed := (*bool)(nil)
	var canary bool
	if manifest, err := fleet.LoadManifest(opts.manifest); err == nil {
		if name := currentRepository(opts.repo); name != "" {
			value, _ := manifest.Authority(name)
			managed = &value
			if entry, ok := manifest.Entry(name); ok {
				canary = entry.Canary
			}
		}
	}

	policyPath := opts.path
	if policyPath == "" {
		if _, err := os.Stat(".release-policy.yml"); err == nil {
			policyPath = ".release-policy.yml"
		}
	}
	var caps policy.Capabilities
	if policyPath != "" {
		if loaded, err := policy.Load(policyPath); err == nil {
			caps = loaded.CapabilitiesOf()
		}
	}

	pinned := rollout.Pin{}
	if raw, err := os.ReadFile(filepath.Clean(opts.workflow)); err == nil {
		pinned = rollout.ParsePin(raw)
	}

	repository := currentRepository(opts.repo)
	payload := context{
		SchemaVersion: statusSchemaVersion,
		Repository:    repository,
		Managed:       managed,
		Manifest:      opts.manifest,
		Scope:         string(domain.ScopeRepository),
		Credential:    string(domain.CredentialRepository),
		Canary:        canary,
		PolicyPath:    policyPath,
		PolicySchema:  1,
		Capabilities:  caps,
		PinnedRef:     pinned.Ref,
		PinnedVersion: pinned.Version,
		SafeCommands: []string{
			"releasegraph status --json",
			"releasegraph explain --json",
			"releasegraph doctor --json",
		},
		Rules: rules{},
	}
	if os.Getenv("RELEASEGRAPH_FLEET_TOKEN") != "" {
		payload.Scope = string(domain.ScopeFleet)
		payload.Credential = string(domain.CredentialFleet)
	}
	if opts.format == "json" || opts.format == "" {
		return write(w, "json", payload, nil)
	}
	fmt.Fprintf(w, "repository     %s\n", payload.Repository)
	fmt.Fprintf(w, "scope          %s (%s)\n", payload.Scope, payload.Credential)
	if managed != nil {
		fmt.Fprintf(w, "managed        %v (canary=%v)\n", *managed, canary)
	} else {
		fmt.Fprintf(w, "managed        unknown (no %s in this checkout)\n", opts.manifest)
	}
	if payload.PolicyPath != "" {
		fmt.Fprintf(w, "contract       %s\n", contractLine(caps))
	}
	if pinned.Ref != "" {
		fmt.Fprintf(w, "infrastructure %s\n", pinned.Ref)
	}
	fmt.Fprintln(w, "\nsafe commands:")
	for _, command := range payload.SafeCommands {
		fmt.Fprintln(w, "  "+command)
	}
	fmt.Fprintln(w, "\nforbidden: manual tag move, manual release creation, manual provider labelling, cross-repo mutation in repository scope")
	return nil
}

// currentRepository resolves the repository under the current checkout.
func currentRepository(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if self := os.Getenv("GITHUB_REPOSITORY"); self != "" {
		return self
	}
	return repositoryFromGit(context.Background())
}
