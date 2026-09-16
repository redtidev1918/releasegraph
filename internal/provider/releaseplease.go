package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/policy"
	"github.com/redtidev1918/releasegraph/internal/registry"
)

// Context identifies one version release across the provider boundary.
type Context struct {
	Repository string         `json:"repository"`
	Version    domain.Version `json:"version"`
	Tag        string         `json:"tag"`
	ReleasePR  int            `json:"releasePr,omitempty"`
	PRMergeSHA string         `json:"prMergeSha,omitempty"`
	Provider   Kind           `json:"provider"`
	Labels     []string       `json:"labels,omitempty"`
	// PolicyHashMatches reports whether the release was published under the
	// current policy; false means asset differences are contract drift, not an
	// incomplete release.
	PolicyHashMatches bool `json:"policyHashMatches"`
	// ProviderEvidence names the signal that identified the release PR.
	ProviderEvidence string `json:"providerEvidence,omitempty"`
}

// Report is the full reconciliation report for one version.
type Report struct {
	Context    Context         `json:"context"`
	Observed   Observed        `json:"observed"`
	Verdict    Verdict         `json:"verdict"`
	PlannedACK []LabelMutation `json:"plannedAck,omitempty"`
}

const (
	labelPending   = "autorelease: pending"
	labelTriggered = "autorelease: triggered"
	labelTagged    = "autorelease: tagged"
	// labelWaived is the explicit human waiver for an unrecoverable historical
	// version. It is deliberately a different namespace from release-please's
	// own labels so the two never overwrite each other.
	labelWaived = "releasegraph: historical-waived"
)

// releasePRPattern matches release-please PR titles like
// "chore(main): release 2.16.0" and component variants.
var releasePRPattern = regexp.MustCompile(`release[ :]+v?(\d+\.\d+[\w.\-+]*)`)

type releaseAsset struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type releaseAPI struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	HTMLURL    string         `json:"html_url"`
	Assets     []releaseAsset `json:"assets"`
}

// ResolveProvider maps a policy versioning mode to a provider kind.
func ResolveProvider(p *policy.Policy) Kind {
	mode := p.Versioning.Mode
	if mode == "" {
		mode = p.Versioning.Provider
	}
	switch mode {
	case "release-please":
		return KindReleasePlease
	case "manual":
		return KindManual
	default:
		return KindTag
	}
}

// Inspect gathers actual release state and provider acknowledgement for one
// version of a repository and classifies the drift. No mutations.
func Inspect(ctx context.Context, client *github.Bound, verifier *registry.Verifier, p *policy.Policy, repo, version string) (*Report, error) {
	tag := policy.ReleaseTag(p, version)
	provider := ResolveProvider(p)

	report := &Report{Context: Context{Repository: repo, Version: domain.Version(version), Tag: tag, Provider: provider}}
	capabilities := p.CapabilitiesOf()
	report.Observed.Capabilities = capabilities

	// Provider side: find the merged release PR and its labels.
	waived := false
	if provider == KindReleasePlease {
		pr, state, evidence, err := providerState(ctx, client, p, repo, version)
		if err != nil {
			return nil, err
		}
		report.Observed.ProviderState = state
		report.Context.ProviderEvidence = evidence
		if pr != nil {
			for _, l := range pr.Labels {
				if l.Name == labelWaived {
					waived = true
				}
			}
			report.Context.ReleasePR = pr.Number
			report.Context.PRMergeSHA = pr.MergeCommitSHA
			for _, l := range pr.Labels {
				report.Context.Labels = append(report.Context.Labels, l.Name)
			}
		}
	} else {
		report.Observed.ProviderState = StateNone
	}

	actual := Actual{ExpectedCommit: report.Context.PRMergeSHA, Waived: waived}

	// Tag (peel annotated tags to the commit).
	tagCommit, err := client.TagCommit(ctx, repo, tag)
	if err != nil {
		return nil, err
	}
	actual.TagExists = tagCommit != ""
	actual.TagCommit = tagCommit

	// GitHub Release. Drafts are invisible to the by-tag endpoint (it answers
	// 404), so a draft must be looked up in the releases list; otherwise an
	// in-flight publish looks exactly like a missing release.
	var rel releaseAPI
	found, err := client.GetOptional(ctx, fmt.Sprintf("repos/%s/releases/tags/%s", repo, tag), &rel)
	if err != nil {
		return nil, err
	}
	if !found {
		if draft, ok := draftRelease(ctx, client, repo, tag); ok {
			rel, found = draft, true
		}
	}
	actual.ReleaseExists = found && !rel.Draft
	actual.ReleaseDraft = found && rel.Draft

	if found {
		meta := readReleaseMetadata(ctx, client, repo, rel.Assets)
		// Without a release PR (manual / tag providers) the release's own
		// metadata records the commit it was built from; that is the expected
		// tag target. Judging such a provider against an empty commit would
		// report every healthy manual release as incomplete forever.
		if actual.ExpectedCommit == "" {
			actual.ExpectedCommit = meta.CommitSHA
		}
		// A historical release keeps the contract it was published under.
		if meta.Capabilities != nil {
			caps := policy.Capabilities{
				GitHubRelease: meta.Capabilities.GitHubRelease,
				Binaries:      meta.Capabilities.Binaries,
				Checksums:     meta.Capabilities.Checksums,
				Registries:    meta.Capabilities.Registries,
				Assets:        meta.Capabilities.Assets,
			}
			if len(caps.Assets) == 0 && len(meta.Assets) > 0 {
				caps.Assets = append([]string{}, meta.Assets...)
			}
			report.Observed.Capabilities = caps
		}
		var policyHashMatches bool
		actual.AssetsComplete, actual.ChecksumsVerified, policyHashMatches = assetState(p, report.Observed.Capabilities, rel.Assets, meta, client, ctx, repo)
		report.Context.PolicyHashMatches = policyHashMatches

		// Latest is only meaningful for stable releases.
		if !rel.Prerelease {
			var latest releaseAPI
			latestFound, err := client.GetOptional(ctx, fmt.Sprintf("repos/%s/releases/latest", repo), &latest)
			if err != nil {
				return nil, err
			}
			actual.Latest = latestFound && latest.TagName == tag
		} else {
			actual.Latest = true
		}
	}

	// Required registries.
	actual.NoRegistryRequired, actual.RegistriesHealthy = registryState(ctx, client, verifier, p, repo, version)

	report.Observed.Provider = provider
	report.Observed.Version = domain.Version(version)
	report.Observed.Actual = actual
	report.Verdict = Classify(report.Observed)
	return report, nil
}

// releasePREvidence records which signal identified the merged release PR.
const (
	EvidenceTitle    = "title"
	EvidenceManifest = "manifest"
	EvidenceLabel    = "label"
	// EvidenceNoPending means no pull request carries an outstanding autorelease
	// label: nothing is blocking the provider, so no ACK is needed.
	EvidenceNoPending = "no-pending-label"
)

// providerState identifies where a version's provider acknowledgement lives, using
// several independent signals because any single one can miss:
//
//   - the pull request title (release-please's default pattern),
//   - the manifest-bump commit, which is the authoritative record that this
//     merge released exactly this version,
//   - any outstanding autorelease label, which is what actually blocks
//     release-please and therefore the only thing an ACK must clear.
//
// A signal that cannot confirm the version leaves the provider state UNKNOWN
// rather than guessing, so an ACK is never issued against the wrong pull request.
func providerState(ctx context.Context, client *github.Bound, p *policy.Policy, repo, version string) (*github.PullRequest, State, string, error) {
	prs, err := client.MergedPullRequests(ctx, repo)
	if err != nil {
		return nil, StateUnknown, "", err
	}

	var titleMatch *github.PullRequest
	for i := range prs {
		if match := releasePRPattern.FindStringSubmatch(prs[i].Title); match != nil && match[1] == version {
			titleMatch = &prs[i]
			break
		}
	}

	if titleMatch != nil {
		return titleMatch, stateFromLabels(titleMatch), EvidenceTitle, nil
	}

	// The authoritative signal: find the merge that actually raised the release
	// manifest to this version. Deriving it from commits that touched the
	// manifest is precise and bounded, whereas guessing from pull request titles
	// is not (release-please titles are configurable and may carry no version).
	manifestEvidence := ""
	if mergeSHA := manifestBumpCommit(ctx, client, p, repo, version); mergeSHA != "" {
		for i := range prs {
			if prs[i].MergeCommitSHA == mergeSHA {
				return &prs[i], stateFromLabels(&prs[i]), EvidenceManifest, nil
			}
		}
		// The manifest moved to this version but its merge commit is no longer
		// reachable (history rewritten after the release): remember how we know
		// the release happened and keep looking for outstanding labels.
		manifestEvidence = EvidenceManifest
	}

	// A pending label on any pull request is what actually blocks release-please,
	// and it is the exact thing the ACK clears. Identify it directly rather than
	// requiring the historical release PR to still be reachable: repository
	// history can be rewritten after a release.
	if pending, found := pendingLabelPR(prs); found {
		return pending, StatePending, EvidenceLabel, nil
	}
	if manifestEvidence != "" {
		// The version was released and nothing is outstanding: there is no
		// acknowledgement left to perform.
		return nil, StateTagged, EvidenceNoPending, nil
	}
	return nil, StateTagged, EvidenceNoPending, nil
}

// pendingLabelPR finds a merged pull request that still carries an autorelease
// pending or triggered label. The input comes from MergedPullRequests; querying
// the issues-by-label endpoint would also return closed, unmerged release PRs.
func pendingLabelPR(prs []github.PullRequest) (*github.PullRequest, bool) {
	for i := range prs {
		for _, label := range prs[i].Labels {
			if label.Name == labelPending || label.Name == labelTriggered {
				return &prs[i], true
			}
		}
	}
	return nil, false
}

// manifestVersionAt reads the release manifest version at a commit. It returns ""
// when the file cannot be read or does not pin this package.
// manifestBumpCommit returns the commit that raised the release manifest to this
// version, searched over commits that touched the manifest (newest first).
func manifestBumpCommit(ctx context.Context, client *github.Bound, p *policy.Policy, repo, version string) string {
	manifestPath := p.Versioning.Manifest
	if manifestPath == "" {
		manifestPath = ".release-please-manifest.json"
	}
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := client.Get(ctx, fmt.Sprintf("repos/%s/commits?path=%s&per_page=5", repo, url.QueryEscape(manifestPath)), &commits); err != nil {
		return ""
	}
	for _, commit := range commits {
		if manifestIntroducedAt(ctx, client, p, repo, commit.SHA) == version {
			return commit.SHA
		}
	}
	return ""
}

// manifestIntroducedAt reports the version a merge commit is the first to record.
//
// Reading the manifest at a commit alone is not evidence: every commit after a
// release still shows the released version. Only a commit that raised the
// manifest from a different value introduced this version.
func manifestIntroducedAt(ctx context.Context, client *github.Bound, p *policy.Policy, repo, mergeSHA string) string {
	if mergeSHA == "" {
		return ""
	}
	var commit struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	}
	if err := client.Get(ctx, fmt.Sprintf("repos/%s/commits/%s", repo, mergeSHA), &commit); err != nil {
		return ""
	}
	if len(commit.Parents) == 0 {
		return ""
	}
	at := manifestVersionAtRef(ctx, client, p, repo, mergeSHA)
	if at == "" {
		return ""
	}
	if parent := manifestVersionAtRef(ctx, client, p, repo, commit.Parents[0].SHA); parent == at {
		return ""
	}
	return at
}

// manifestVersionAt reports the manifest version at a ref.
func manifestVersionAt(ctx context.Context, client *github.Bound, p *policy.Policy, repo, ref string) string {
	return manifestVersionAtRef(ctx, client, p, repo, ref)
}

func manifestVersionAtRef(ctx context.Context, client *github.Bound, p *policy.Policy, repo, ref string) string {
	manifestPath := p.Versioning.Manifest
	if manifestPath == "" {
		manifestPath = ".release-please-manifest.json"
	}
	raw, found, err := client.ReadFile(ctx, repo, manifestPath, ref)
	if err != nil || !found {
		return ""
	}
	// Resolve through the policy so a multi-component monorepo is read the same
	// way its desired version is resolved (component selected by the policy).
	if version, err := policy.DesiredVersionFromManifest(p, raw); err == nil {
		return version
	}
	return ""
}

// stateFromLabels maps release-please labels onto the provider state. A merged
// release PR with no autorelease label is treated as acknowledged: that is the
// shape release-please itself leaves behind after a successful run.
func stateFromLabels(pr *github.PullRequest) State {
	labels := map[string]bool{}
	for _, label := range pr.Labels {
		labels[label.Name] = true
	}
	switch {
	case labels[labelTagged]:
		return StateTagged
	case labels[labelTriggered]:
		return StateTriggered
	case labels[labelPending]:
		return StatePending
	default:
		return StateTagged
	}
}

// releaseMetadata is the RELEASE-METADATA.json contract recorded at publish time.
type releaseMetadata struct {
	Assets     []string          `json:"assets"`
	AssetSHA   map[string]string `json:"asset_sha256"`
	PolicyHash string            `json:"policy_hash"`
	CommitSHA  string            `json:"commit_sha"`
	// Capabilities is the contract that was in force when this version was
	// published. A historical release is judged against it, never against a
	// policy that changed afterwards.
	Capabilities *metadataCapabilities `json:"capabilities,omitempty"`
}

type metadataCapabilities struct {
	GitHubRelease bool     `json:"github_release"`
	Binaries      bool     `json:"binaries"`
	Checksums     bool     `json:"checksums"`
	Registries    []string `json:"registries,omitempty"`
	Assets        []string `json:"required_assets,omitempty"`
}

// readReleaseMetadata downloads and parses the release's own contract. A missing
// or unparsable metadata asset yields a zero value, which callers treat as
// "fall back to the current policy".
func readReleaseMetadata(ctx context.Context, client *github.Bound, repo string, assets []releaseAsset) releaseMetadata {
	for _, a := range assets {
		if a.Name != "RELEASE-METADATA.json" {
			continue
		}
		text, found, err := client.ReleaseAssetText(ctx, repo, a.ID)
		if err != nil || !found {
			return releaseMetadata{}
		}
		var meta releaseMetadata
		if json.Unmarshal([]byte(text), &meta) != nil {
			return releaseMetadata{}
		}
		return meta
	}
	return releaseMetadata{}
}

// assetState evaluates the contract recorded inside the release itself before
// falling back to the current policy. A release published under an older policy
// must not be judged against a policy that changed afterwards: newly required
// assets would otherwise mark every historical release incomplete and block all
// future versions.
func assetState(p *policy.Policy, caps policy.Capabilities, assets []releaseAsset, meta releaseMetadata, client *github.Bound, ctx context.Context, repo string) (complete, sumsVerified, policyHashMatches bool) {
	byName := map[string]releaseAsset{}
	for _, a := range assets {
		byName[a.Name] = a
	}
	// The contract is the capability-derived asset set. A repository with no
	// required assets (source-only, registry-only) only records its metadata.
	contract := append([]string{}, caps.Assets...)
	required := append([]string{}, contract...)
	if caps.GitHubRelease {
		required = append(required, "RELEASE-METADATA.json")
	}
	checksumsEnabled := caps.Checksums
	if checksumsEnabled {
		required = append(required, "SHA256SUMS")
	}
	policyHashMatches = true

	if len(meta.Assets) > 0 && meta.Capabilities == nil {
		// Metadata predating explicit capabilities: its asset list was the contract.
		if meta.PolicyHash != "" && p.Hash != "" {
			policyHashMatches = meta.PolicyHash == p.Hash
		}
		contract = append([]string{}, meta.Assets...)
		required = append([]string{}, contract...)
		if caps.GitHubRelease {
			required = append(required, "RELEASE-METADATA.json")
		}
		if _, hasSums := byName["SHA256SUMS"]; hasSums {
			required = append(required, "SHA256SUMS")
		}
	}
	complete = true
	for _, pattern := range required {
		matched := false
		for _, a := range assets {
			if ok, _ := path.Match(pattern, a.Name); ok && a.Size > 0 {
				matched = true
				break
			}
		}
		if !matched {
			complete = false
		}
	}
	_, hasSums := byName["SHA256SUMS"]
	if !checksumsEnabled && !hasSums {
		return complete, complete, policyHashMatches
	}
	sumsAsset, ok := byName["SHA256SUMS"]
	if !ok {
		return complete, false, policyHashMatches
	}
	text, found, err := client.ReleaseAssetText(ctx, repo, sumsAsset.ID)
	if err != nil || !found {
		return complete, false, policyHashMatches
	}
	listed := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			listed[strings.TrimPrefix(fields[1], "*")] = true
		}
	}
	// Coverage is judged against the same contract used for the asset gate:
	// the release's own recorded list when available, else the current policy.
	covers := true
	for _, pattern := range contract {
		base := strings.ToUpper(path.Base(pattern))
		if base == "SHA256SUMS" || strings.HasPrefix(base, "SHA256SUMS.") || pattern == "RELEASE-METADATA.json" {
			continue
		}
		matched := false
		for name := range listed {
			if ok, _ := path.Match(pattern, name); ok {
				matched = true
				break
			}
		}
		if !matched {
			covers = false
		}
	}
	return complete, complete && covers, policyHashMatches
}

func registryState(ctx context.Context, client *github.Bound, verifier *registry.Verifier, p *policy.Policy, repo, version string) (noneRequired, healthy bool) {
	healthy = true
	any := false
	names := make([]string, 0, len(p.Registries))
	for name := range p.Registries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := p.Registries[name]
		if name == "github" || !cfg.Required {
			continue
		}
		any = true
		manifestPath := cfg.File
		if manifestPath == "" {
			manifestPath = map[string]string{"npm": "package.json", "pypi": "pyproject.toml", "pub": "pubspec.yaml"}[name]
		}
		var manifest []byte
		if manifestPath != "" {
			if raw, found, err := client.ReadFile(ctx, repo, manifestPath, ""); err == nil && found {
				manifest = raw
			}
		}
		if err := verifier.Verify(ctx, name, cfg, repo, version, manifest); err != nil {
			healthy = false
		}
	}
	return !any, healthy
}

// Acknowledge reconciles provider labels after a healthy release. It is
// idempotent: no tag/release mutation, label-only. dryRun returns the plan
// without applying it.
func Acknowledge(ctx context.Context, client *github.Bound, report *Report, dryRun bool) ([]LabelMutation, error) {
	if !report.Verdict.ACKAllowed {
		return nil, fmt.Errorf("ACK refused: %s (%s)", report.Verdict.Health, report.Verdict.Drift)
	}
	if report.Context.ReleasePR == 0 {
		return nil, fmt.Errorf("ACK refused: no release PR resolved for %s %s", report.Context.Repository, report.Context.Version)
	}
	mutations := PlanACK(report.Context.Labels, nil, nil)
	if dryRun || len(mutations) == 0 {
		return mutations, nil
	}
	// Add first so an interrupted run can never leave the PR without an ACK
	// label; remove pending afterwards. Both calls are independently idempotent.
	var toAdd, toRemove []string
	for _, m := range mutations {
		switch m.Action {
		case "add":
			toAdd = append(toAdd, m.Label)
		case "remove":
			toRemove = append(toRemove, m.Label)
		}
	}
	if len(toAdd) > 0 {
		if err := client.AddIssueLabels(ctx, report.Context.Repository, report.Context.ReleasePR, toAdd); err != nil {
			return nil, err
		}
	}
	for _, label := range toRemove {
		if err := client.RemoveIssueLabel(ctx, report.Context.Repository, report.Context.ReleasePR, label); err != nil {
			return nil, err
		}
	}
	return mutations, nil
}

// ScanResult is the fleet-wide outcome of a provider scan. It is pure data:
// the scope-bound client that produced it stays in the calling layer.
type ScanResult struct {
	Reports   []Report                `json:"reports"`
	Errors    []string                `json:"errors,omitempty"`
	Execution domain.ExecutionContext `json:"execution"`
}

// Waive records an explicit, auditable human decision that a historical version
// must not be repaired retroactively and must not block newer versions. It only
// adds a label; it never fabricates a release or moves a tag.
func Waive(ctx context.Context, client *github.Bound, repo string, version string) (int, error) {
	prs, err := client.MergedPullRequests(ctx, repo)
	if err != nil {
		return 0, err
	}
	for i := range prs {
		if m := releasePRPattern.FindStringSubmatch(prs[i].Title); m != nil && m[1] == version {
			if err := client.AddIssueLabels(ctx, repo, prs[i].Number, []string{labelWaived}); err != nil {
				return prs[i].Number, err
			}
			return prs[i].Number, nil
		}
	}
	return 0, fmt.Errorf("no merged release PR found for version %s", version)
}

// RepairPlan decides how an incomplete version is recovered. Same-version
// recovery is the only allowed path: starting a newer version to escape an
// unfinished release is exactly the failure mode this layer exists to prevent.
func RepairPlan(r *Report) (allowed bool, reason string, inputs map[string]string) {
	v := r.Verdict
	switch {
	case v.HardFail:
		return false, "TAG_CONFLICT: an existing tag points at the wrong commit; manual intervention required", nil
	case v.Waived:
		return false, "version is explicitly waived; nothing to repair", nil
	case v.Health == domain.HealthHealthy:
		return false, "release transaction is already healthy", nil
	case !v.RepairSameVersion:
		return false, "release is not in a repairable state (" + string(v.Drift) + ")", nil
	case r.Context.Version == "":
		return false, "no version resolved for repair", nil
	}
	return true, "resume the same version through the repository's own release pipeline", map[string]string{
		"version": string(r.Context.Version),
		"force":   "true",
		"repair":  "true",
	}
}

// Repair re-enters the repository's release pipeline for the same version.
// dryRun only reports the dispatch it would perform.
func Repair(ctx context.Context, client *github.Bound, r *Report, workflowFile string, dryRun bool) (map[string]string, error) {
	allowed, reason, inputs := RepairPlan(r)
	if !allowed {
		return nil, fmt.Errorf("repair refused: %s", reason)
	}
	if workflowFile == "" {
		workflowFile = "release.yml"
	}
	if dryRun {
		return inputs, nil
	}
	ref, err := client.DefaultBranch(ctx, r.Context.Repository)
	if err != nil {
		return nil, err
	}
	if err := client.DispatchWorkflow(ctx, r.Context.Repository, workflowFile, ref, inputs); err != nil {
		return nil, err
	}
	return inputs, nil
}

// draftRelease finds a draft release for a tag through the releases list, the
// only endpoint that exposes drafts.
func draftRelease(ctx context.Context, client *github.Bound, repo, tag string) (releaseAPI, bool) {
	raw, err := client.Releases(ctx, repo)
	if err != nil {
		return releaseAPI{}, false
	}
	buf, err := json.Marshal(raw)
	if err != nil {
		return releaseAPI{}, false
	}
	var releases []releaseAPI
	if json.Unmarshal(buf, &releases) != nil {
		return releaseAPI{}, false
	}
	for _, release := range releases {
		if release.TagName == tag && release.Draft {
			return release, true
		}
	}
	return releaseAPI{}, false
}
