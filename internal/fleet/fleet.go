package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	rgdomain "github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/health"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

type Repository struct {
	Name           string          `json:"name"`
	Visibility     string          `json:"visibility"`
	DefaultBranch  string          `json:"defaultBranch"`
	Fork           bool            `json:"fork"`
	Archived       bool            `json:"archived"`
	Managed        bool            `json:"managed"`
	Classification string          `json:"classification"`
	Health         rgdomain.Health `json:"health"`
	// Why this health value was chosen. Empty for HEALTHY, and non-empty for
	// everything else, so a reader never has to guess.
	HealthReasons []health.Reason `json:"healthReasons,omitempty"`
	// Required patterns no released asset satisfies, and patterns satisfied only
	// by zero-byte assets. Kept apart because the fixes differ.
	MissingAssets []string `json:"missingAssets,omitempty"`
	EmptyAssets   []string `json:"emptyAssets,omitempty"`
	LatestRelease string   `json:"latestRelease,omitempty"`
	DraftCount    int      `json:"draftCount"`
	AssetCount    int      `json:"assetCount"`
	Signals       []string `json:"signals"`
}

type Fleet struct {
	Repositories []Repository `json:"repositories"`
}

func Discover(ctx context.Context, client *github.Bound, owner string, publicOnly bool) (*Fleet, error) {
	// GitHub rejects combining affiliation with type (422); affiliation=owner
	// already spans every repo type and visibility.
	path := "user/repos?affiliation=owner"
	if publicOnly {
		path = fmt.Sprintf("users/%s/repos?type=all", url.PathEscape(owner))
	}
	raw, err := client.Paginate(ctx, path)
	if err != nil {
		return nil, err
	}
	out := &Fleet{Repositories: []Repository{}}
	for _, source := range raw {
		fullName, _ := source["full_name"].(string)
		repoOwner, _, _ := strings.Cut(fullName, "/")
		if !strings.EqualFold(repoOwner, owner) {
			continue
		}
		repo := Repository{
			Name:           fullName,
			Visibility:     stringValue(source, "visibility", "private"),
			DefaultBranch:  stringValue(source, "default_branch", ""),
			Fork:           boolValue(source, "fork"),
			Archived:       boolValue(source, "archived"),
			Signals:        []string{},
			Classification: "observe-only",
			Health:         rgdomain.HealthUnmanaged,
		}
		if publicOnly && repo.Visibility != "public" {
			continue
		}
		// Every classification goes through the one decision function, so every
		// repository carries a health value AND the reason for it. Setting the
		// value here by hand is how UNMANAGED ended up with no reason at all.
		switch {
		case repo.Archived:
			repo.Classification = "archived"
			applyAssessment(&repo, health.Assess(nil, health.Observation{Archived: true}))
		case repo.Fork:
			repo.Classification = "fork"
			applyAssessment(&repo, health.Assess(nil, health.Observation{Fork: true}))
		default:
			raw, found, err := client.ReadFile(ctx, fullName, ".release-policy.yml", repo.DefaultBranch)
			if err != nil {
				return nil, err
			}
			if found && len(raw) > 0 {
				repo.Managed = true
				repo.Classification = "managed"
				repo.Signals = append(repo.Signals, ".release-policy.yml")
				if err := assessManaged(ctx, client, &repo, raw); err != nil {
					return nil, err
				}
				break
			}
			applyAssessment(&repo, health.Assess(nil, health.Observation{Unmanaged: true}))
		}
		sort.Strings(repo.Signals)
		out.Repositories = append(out.Repositories, repo)
	}
	sort.Slice(out.Repositories, func(i, j int) bool {
		return strings.ToLower(out.Repositories[i].Name) < strings.ToLower(out.Repositories[j].Name)
	})
	return out, nil
}

// applyAssessment copies a decision onto a repository. One assignment site means
// a new field on the assessment cannot be forgotten in one branch.
func applyAssessment(repo *Repository, a health.Assessment) {
	repo.Health = a.Health
	repo.HealthReasons = a.Reasons
	repo.MissingAssets = a.MissingAssets
	repo.EmptyAssets = a.EmptyAssets
}

// assessManaged computes a managed repository's health from real provider state
// through the shared contract, so `releasegraph fleet` and the Python inventory
// answer the same question with the same words.
//
// It replaces a function that fetched the release, kept only the asset *count*,
// discarded the asset names, and left the repository at NEEDS_REVIEW forever --
// so Go could not say HEALTHY or DEGRADED about anything.
func assessManaged(ctx context.Context, client *github.Bound, repo *Repository, raw []byte) error {
	parsed, parseErr := policy.Parse(raw)
	obs := health.Observation{
		HasPolicy:      true,
		PolicyParsable: parseErr == nil,
		Assets:         []health.Asset{},
	}

	releases, err := client.Releases(ctx, repo.Name)
	if err != nil {
		return err
	}
	repo.DraftCount = 0
	firstDraft := ""
	for _, release := range releases {
		if release["draft"] == true {
			repo.DraftCount++
			if firstDraft == "" {
				firstDraft, _ = release["tag_name"].(string)
			}
		}
	}
	// The latest published release: not a draft, not a prerelease.
	for _, release := range releases {
		if release["draft"] == true || release["prerelease"] == true {
			continue
		}
		repo.LatestRelease, _ = release["tag_name"].(string)
		if assets, ok := release["assets"].([]any); ok {
			repo.AssetCount = len(assets)
			for _, entry := range assets {
				asset, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				name, _ := asset["name"].(string)
				size, _ := asset["size"].(float64)
				obs.Assets = append(obs.Assets, health.Asset{Name: name, Size: int64(size)})
			}
		}
		break
	}
	obs.Release = repo.LatestRelease
	obs.DraftRelease = firstDraft

	if parsed != nil {
		// Version drift is judged on the tag *name* against the desired version,
		// exactly as the Python inventory does. Commit-level drift is a different
		// question, owned by the provider verdict (see internal/domain/health.go).
		if desired, err := desiredVersion(ctx, client, repo, parsed); err == nil && desired != "" && obs.Release != "" {
			obs.TagDrift = !releaseTags(parsed, desired)[obs.Release]
		}
		// The canonical caller, as documented in docs/callers.md. Its presence in
		// the default branch is checked first: the runs API resolves a workflow by
		// file name even after the file is deleted, so querying blindly reports
		// startup_failure runs of a workflow the repository no longer has.
		// svn-easy-kit is exactly that case, and its manifest entry is disabled
		// for the same reason.
		caller := ".github/workflows/release.yml"
		if _, present, err := client.ReadFile(ctx, repo.Name, caller, repo.DefaultBranch); err == nil && present {
			if run, found, err := client.LatestCompletedWorkflowRun(ctx, repo.Name, "release.yml"); err == nil && found {
				obs.RunConclusion = run.Conclusion
			}
		}
	}

	applyAssessment(repo, health.Assess(parsed, obs))
	return nil
}

// desiredVersion mirrors release_infra.inventory._desired_manifest_version: the
// manifest entry for the policy's component, else the manifest's only entry.
func desiredVersion(ctx context.Context, client *github.Bound, repo *Repository, parsed *policy.Policy) (string, error) {
	raw, found, err := client.ReadFile(ctx, repo.Name, ".release-please-manifest.json", repo.DefaultBranch)
	if err != nil || !found {
		return "", err
	}
	versions := map[string]string{}
	if err := json.Unmarshal(raw, &versions); err != nil {
		return "", nil
	}
	pkg := parsed.Versioning.Package
	if pkg == "" {
		pkg = "."
	}
	if version, ok := versions[pkg]; ok {
		return version, nil
	}
	for _, version := range versions {
		return version, nil
	}
	return "", nil
}

// releaseTags mirrors release_infra.inventory._release_tags: the bare and v-
// prefixed version, plus the policy's own tag template when it declares one.
func releaseTags(parsed *policy.Policy, version string) map[string]bool {
	tags := map[string]bool{version: true, "v" + version: true}
	if parsed.Tag.Template != "" {
		tags[policy.ReleaseTag(parsed, version)] = true
	}
	return tags
}

func stringValue(values map[string]any, key, fallback string) string {
	if value, ok := values[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func boolValue(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}
