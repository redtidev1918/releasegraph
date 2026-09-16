package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/policy"
)

// initCommand derives a candidate release policy from what the repository
// actually contains. It never guesses silently: ambiguity is reported as
// NEEDS_CONFIRMATION and nothing is written without --write.
func initCommand(w io.Writer, args []string) error {
	var format, dir string
	var writeFile, force bool
	fs := flags(&format)
	fs.StringVar(&dir, "dir", ".", "repository root to inspect")
	fs.BoolVar(&writeFile, "write", false, "write .release-policy.yml (default: print the proposal)")
	fs.BoolVar(&force, "force", false, "overwrite an existing .release-policy.yml")
	if err := fs.Parse(args); err != nil {
		return err
	}

	proposal := detectPolicy(dir)
	result := map[string]any{
		"schemaVersion": statusSchemaVersion,
		"command":       "init",
		"directory":     dir,
		"detected":      proposal.Detected,
		"capabilities":  proposal.Capabilities,
		"confidence":    proposal.Confidence,
		"notes":         proposal.Notes,
		"policy":        proposal.Policy,
	}
	if proposal.Confidence == "NEEDS_CONFIRMATION" {
		result["action"] = "review the proposal, then write it with --write"
	}
	if !writeFile {
		if format == "json" || format == "" {
			return write(w, "json", result, nil)
		}
		humanInit(w, proposal)
		return nil
	}
	if proposal.Confidence == "NEEDS_CONFIRMATION" && !force {
		return fmt.Errorf("detection is ambiguous (%s); pass --force after reviewing the proposal", strings.Join(proposal.Notes, "; "))
	}
	target := filepath.Join(dir, ".release-policy.yml")
	if _, err := os.Stat(target); err == nil && !force {
		return fmt.Errorf("%s already exists; pass --force to overwrite", target)
	}
	encoded, err := json.MarshalIndent(proposal.Policy, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(target, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote %s\n", target)
	return nil
}

type policyProposal struct {
	Detected     []string       `json:"detected"`
	Capabilities []string       `json:"capabilities"`
	Confidence   string         `json:"confidence"`
	Notes        []string       `json:"notes,omitempty"`
	Policy       map[string]any `json:"policy"`
}

// detectPolicy infers a release contract from repository contents.
func detectPolicy(dir string) policyProposal {
	exists := func(name string) bool {
		info, err := os.Stat(filepath.Join(dir, name))
		return err == nil && !info.IsDir()
	}
	hasDir := func(name string) bool {
		info, err := os.Stat(filepath.Join(dir, name))
		return err == nil && info.IsDir()
	}

	proposal := policyProposal{Notes: []string{}}
	ecosystems := []string{}
	if exists("go.mod") {
		ecosystems = append(ecosystems, "go")
		proposal.Detected = append(proposal.Detected, "go.mod")
	}
	if exists("package.json") {
		ecosystems = append(ecosystems, "node")
		proposal.Detected = append(proposal.Detected, "package.json")
	}
	if exists("pyproject.toml") || exists("setup.py") {
		ecosystems = append(ecosystems, "python")
		if exists("pyproject.toml") {
			proposal.Detected = append(proposal.Detected, "pyproject.toml")
		} else {
			proposal.Detected = append(proposal.Detected, "setup.py")
		}
	}
	if exists("Cargo.toml") {
		ecosystems = append(ecosystems, "rust")
		proposal.Detected = append(proposal.Detected, "Cargo.toml")
	}
	if exists("pubspec.yaml") {
		ecosystems = append(ecosystems, "flutter")
		proposal.Detected = append(proposal.Detected, "pubspec.yaml")
	}
	if exists("Dockerfile") {
		proposal.Detected = append(proposal.Detected, "Dockerfile")
	}
	if hasDir("scripts") && exists("scripts/build-release") {
		proposal.Detected = append(proposal.Detected, "scripts/build-release")
	}
	sort.Strings(proposal.Detected)

	buildScript := exists("scripts/build-release")
	binaries := buildScript || exists("main.go") || hasDir("cmd")

	registries := map[string]any{"github": map[string]any{"required": true}}
	switch {
	case exists("package.json"):
		registries["npm"] = map[string]any{"required": false}
	case exists("pyproject.toml") || exists("setup.py"):
		registries["pypi"] = map[string]any{"required": false}
	}
	if exists("Dockerfile") {
		registries["ghcr"] = map[string]any{"required": false}
	}

	kind := "binary"
	switch {
	case len(ecosystems) == 1 && ecosystems[0] == "python":
		kind = "python-library"
	case len(ecosystems) == 1 && ecosystems[0] == "node":
		kind = "node-library"
	case exists("Dockerfile") && !binaries:
		kind = "container"
	case len(ecosystems) == 0 && !binaries:
		kind = "none"
	}

	assets := []string{}
	if binaries {
		assets = append(assets, "app-linux-amd64", "app-darwin-arm64", "app-windows-amd64.exe")
	}
	if binaries && !buildScript {
		proposal.Notes = append(proposal.Notes, "no scripts/build-release found; declare how the configured targets are built")
	}
	proposal.Capabilities = []string{"github-release"}
	if binaries {
		proposal.Capabilities = append(proposal.Capabilities, "binary", "checksums")
	}
	for name := range registries {
		if name != "github" {
			proposal.Capabilities = append(proposal.Capabilities, name)
		}
	}
	sort.Strings(proposal.Capabilities)

	proposal.Confidence = "HIGH"
	if len(ecosystems) > 1 {
		proposal.Confidence = "NEEDS_CONFIRMATION"
		proposal.Notes = append(proposal.Notes, fmt.Sprintf("multiple ecosystems detected: %s", strings.Join(ecosystems, ", ")))
	}
	if len(proposal.Detected) == 0 {
		proposal.Confidence = "NEEDS_CONFIRMATION"
		proposal.Notes = append(proposal.Notes, "nothing recognisable was detected; declare the contract explicitly")
	}

	proposal.Policy = map[string]any{
		"kind":       kind,
		"versioning": map[string]any{"mode": "release-please"},
		"artifacts":  map[string]any{"binaries": map[string]any{"enabled": binaries}},
		"assets":     map[string]any{"required": assets, "optional": []string{}},
		"registries": registries,
		"release":    map[string]any{"github": true},
		"retention":  map[string]any{"stable": 1, "prerelease": 1, "failed_draft": 2},
		"checksums":  binaries,
	}
	return proposal
}

func humanInit(w io.Writer, proposal policyProposal) {
	fmt.Fprintln(w, "detected:")
	for _, item := range proposal.Detected {
		fmt.Fprintln(w, "  "+item)
	}
	fmt.Fprintf(w, "suggested capabilities: %s\n", strings.Join(proposal.Capabilities, ", "))
	fmt.Fprintf(w, "confidence: %s\n", proposal.Confidence)
	for _, note := range proposal.Notes {
		fmt.Fprintln(w, "  note: "+note)
	}
	encoded, _ := json.MarshalIndent(proposal.Policy, "", "  ")
	fmt.Fprintf(w, "\nproposed .release-policy.yml:\n%s\n", encoded)
}

// migrateCommand makes an implicit contract explicit. ReleaseGraph derives
// capabilities from older policies for compatibility; migrating writes them down
// so the contract is stated rather than inferred.
func migrateCommand(w io.Writer, args []string) error {
	var format, path string
	var apply bool
	fs := flags(&format)
	fs.StringVar(&path, "path", ".release-policy.yml", "policy to migrate")
	fs.BoolVar(&apply, "apply", false, "write the migrated policy (default: dry run)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var current map[string]any
	if err := json.Unmarshal(raw, &current); err != nil {
		return fmt.Errorf("%s must use JSON syntax: %w", path, err)
	}
	loaded, err := policy.Parse(raw)
	if err != nil {
		return err
	}
	caps := loaded.CapabilitiesOf()

	migrated := map[string]any{}
	for key, value := range current {
		migrated[key] = value
	}
	migrated["policy_schema"] = 1
	migrated["artifacts"] = map[string]any{"binaries": map[string]any{"enabled": caps.Binaries}}
	release, _ := migrated["release"].(map[string]any)
	if release == nil {
		release = map[string]any{}
	}
	release["github"] = caps.GitHubRelease
	migrated["release"] = release

	changed := !capabilitiesExplicit(current, caps)
	out := map[string]any{
		"schemaVersion": statusSchemaVersion,
		"command":       "migrate",
		"path":          path,
		"changed":       changed,
		"capabilities":  caps,
		"mode":          mode(apply),
	}
	if !changed {
		if format == "json" || format == "" {
			return write(w, "json", out, nil)
		}
		fmt.Fprintf(w, "%s already declares its contract explicitly; nothing to migrate\n", path)
		return nil
	}
	if !apply {
		if format == "json" || format == "" {
			out["migrated"] = migrated
			return write(w, "json", out, nil)
		}
		fmt.Fprintf(w, "%s: capabilities are implicit; migration would state them explicitly\n", path)
		fmt.Fprintf(w, "  artifacts.binaries.enabled = %v\n  release.github = %v\n", caps.Binaries, caps.GitHubRelease)
		fmt.Fprintln(w, "\ndry run only; pass --apply to write")
		return nil
	}
	encoded, err := json.MarshalIndent(migrated, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "migrated %s (capabilities are now explicit)\n", path)
	return nil
}

// capabilitiesExplicit reports whether a policy states its capabilities.
func capabilitiesExplicit(current map[string]any, caps policy.Capabilities) bool {
	artifacts, ok := current["artifacts"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := artifacts["binaries"]; !ok {
		return false
	}
	release, ok := current["release"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = release["github"]
	_ = caps
	return ok
}
