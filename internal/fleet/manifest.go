package fleet

import (
	"fmt"
	rgdomain "github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/health"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Classification of a repository with respect to the fleet manifest.
const (
	// ClassificationManaged is a repository listed in the manifest with enabled: true.
	ClassificationManaged = "MANAGED"
	// ClassificationDisabled is listed but explicitly switched off.
	ClassificationDisabled = "DISABLED"
	// ClassificationArchived is managed on paper but archived on GitHub.
	ClassificationArchived = "ARCHIVED"
	// ClassificationDiscoveredUnmanaged exists on GitHub but is NOT part of the
	// fleet. GitHub discovery never implies ownership.
	ClassificationDiscoveredUnmanaged = "DISCOVERED_UNMANAGED"
)

// ManifestEntry is one managed repository declared by fleet.yaml.
type ManifestEntry struct {
	Name    string `yaml:"name" json:"name"`
	Enabled *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Canary  bool   `yaml:"canary,omitempty" json:"canary,omitempty"`
	Note    string `yaml:"note,omitempty" json:"note,omitempty"`
}

// IsEnabled reports the effective enabled state (default true).
func (e ManifestEntry) IsEnabled() bool { return e.Enabled == nil || *e.Enabled }

// Manifest is the authoritative list of repositories ReleaseGraph may manage.
type Manifest struct {
	Version      int             `yaml:"version" json:"version"`
	Repositories []ManifestEntry `yaml:"repositories" json:"repositories"`
	path         string
}

// LoadManifest reads fleet.yaml. A missing manifest is an error: without it
// there is no authoritative answer to "which repositories are managed".
func LoadManifest(path string) (*Manifest, error) {
	if path == "" {
		path = "fleet.yaml"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fleet manifest %s: %w", path, err)
	}
	var manifest Manifest
	if err := yaml.Unmarshal(raw, &manifest); err != nil {
		return nil, fmt.Errorf("fleet manifest %s: %w", path, err)
	}
	if manifest.Version != 1 {
		return nil, fmt.Errorf("fleet manifest %s: unsupported version %d", path, manifest.Version)
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Repositories {
		if entry.Name == "" || !strings.Contains(entry.Name, "/") {
			return nil, fmt.Errorf("fleet manifest %s: repository %q must be owner/name", path, entry.Name)
		}
		if seen[entry.Name] {
			return nil, fmt.Errorf("fleet manifest %s: duplicate repository %q", path, entry.Name)
		}
		seen[entry.Name] = true
	}
	manifest.path = path
	return &manifest, nil
}

// Names returns every declared repository, enabled or not, sorted.
func (m *Manifest) Names() []string {
	names := make([]string, 0, len(m.Repositories))
	for _, entry := range m.Repositories {
		names = append(names, entry.Name)
	}
	sort.Strings(names)
	return names
}

// EnabledNames returns the repositories ReleaseGraph may operate on.
func (m *Manifest) EnabledNames() []string {
	names := []string{}
	for _, entry := range m.Repositories {
		if entry.IsEnabled() {
			names = append(names, entry.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Entry returns the manifest entry for a repository, if declared.
func (m *Manifest) Entry(name string) (ManifestEntry, bool) {
	for _, entry := range m.Repositories {
		if strings.EqualFold(entry.Name, name) {
			return entry, true
		}
	}
	return ManifestEntry{}, false
}

// Canary returns the repository designated as the infrastructure canary.
func (m *Manifest) Canary() (string, bool) {
	for _, entry := range m.Repositories {
		if entry.Canary {
			return entry.Name, true
		}
	}
	return "", false
}

// Authority reports whether a repository is managed. This is the single answer
// to "may ReleaseGraph operate on this repository?".
func (m *Manifest) Authority(name string) (bool, string) {
	entry, ok := m.Entry(name)
	if !ok {
		return false, ClassificationDiscoveredUnmanaged
	}
	if !entry.IsEnabled() {
		return false, ClassificationDisabled
	}
	return true, ClassificationManaged
}

// Resolved is one repository after intersecting the manifest with observed
// GitHub metadata. Metadata may enrich an entry, never create one.
type Resolved struct {
	Name           string `json:"name"`
	Classification string `json:"classification"`
	Canary         bool   `json:"canary,omitempty"`
	Archived       bool   `json:"archived,omitempty"`
	Visibility     string `json:"visibility,omitempty"`
	DefaultBranch  string `json:"defaultBranch,omitempty"`
	Note           string `json:"note,omitempty"`
	// Release health, from the shared contract in internal/health. It is filled
	// in only when the repository was discovered with enrichment, so an entry
	// resolved from the manifest alone carries no health rather than a made-up
	// one. Reported by `fleet audit`.
	Health        rgdomain.Health `json:"health,omitempty"`
	HealthReasons []health.Reason `json:"healthReasons,omitempty"`
	MissingAssets []string        `json:"missingAssets,omitempty"`
	EmptyAssets   []string        `json:"emptyAssets,omitempty"`
}

// Resolve intersects manifest entries with discovered repositories.
//
// Invariant: discovery never implies ownership. A repository that exists on
// GitHub but is absent from the manifest is only ever reported as
// DISCOVERED_UNMANAGED and is never returned as managed.
func Resolve(manifest *Manifest, discovered []Repository) (managed []Resolved, unmanaged []Resolved) {
	byName := map[string]Repository{}
	for _, repo := range discovered {
		byName[repo.Name] = repo
	}

	managed = []Resolved{}
	unmanaged = []Resolved{}
	for _, entry := range manifest.Repositories {
		resolved := Resolved{Name: entry.Name, Canary: entry.Canary, Note: entry.Note, Classification: ClassificationManaged}
		if !entry.IsEnabled() {
			resolved.Classification = ClassificationDisabled
		}
		if observed, ok := byName[entry.Name]; ok {
			resolved.Archived = observed.Archived
			resolved.Visibility = observed.Visibility
			resolved.DefaultBranch = observed.DefaultBranch
			resolved.Health = observed.Health
			resolved.HealthReasons = observed.HealthReasons
			resolved.MissingAssets = observed.MissingAssets
			resolved.EmptyAssets = observed.EmptyAssets
			if observed.Archived && resolved.Classification == ClassificationManaged {
				resolved.Classification = ClassificationArchived
			}
		}
		managed = append(managed, resolved)
	}
	sort.Slice(managed, func(i, j int) bool { return managed[i].Name < managed[j].Name })

	for _, repo := range discovered {
		if _, declared := manifest.Entry(repo.Name); declared {
			continue
		}
		unmanaged = append(unmanaged, Resolved{
			Name:           repo.Name,
			Classification: ClassificationDiscoveredUnmanaged,
			Archived:       repo.Archived,
			Visibility:     repo.Visibility,
			DefaultBranch:  repo.DefaultBranch,
		})
	}
	sort.Slice(unmanaged, func(i, j int) bool { return unmanaged[i].Name < unmanaged[j].Name })
	return managed, unmanaged
}

// Operable returns the subset of managed repositories that may be operated on
// right now (managed, not disabled, not archived).
func Operable(managed []Resolved) []string {
	names := []string{}
	for _, repo := range managed {
		if repo.Classification == ClassificationManaged {
			names = append(names, repo.Name)
		}
	}
	sort.Strings(names)
	return names
}
