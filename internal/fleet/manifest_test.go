package fleet

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const manifestYAML = `version: 1
repositories:
  - name: acme/app
    enabled: true
    canary: true
  - name: acme/lib
    enabled: true
  - name: acme/old
    enabled: false
`

func TestManifestIsAuthorityNotDiscovery(t *testing.T) {
	manifest, err := LoadManifest(write(t, manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	// GitHub reports a third repository that the manifest never declared.
	discovered := []Repository{
		{Name: "acme/app", Visibility: "private", DefaultBranch: "main"},
		{Name: "acme/lib", Visibility: "public", DefaultBranch: "main"},
		{Name: "acme/old", Visibility: "public", DefaultBranch: "main"},
		{Name: "acme/secret-experiment", Visibility: "private"},
		{Name: "acme/archived-fork", Archived: true},
	}

	managed, unmanaged := Resolve(manifest, discovered)
	if len(managed) != 3 {
		t.Fatalf("managed = %+v, want the 3 declared repositories", managed)
	}
	for _, repo := range unmanaged {
		if repo.Classification != ClassificationDiscoveredUnmanaged {
			t.Errorf("%s classified %s", repo.Name, repo.Classification)
		}
	}
	if len(unmanaged) != 2 {
		t.Fatalf("unmanaged = %+v, want 2 discovered-only repositories", unmanaged)
	}

	// Discovery alone never grants authority.
	if ok, why := manifest.Authority("acme/secret-experiment"); ok {
		t.Errorf("discovered repository granted authority (%s)", why)
	}
	if ok, why := manifest.Authority("acme/app"); !ok {
		t.Errorf("declared repository denied authority (%s)", why)
	}
	// Disabled entries are declared but not operable.
	if ok, why := manifest.Authority("acme/old"); ok || why != ClassificationDisabled {
		t.Errorf("disabled repository: ok=%v why=%s", ok, why)
	}

	operable := Operable(managed)
	if len(operable) != 2 || operable[0] != "acme/app" || operable[1] != "acme/lib" {
		t.Fatalf("operable = %v", operable)
	}
}

func TestManifestArchivedRepoIsClassified(t *testing.T) {
	manifest, err := LoadManifest(write(t, manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	managed, _ := Resolve(manifest, []Repository{
		{Name: "acme/app", Archived: true, Visibility: "public", DefaultBranch: "main"},
		{Name: "acme/lib", Visibility: "public", DefaultBranch: "main"},
	})
	byName := map[string]Resolved{}
	for _, repo := range managed {
		byName[repo.Name] = repo
	}
	if byName["acme/app"].Classification != ClassificationArchived {
		t.Errorf("archived managed repo = %s, want ARCHIVED", byName["acme/app"].Classification)
	}
	if byName["acme/lib"].Classification != ClassificationManaged {
		t.Errorf("healthy managed repo = %s", byName["acme/lib"].Classification)
	}
	if operable := Operable(managed); len(operable) != 1 || operable[0] != "acme/lib" {
		t.Fatalf("operable = %v, want only the non-archived repository", operable)
	}
}

func TestManifestCanaryAndRejectsMalformedInput(t *testing.T) {
	manifest, err := LoadManifest(write(t, manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	if canary, ok := manifest.Canary(); !ok || canary != "acme/app" {
		t.Fatalf("canary = %q %v", canary, ok)
	}
	if got := manifest.EnabledNames(); len(got) != 2 {
		t.Fatalf("enabled = %v", got)
	}

	for name, body := range map[string]string{
		"duplicate": `version: 1
repositories:
  - name: acme/app
  - name: acme/app
`,
		"not owner/name": `version: 1
repositories:
  - name: app
`,
		"wrong version": `version: 2
repositories: []
`,
	} {
		if _, err := LoadManifest(write(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}

	// A missing manifest is an error, never an empty fleet.
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Error("missing manifest must be an error")
	}
}
