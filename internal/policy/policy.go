package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/redtidev1918/releasegraph/internal/config"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/glob"
)

type Versioning struct {
	Mode     string `json:"mode" yaml:"mode"`
	Provider string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Package  string `json:"package,omitempty" yaml:"package,omitempty"`
	Manifest string `json:"manifest,omitempty" yaml:"manifest,omitempty"`
	Version  string `json:"version,omitempty" yaml:"version,omitempty"`
}

type Tag struct {
	Template string `json:"template,omitempty" yaml:"template,omitempty"`
}

type BuildMatrixItem struct {
	Runner       string `json:"runner" yaml:"runner"`
	Command      string `json:"command" yaml:"command"`
	VersionCheck string `json:"version_check,omitempty" yaml:"versionCheck,omitempty"`
}

type Build struct {
	Test           string            `json:"test,omitempty" yaml:"test,omitempty"`
	Command        string            `json:"command,omitempty" yaml:"command,omitempty"`
	VersionCheck   string            `json:"version_check,omitempty" yaml:"versionCheck,omitempty"`
	FlutterVersion string            `json:"flutter_version,omitempty" yaml:"flutterVersion,omitempty"`
	Matrix         []BuildMatrixItem `json:"matrix,omitempty" yaml:"matrix,omitempty"`
}

type Assets struct {
	Required []string `json:"required" yaml:"required"`
	Optional []string `json:"optional" yaml:"optional"`
}

type Registry struct {
	Required bool   `json:"required" yaml:"required"`
	Publish  string `json:"publish,omitempty" yaml:"publish,omitempty"`
	Verify   string `json:"verify,omitempty" yaml:"verify,omitempty"`
	Context  string `json:"context,omitempty" yaml:"context,omitempty"`
	File     string `json:"file,omitempty" yaml:"file,omitempty"`
	Image    string `json:"image,omitempty" yaml:"image,omitempty"`
}

type Retention struct {
	Stable      int `json:"stable,omitempty" yaml:"stable,omitempty"`
	Prerelease  int `json:"prerelease,omitempty" yaml:"prerelease,omitempty"`
	FailedDraft int `json:"failed_draft,omitempty" yaml:"failed_draft,omitempty"`
	// PruneStable opts in to deleting published stable releases beyond Stable.
	// Off by default: a published stable release is history, not cache.
	PruneStable bool `json:"pruneStable,omitempty" yaml:"pruneStable,omitempty"`
}

// ProductionOperations declares the production-operation branch contract:
// branches that perform a production state transition (cutovers, releases,
// hotfixes, ops changes) must target the production base directly and must
// descend from its current HEAD.
type ProductionOperations struct {
	// Base is the production base ref: a branch name ("main", "master") or
	// the literal "default" to resolve the repository default branch.
	Base string `json:"base" yaml:"base"`
	// Branches are the glob patterns naming production-operation branches.
	Branches []string `json:"branches" yaml:"branches"`
	// RequireLatestBase is nil or true (default): the branch must descend
	// from the current HEAD of the production base. The contract deliberately
	// provides no opt-out; Validate rejects an explicit false.
	RequireLatestBase *bool `json:"requireLatestBase,omitempty" yaml:"requireLatestBase,omitempty"`
	// Operations optionally narrows the change scope per operation name.
	Operations map[string]OperationScope `json:"operations,omitempty" yaml:"operations,omitempty"`
}

// RequireLatest reports whether the latest-base invariant is enforced. It is
// true unless policy validation would have rejected the policy.
func (po *ProductionOperations) RequireLatest() bool {
	return po == nil || po.RequireLatestBase == nil || *po.RequireLatestBase
}

type OperationScope struct {
	// Branches are the glob patterns this scope applies to.
	Branches []string `json:"branches" yaml:"branches"`
	// AllowedPaths are optional change-scope glob patterns ("**" crosses
	// directories). Absent means the scope is not enforced.
	AllowedPaths []string `json:"allowedPaths,omitempty" yaml:"allowedPaths,omitempty"`
}

type Repository struct {
	Git          GitPolicy    `json:"git,omitempty" yaml:"git,omitempty"`
	PullRequests PullRequests `json:"pullRequests,omitempty" yaml:"pullRequests,omitempty"`
}

type GitPolicy struct {
	ProductionOperations *ProductionOperations `json:"productionOperations,omitempty" yaml:"productionOperations,omitempty"`
}

// PullRequests declares the pull-request lifecycle contract: an open pull
// request is a merge candidate, not a work tracker.
type PullRequests struct {
	Lifecycle *PRLifecycle `json:"lifecycle,omitempty" yaml:"lifecycle,omitempty"`
}

// PRLifecycle declares when an open pull request stops being a merge candidate.
//
// The contract answers one question per open pull request ("is this still part
// of the merge queue?") and separates two very different outcomes: work that
// has already landed (OBSOLETE) and work that is paused (PARKED). Paused work
// is archived into an issue before it leaves the queue, so closing a pull
// request never discards engineering context.
type PRLifecycle struct {
	// Enabled is nil or true (default): the contract is enforced and classified
	// pull requests may be mutated. An explicit false declares the contract
	// without enforcing it, which keeps a repository's audit output while
	// leaving its pull requests untouched.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// ParkedAfterDays is the inactivity window after which a pull request that
	// is still a draft or conflicting is classified PARKED. nil means
	// DefaultParkedAfterDays.
	ParkedAfterDays *int `json:"parkedAfterDays,omitempty" yaml:"parkedAfterDays,omitempty"`
	// ArchiveParkedToIssue is nil or true (default): parked work is archived
	// into an issue before the pull request is closed. Validate rejects false
	// while parked pull requests are closed, because closing without an issue
	// is how context gets lost.
	ArchiveParkedToIssue *bool `json:"archiveParkedToIssue,omitempty" yaml:"archiveParkedToIssue,omitempty"`
	// CloseParked is nil or true (default): parked pull requests leave the
	// merge queue.
	CloseParked *bool `json:"closeParked,omitempty" yaml:"closeParked,omitempty"`
	// DeleteBranch must be false. The contract never deletes a branch: a
	// closed pull request is a pointer to work that may be resumed, and
	// Validate rejects an explicit true rather than accepting an opt-in.
	DeleteBranch *bool `json:"deleteBranch,omitempty" yaml:"deleteBranch,omitempty"`
	// Exempt declares what the contract never classifies as parked or obsolete.
	Exempt PRExemptions `json:"exempt,omitempty" yaml:"exempt,omitempty"`
}

// PRExemptions are the pull requests the lifecycle contract never acts on.
type PRExemptions struct {
	// Branches are glob patterns of head branches that are managed elsewhere
	// (for example release-please's branches).
	Branches []string `json:"branches,omitempty" yaml:"branches,omitempty"`
	// Actors are the authors whose pull requests are managed elsewhere (bots).
	Actors []string `json:"actors,omitempty" yaml:"actors,omitempty"`
	// Labels are the labels that retain a pull request on purpose. "keep-open"
	// is the human escape hatch: a labelled pull request is never touched.
	Labels []string `json:"labels,omitempty" yaml:"labels,omitempty"`
}

// DefaultParkedAfterDays is the inactivity window used when the policy does not
// declare one.
const DefaultParkedAfterDays = 7

// IsEnabled reports whether the contract may act on pull requests. A declared
// contract is enforced unless the policy explicitly disables it.
func (l *PRLifecycle) IsEnabled() bool {
	return l != nil && (l.Enabled == nil || *l.Enabled)
}

// ParkedAfter is the inactivity window in days.
func (l *PRLifecycle) ParkedAfter() int {
	if l == nil || l.ParkedAfterDays == nil {
		return DefaultParkedAfterDays
	}
	return *l.ParkedAfterDays
}

// ArchivesToIssue reports whether parked work is archived before it is closed.
func (l *PRLifecycle) ArchivesToIssue() bool {
	return l == nil || l.ArchiveParkedToIssue == nil || *l.ArchiveParkedToIssue
}

// ClosesParked reports whether parked pull requests leave the merge queue.
func (l *PRLifecycle) ClosesParked() bool {
	return l == nil || l.CloseParked == nil || *l.CloseParked
}

type Release struct {
	Prerelease  bool   `json:"prerelease,omitempty" yaml:"prerelease,omitempty"`
	PostPublish string `json:"post_publish,omitempty" yaml:"post_publish,omitempty"`
	// PostRelease declares resumable, idempotent actions that run after this
	// release is published. Executed by release_infra/actions.py.
	PostRelease []PostReleaseAction `json:"postRelease,omitempty" yaml:"postRelease,omitempty"`
	// GitHub declares whether a GitHub Release is part of this repository's
	// contract. nil means "not declared" and falls back to the historical
	// default (true), so existing policies keep their meaning.
	GitHub *bool `json:"github,omitempty" yaml:"github,omitempty"`
	Notes  Notes `json:"notes,omitempty" yaml:"notes,omitempty"`
}

// Notes configures the user-facing GitHub Release body.
type Notes struct {
	// Language is "auto" (follow the repository's primary README), "en" or "zh".
	Language string `json:"language,omitempty" yaml:"language,omitempty"`
}

// PostReleaseAction is one step of the post-release transaction. Exactly the
// fields the python executor consumes; unknown fields are rejected.
type PostReleaseAction struct {
	ID       string            `json:"id" yaml:"id"`
	Type     string            `json:"type" yaml:"type"`
	Required bool              `json:"required,omitempty" yaml:"required,omitempty"`
	Workflow string            `json:"workflow,omitempty" yaml:"workflow,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty" yaml:"inputs,omitempty"`
}

// Artifacts describes distributable build outputs. Repositories without
// distributable artifacts simply do not enable them.
type Artifacts struct {
	Binaries Binaries `json:"binaries,omitempty" yaml:"binaries,omitempty"`
}

// Binaries covers native/CLI artifacts. It is optional: a Python library, an npm
// package, a container service or a source-only repository has none.
type Binaries struct {
	Enabled *bool    `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Targets []string `json:"targets,omitempty" yaml:"targets,omitempty"`
}

type Policy struct {
	APIVersion string              `json:"apiVersion,omitempty" yaml:"apiVersion,omitempty"`
	Kind       string              `json:"kind" yaml:"kind"`
	Versioning Versioning          `json:"versioning" yaml:"versioning"`
	Tag        Tag                 `json:"tag,omitempty" yaml:"tag,omitempty"`
	Build      Build               `json:"build,omitempty" yaml:"build,omitempty"`
	Assets     Assets              `json:"assets" yaml:"assets"`
	Registries map[string]Registry `json:"registries,omitempty" yaml:"registries,omitempty"`
	Retention  Retention           `json:"retention,omitempty" yaml:"retention,omitempty"`
	Release    Release             `json:"release,omitempty" yaml:"release,omitempty"`
	Artifacts  Artifacts           `json:"artifacts,omitempty" yaml:"artifacts,omitempty"`
	Checksums  bool                `json:"checksums" yaml:"checksums"`
	Metadata   bool                `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	SBOM       bool                `json:"sbom,omitempty" yaml:"sbom,omitempty"`
	Repository Repository          `json:"repository,omitempty" yaml:"repository,omitempty"`
	Hash       string              `json:"hash,omitempty" yaml:"-"`
}

var versionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9A-Za-z-]+)+$`)

var kinds = map[string]bool{"binary": true, "python-library": true, "node-library": true, "container": true, "flutter": true, "android": true, "hybrid": true, "none": true}
var versionModes = map[string]bool{"release-please": true, "manual": true, "tag": true}
var registryNames = map[string]bool{"github": true, "pypi": true, "npm": true, "pub": true, "ghcr": true}

// notesLanguages mirrors release_infra/notes.LANGUAGES. Both sides reject the
// same values, so a policy that loads here renders the same way there.
var notesLanguages = map[string]bool{"auto": true, "en": true, "zh": true}

func Load(path string) (*Policy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, rgerrors.Wrap(rgerrors.Policy, "read policy", err)
	}
	return Parse(raw)
}

func Parse(raw []byte) (*Policy, error) {
	var p Policy
	if err := config.Unmarshal(raw, &p); err != nil {
		return nil, rgerrors.Wrap(rgerrors.Policy, "parse policy", err)
	}
	if p.APIVersion == "" {
		p.APIVersion = "releasegraph.dev/v1"
	}
	var rawMap map[string]any
	if err := yaml.Unmarshal(raw, &rawMap); err != nil {
		return nil, rgerrors.Wrap(rgerrors.Policy, "parse policy", err)
	}
	if _, ok := rawMap["checksums"]; !ok {
		p.Checksums = true
	}
	if p.Registries == nil {
		p.Registries = map[string]Registry{}
	}
	if err := Validate(&p); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	p.Hash = hex.EncodeToString(sum[:])
	return &p, nil
}

func Validate(p *Policy) error {
	if p.APIVersion != "" && p.APIVersion != "releasegraph.dev/v1" {
		return rgerrors.New(rgerrors.Policy, "apiVersion must be releasegraph.dev/v1")
	}
	if !kinds[p.Kind] {
		return rgerrors.New(rgerrors.Policy, "kind must be binary, python-library, node-library, container, flutter, android, hybrid, or none")
	}
	mode := p.Versioning.Mode
	if mode == "" {
		mode = p.Versioning.Provider
	}
	if !versionModes[mode] {
		return rgerrors.New(rgerrors.Policy, "versioning.mode must be release-please, manual, or tag")
	}
	if p.Tag.Template != "" && !strings.Contains(p.Tag.Template, "{version}") {
		return rgerrors.New(rgerrors.Policy, "tag.template must contain {version}")
	}
	for _, key := range []string{"required", "optional"} {
		values := map[string][]string{"required": p.Assets.Required, "optional": p.Assets.Optional}[key]
		for _, value := range values {
			if value == "" {
				return rgerrors.New(rgerrors.Policy, "assets."+key+" must contain non-empty strings")
			}
			// The pattern language is part of the policy grammar, so it is
			// checked where every other policy rule is. See pattern.go for why
			// it is restricted rather than the matcher being ported.
			if err := ValidatePattern(value); err != nil {
				return err
			}
		}
	}
	names := make([]string, 0, len(p.Registries))
	for name := range p.Registries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !registryNames[name] {
			return rgerrors.New(rgerrors.Policy, "unknown registry: "+name)
		}
		config := p.Registries[name]
		if containsNewline(config.Publish) || containsNewline(config.Verify) {
			return rgerrors.New(rgerrors.Policy, "registry publish and verify must be single-line commands")
		}
	}
	for _, item := range p.Build.Matrix {
		if item.Runner == "" || item.Command == "" {
			return rgerrors.New(rgerrors.Policy, "each build matrix item needs runner and command")
		}
		if containsNewline(item.Command) {
			return rgerrors.New(rgerrors.Policy, "build matrix commands must be single-line strings")
		}
	}
	if containsNewline(p.Release.PostPublish) {
		return rgerrors.New(rgerrors.Policy, "release.post_publish must be a single-line command")
	}
	seenPostRelease := map[string]bool{}
	for _, action := range p.Release.PostRelease {
		if action.ID == "" {
			return rgerrors.New(rgerrors.Policy, "release.postRelease action needs a non-empty id")
		}
		if seenPostRelease[action.ID] {
			return rgerrors.New(rgerrors.Policy, "release.postRelease action id duplicated: "+action.ID)
		}
		seenPostRelease[action.ID] = true
		switch action.Type {
		case "github-workflow":
			if action.Workflow == "" {
				return rgerrors.New(rgerrors.Policy, "release.postRelease action "+action.ID+": github-workflow needs a workflow")
			}
		case "":
			return rgerrors.New(rgerrors.Policy, "release.postRelease action "+action.ID+" needs a type")
		default:
			return rgerrors.New(rgerrors.Policy, "release.postRelease action "+action.ID+": unknown type "+action.Type)
		}
		for key, value := range action.Inputs {
			if containsNewline(value) {
				return rgerrors.New(rgerrors.Policy, "release.postRelease action "+action.ID+" input "+key+" must be single-line")
			}
		}
	}
	if p.Release.Notes.Language != "" && !notesLanguages[p.Release.Notes.Language] {
		return rgerrors.New(rgerrors.Policy, "release.notes.language must be auto, en, or zh")
	}
	if p.Retention.Stable < 0 || p.Retention.Prerelease < 0 || p.Retention.FailedDraft < 0 {
		return rgerrors.New(rgerrors.Policy, "retention values must not be negative")
	}
	if po := p.Repository.Git.ProductionOperations; po != nil {
		if err := validateProductionOperations(po); err != nil {
			return err
		}
	}
	if lifecycle := p.Repository.PullRequests.Lifecycle; lifecycle != nil {
		if err := validatePRLifecycle(lifecycle); err != nil {
			return err
		}
	}
	return nil
}

// validatePRLifecycle enforces the lifecycle contract's own invariants. Two of
// them are deliberately not opt-in flags: the contract never deletes a branch,
// and it never closes parked work without first archiving it into an issue.
func validatePRLifecycle(lifecycle *PRLifecycle) error {
	const field = "repository.pullRequests.lifecycle"
	if lifecycle.DeleteBranch != nil && *lifecycle.DeleteBranch {
		return rgerrors.New(rgerrors.Policy, field+".deleteBranch must be false; the lifecycle contract never deletes a branch")
	}
	if lifecycle.ParkedAfterDays != nil && (*lifecycle.ParkedAfterDays < 1 || *lifecycle.ParkedAfterDays > 365) {
		return rgerrors.New(rgerrors.Policy, field+".parkedAfterDays must be between 1 and 365")
	}
	if lifecycle.ClosesParked() && !lifecycle.ArchivesToIssue() {
		return rgerrors.New(rgerrors.Policy, field+".archiveParkedToIssue must be true while parked pull requests are closed; closing without an issue discards the work")
	}
	if err := validateGlobs(field+".exempt.branches", lifecycle.Exempt.Branches); err != nil {
		return err
	}
	if err := validateValues(field+".exempt.actors", lifecycle.Exempt.Actors); err != nil {
		return err
	}
	return validateValues(field+".exempt.labels", lifecycle.Exempt.Labels)
}

// validateValues checks a list of literal (non-glob) policy values.
func validateValues(field string, values []string) error {
	for _, value := range values {
		if value == "" {
			return rgerrors.New(rgerrors.Policy, field+" must contain non-empty strings")
		}
		if containsNewline(value) {
			return rgerrors.New(rgerrors.Policy, field+" must contain single-line strings")
		}
	}
	return nil
}

func validateProductionOperations(po *ProductionOperations) error {
	if po.Base == "" {
		return rgerrors.New(rgerrors.Policy, `repository.git.productionOperations.base must be a non-empty ref (branch name or "default")`)
	}
	if len(po.Branches) == 0 {
		return rgerrors.New(rgerrors.Policy, "repository.git.productionOperations.branches must declare at least one glob pattern")
	}
	if po.RequireLatestBase != nil && !*po.RequireLatestBase {
		return rgerrors.New(rgerrors.Policy, "repository.git.productionOperations.requireLatestBase must be true; the production-operation contract has no opt-out")
	}
	if err := validateGlobs("repository.git.productionOperations.branches", po.Branches); err != nil {
		return err
	}
	names := make([]string, 0, len(po.Operations))
	for name := range po.Operations {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "" {
			return rgerrors.New(rgerrors.Policy, "repository.git.productionOperations.operations names must be non-empty")
		}
		scope := po.Operations[name]
		field := fmt.Sprintf("repository.git.productionOperations.operations.%s.branches", name)
		if len(scope.Branches) == 0 {
			return rgerrors.New(rgerrors.Policy, field+" must declare at least one glob pattern")
		}
		if err := validateGlobs(field, scope.Branches); err != nil {
			return err
		}
		if err := validateGlobs(fmt.Sprintf("repository.git.productionOperations.operations.%s.allowedPaths", name), scope.AllowedPaths); err != nil {
			return err
		}
	}
	return nil
}

func validateGlobs(field string, patterns []string) error {
	for _, pattern := range patterns {
		if pattern == "" {
			return rgerrors.New(rgerrors.Policy, field+" must contain non-empty strings")
		}
		if err := glob.Check(pattern); err != nil {
			return rgerrors.New(rgerrors.Policy, fmt.Sprintf("%s has invalid glob pattern %q: %v", field, pattern, err))
		}
	}
	return nil
}

func DesiredVersion(p *Policy, explicit, root string) (string, error) {
	if explicit != "" {
		version := trimPrefix(explicit, "v")
		if !versionPattern.MatchString(version) {
			return "", rgerrors.New(rgerrors.Policy, "invalid release version: "+explicit)
		}
		return version, nil
	}
	mode := p.Versioning.Mode
	if mode == "" {
		mode = p.Versioning.Provider
	}
	if mode == "manual" || mode == "tag" {
		if p.Versioning.Version == "" {
			return "", rgerrors.New(rgerrors.Policy, "manual versioning requires versioning.version or --version")
		}
		return DesiredVersion(&Policy{Versioning: Versioning{Mode: "manual", Version: p.Versioning.Version}}, p.Versioning.Version, root)
	}
	manifestPath := filepath.Join(root, defaultValue(p.Versioning.Manifest, ".release-please-manifest.json"))
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", rgerrors.Wrap(rgerrors.Policy, "read release manifest", err)
	}
	return DesiredVersionFromManifest(p, raw)
}

func DesiredVersionFromManifest(p *Policy, raw []byte) (string, error) {
	var manifest map[string]string
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", rgerrors.Wrap(rgerrors.Policy, "parse release manifest", err)
	}
	packageName := defaultValue(p.Versioning.Package, ".")
	version, ok := manifest[packageName]
	if !ok {
		return "", rgerrors.New(rgerrors.Policy, fmt.Sprintf("manifest has no package %q", packageName))
	}
	return DesiredVersion(&Policy{Versioning: Versioning{Mode: "manual", Version: version}}, version, ".")
}

func ReleaseTag(p *Policy, version string) string {
	return strings.ReplaceAll(defaultValue(p.Tag.Template, "v{version}"), "{version}", version)
}

func defaultValue(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
func trimPrefix(value, prefix string) string {
	if len(value) >= len(prefix) && value[:len(prefix)] == prefix {
		return value[len(prefix):]
	}
	return value
}
func containsNewline(value string) bool {
	for i := range value {
		if value[i] == '\n' {
			return true
		}
	}
	return false
}

// Capabilities is the release contract of one repository, derived from its
// policy. ReleaseGraph is capability-driven: nothing here may be assumed for
// every repository.
type Capabilities struct {
	// GitHubRelease reports whether a public GitHub Release is part of the contract.
	GitHubRelease bool `json:"githubRelease"`
	// Binaries reports whether native artifacts are built and uploaded.
	Binaries bool `json:"binaries"`
	// Checksums reports whether SHA256SUMS must cover the required assets.
	Checksums bool `json:"checksums"`
	// Assets are the required asset patterns (may be empty: source-only repos).
	Assets []string `json:"assets,omitempty"`
	// Registries are the registry names that must be published.
	Registries []string `json:"registries,omitempty"`
}

// CapabilitiesOf derives the effective contract.
//
// Defaults preserve existing policies: a policy that builds a matrix or
// declares required assets has binaries; one that declares neither does not.
// Absence of a capability is a contract choice, never a release failure.
func (p *Policy) CapabilitiesOf() Capabilities {
	caps := Capabilities{GitHubRelease: true, Assets: append([]string{}, p.Assets.Required...)}

	if p.Release.GitHub != nil {
		caps.GitHubRelease = *p.Release.GitHub
	}
	switch {
	case p.Artifacts.Binaries.Enabled != nil:
		caps.Binaries = *p.Artifacts.Binaries.Enabled
	default:
		caps.Binaries = len(p.Build.Matrix) > 0 || len(p.Assets.Required) > 0
	}
	caps.Checksums = p.Checksums && len(caps.Assets) > 0

	names := make([]string, 0, len(p.Registries))
	for name, config := range p.Registries {
		if name == "github" || !config.Required {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	caps.Registries = names
	return caps
}

// Names renders the capability set as stable identifiers used by ReleaseGraph
// compatibility metadata ("binary", "github-release", "checksums", registry
// names). Rollout compares these with a ReleaseGraph release's
// affected_capabilities to decide whether a repository is affected at all.
func (c Capabilities) Names() []string {
	names := []string{}
	if c.GitHubRelease {
		names = append(names, "github-release")
	}
	if c.Binaries {
		names = append(names, "binary")
	}
	if c.Checksums {
		names = append(names, "checksums")
	}
	for _, registry := range c.Registries {
		names = append(names, registry)
	}
	sort.Strings(names)
	return names
}
