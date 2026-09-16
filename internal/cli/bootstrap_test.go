package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// init derives capabilities from what the repository actually contains.
func TestInitDetectsCapabilities(t *testing.T) {
	python := t.TempDir()
	writeFile(t, python, "pyproject.toml", "[project]\nname = \"widget\"\n")
	proposal := detectPolicy(python)
	if proposal.Confidence != "HIGH" {
		t.Fatalf("confidence = %s (%v)", proposal.Confidence, proposal.Notes)
	}
	if !contains(proposal.Capabilities, "pypi") || contains(proposal.Capabilities, "binary") {
		t.Fatalf("capabilities = %v, want pypi without binaries", proposal.Capabilities)
	}

	cli := t.TempDir()
	writeFile(t, cli, "go.mod", "module app\n")
	writeFile(t, cli, "scripts/build-release", "#!/bin/sh\n")
	proposal = detectPolicy(cli)
	if !contains(proposal.Capabilities, "binary") || !contains(proposal.Capabilities, "checksums") {
		t.Fatalf("capabilities = %v, want binary + checksums", proposal.Capabilities)
	}
	assets, _ := proposal.Policy["assets"].(map[string]any)
	if required, _ := assets["required"].([]string); len(required) == 0 {
		t.Fatal("a binary project must declare required assets")
	}
}

// Ambiguity is reported instead of guessed.
func TestInitMarksAmbiguousRepositoriesForConfirmation(t *testing.T) {
	mixed := t.TempDir()
	writeFile(t, mixed, "pyproject.toml", "[project]\nname = \"widget\"\n")
	writeFile(t, mixed, "package.json", "{\"name\":\"widget\"}\n")
	proposal := detectPolicy(mixed)
	if proposal.Confidence != "NEEDS_CONFIRMATION" {
		t.Fatalf("confidence = %s, want NEEDS_CONFIRMATION", proposal.Confidence)
	}
	if len(proposal.Notes) == 0 {
		t.Fatal("ambiguity must come with a reason")
	}

	empty := t.TempDir()
	if proposal := detectPolicy(empty); proposal.Confidence != "NEEDS_CONFIRMATION" {
		t.Fatalf("empty repository confidence = %s", proposal.Confidence)
	}
}

// migrate makes an implicit contract explicit and is a no-op when it already is.
func TestMigrateStatesCapabilitiesExplicitly(t *testing.T) {
	dir := t.TempDir()
	policyPath := filepath.Join(dir, ".release-policy.yml")
	legacy := `{
  "kind": "binary",
  "versioning": {"mode": "manual", "version": "1.0.0"},
  "build": {"matrix": [{"runner": "ubuntu-latest", "command": "make"}]},
  "assets": {"required": ["app-linux"]},
  "registries": {"github": {"required": true}},
  "checksums": true
}`
	if err := os.WriteFile(policyPath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _ := captureRun(t, []string{"migrate", "--path", policyPath, "--format", "json"})
	if stdout == "" {
		t.Fatal("migrate produced no output")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, stdout)
	}
	data, _ := out["data"].(map[string]any)
	if changed, _ := data["changed"].(bool); !changed {
		t.Fatal("a legacy implicit policy must report a change")
	}

	if _, err := captureRun(t, []string{"migrate", "--path", policyPath, "--apply", "--format", "json"}); err != nil {
		t.Fatal(err)
	}
	migrated, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(migrated, &parsed); err != nil {
		t.Fatal(err)
	}
	artifacts, _ := parsed["artifacts"].(map[string]any)
	binaries, _ := artifacts["binaries"].(map[string]any)
	if enabled, _ := binaries["enabled"].(bool); !enabled {
		t.Fatalf("migration lost the binary contract: %s", migrated)
	}
	if schema, _ := parsed["policy_schema"].(float64); int(schema) != 1 {
		t.Fatalf("policy_schema = %v, want 1", parsed["policy_schema"])
	}

	// Second migration is a no-op.
	out2, _ := captureRun(t, []string{"migrate", "--path", policyPath, "--format", "json"})
	var second map[string]any
	_ = json.Unmarshal([]byte(out2), &second)
	if data, _ := second["data"].(map[string]any); data != nil {
		if changed, _ := data["changed"].(bool); changed {
			t.Fatal("migration is not idempotent")
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
