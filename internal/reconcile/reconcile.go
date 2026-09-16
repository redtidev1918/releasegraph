package reconcile

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/graph"
	"github.com/redtidev1918/releasegraph/internal/health"
	"github.com/redtidev1918/releasegraph/internal/policy"
	"github.com/redtidev1918/releasegraph/internal/registry"
)

type apiRelease struct {
	TagName         string `json:"tag_name"`
	Draft           bool   `json:"draft"`
	Prerelease      bool   `json:"prerelease"`
	TargetCommitish string `json:"target_commitish"`
	HTMLURL         string `json:"html_url"`
	Assets          []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func Inspect(ctx context.Context, client *github.Client, releaseGraph *domain.ReleaseGraph) (*domain.Plan, error) {
	order, err := graph.TopSort(releaseGraph)
	if err != nil {
		return nil, err
	}
	nodes := make(map[string]domain.NodePlan, len(order))
	verifier := registry.New()
	for _, id := range order {
		node, err := inspectProject(ctx, client, verifier, releaseGraph.Projects[id])
		if err != nil {
			return nil, err
		}
		nodes[id] = node
	}

	out := &domain.Plan{Ready: []string{}, Blocked: []string{}, Noop: []string{}, Nodes: make([]domain.NodePlan, 0, len(order))}
	for _, id := range order {
		node := nodes[id]
		if node.Health == domain.HealthHealthy || node.Health == domain.HealthNoop {
			out.Noop = append(out.Noop, id)
			out.Nodes = append(out.Nodes, node)
			continue
		}
		for _, dependency := range releaseGraph.Projects[id].DependsOn {
			if !conditionSatisfied(dependency.Condition, nodes[dependency.ID].Health) {
				node.BlockedBy = append(node.BlockedBy, dependency.ID)
			}
		}
		sort.Strings(node.BlockedBy)
		if len(node.BlockedBy) == 0 && (node.Health == domain.HealthReady || node.Health == domain.HealthRecoverable) {
			out.Ready = append(out.Ready, id)
		} else {
			if node.Health == domain.HealthReady || node.Health == domain.HealthRecoverable {
				node.Health = domain.HealthBlocked
			}
			out.Blocked = append(out.Blocked, id)
		}
		out.Nodes = append(out.Nodes, node)
	}
	return out, nil
}

func inspectProject(ctx context.Context, client *github.Client, verifier *registry.Verifier, project domain.Project) (domain.NodePlan, error) {
	node := domain.NodePlan{ID: project.ID, Kind: project.Kind, Repository: project.Repo.FullName()}
	if project.Kind != domain.NodeKindRelease {
		node.Health = domain.HealthNeedsReview
		node.Failures = []domain.Failure{{Code: "UNSUPPORTED_NODE_KIND", Message: "live inspection currently supports release nodes only"}}
		return node, nil
	}
	raw, found, err := client.ReadFile(ctx, project.Repo.FullName(), ".release-policy.yml", "")
	if err != nil {
		return node, err
	}
	if !found {
		node.Health = domain.HealthUnmanaged
		node.Failures = []domain.Failure{{Code: "POLICY_NOT_FOUND", Message: ".release-policy.yml is missing"}}
		return node, nil
	}
	p, err := policy.Parse(raw)
	if err != nil {
		node.Health = domain.HealthBroken
		node.Failures = []domain.Failure{{Code: "POLICY_ERROR", Message: err.Error()}}
		return node, nil
	}
	desired, err := desiredVersion(ctx, client, project.Repo.FullName(), p)
	if err != nil {
		node.Health = domain.HealthBroken
		node.Failures = []domain.Failure{{Code: "VERSION_ERROR", Message: err.Error()}}
		return node, nil
	}
	node.Desired = &domain.DesiredState{Version: domain.Version(desired)}
	tag := policy.ReleaseTag(p, desired)
	var release apiRelease
	found, err = client.GetOptional(ctx, fmt.Sprintf("repos/%s/releases/tags/%s", project.Repo.FullName(), url.PathEscape(tag)), &release)
	if err != nil {
		return node, err
	}
	if !found {
		node.Health = domain.HealthReady
		return node, nil
	}
	assets := make([]domain.ReleaseAsset, 0, len(release.Assets))
	for _, asset := range release.Assets {
		assets = append(assets, domain.ReleaseAsset{Name: asset.Name, Size: asset.Size})
	}
	node.Actual = &domain.ActualState{
		Version: domain.Version(strings.TrimPrefix(release.TagName, "v")),
		Release: &domain.Release{Tag: release.TagName, Draft: release.Draft, Pre: release.Prerelease, Commit: domain.Commit(release.TargetCommitish), Assets: assets, URL: release.HTMLURL},
	}
	missing, err := missingAssets(p, release.Assets)
	if err != nil {
		node.Health = domain.HealthBroken
		node.Actual.Health = node.Health
		node.Failures = []domain.Failure{{Code: "ASSET_ERROR", Message: err.Error()}}
		return node, nil
	}
	if len(missing) > 0 {
		node.Failures = []domain.Failure{{Code: "ASSET_ERROR", Message: "missing or empty required assets: " + strings.Join(missing, ", ")}}
		if release.Draft {
			node.Health = domain.HealthRecoverable
		} else {
			node.Health = domain.HealthBroken
		}
		node.Actual.Health = node.Health
		return node, nil
	}
	registryNames := make([]string, 0, len(p.Registries))
	for name := range p.Registries {
		registryNames = append(registryNames, name)
	}
	sort.Strings(registryNames)
	for _, name := range registryNames {
		config := p.Registries[name]
		if name == "github" {
			node.Actual.Registries = append(node.Actual.Registries, domain.Registry{Name: name, Required: config.Required, Version: desired, Healthy: true})
			continue
		}
		healthy := true
		message := ""
		if config.Required {
			var metadata []byte
			var err error
			if metadataFile := registryMetadataFile(name); metadataFile != "" {
				metadata, _, err = client.ReadFile(ctx, project.Repo.FullName(), metadataFile, "")
				if err != nil {
					return node, err
				}
			}
			if err := verifier.Verify(ctx, name, config, project.Repo.FullName(), desired, metadata); err != nil {
				healthy = false
				message = err.Error()
			}
		}
		node.Actual.Registries = append(node.Actual.Registries, domain.Registry{Name: name, Required: config.Required, Version: desired, Healthy: healthy})
		if !healthy {
			node.Health = domain.HealthRecoverable
			node.Actual.Health = node.Health
			node.Failures = append(node.Failures, domain.Failure{Code: "REGISTRY_CONFLICT", Message: name + ": " + message})
			return node, nil
		}
	}
	if release.Draft {
		node.Health = domain.HealthRecoverable
		node.Actual.Health = node.Health
		return node, nil
	}
	if !release.Prerelease {
		var latest apiRelease
		latestFound, err := client.GetOptional(ctx, fmt.Sprintf("repos/%s/releases/latest", project.Repo.FullName()), &latest)
		if err != nil {
			return node, err
		}
		if !latestFound || latest.TagName != tag {
			node.Health = domain.HealthRecoverable
			node.Actual.Health = node.Health
			node.Failures = []domain.Failure{{Code: "LATEST_MISMATCH", Message: tag + " is not Latest"}}
			return node, nil
		}
		node.Actual.Release.Latest = true
	}
	node.Health = domain.HealthHealthy
	node.Actual.Health = domain.HealthHealthy
	return node, nil
}

func desiredVersion(ctx context.Context, client *github.Client, repo string, p *policy.Policy) (string, error) {
	mode := p.Versioning.Mode
	if mode == "" {
		mode = p.Versioning.Provider
	}
	if mode != "release-please" {
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

func missingAssets(p *policy.Policy, assets []struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}) ([]string, error) {
	// One implementation of the required set, shared with the health assessment
	// and (through testdata/health/cases.json) with the Python planner.
	required := health.RequiredAssets(p)
	missing := []string{}
	for _, pattern := range required {
		matched := false
		for _, asset := range assets {
			ok, err := policy.MatchAsset(pattern, asset.Name)
			if err != nil {
				return nil, fmt.Errorf("invalid asset pattern %q: %w", pattern, err)
			}
			if ok && asset.Size > 0 {
				matched = true
				break
			}
		}
		if !matched {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	return missing, nil
}

func registryMetadataFile(name string) string {
	switch name {
	case "npm":
		return "package.json"
	case "pypi":
		return "pyproject.toml"
	case "pub":
		return "pubspec.yaml"
	default:
		return ""
	}
}

func conditionSatisfied(condition domain.DependencyCondition, actual domain.Health) bool {
	if condition == "" || condition == domain.ConditionHealthy {
		return actual == domain.HealthHealthy
	}
	return actual == domain.HealthHealthy || actual == domain.HealthNoop
}
