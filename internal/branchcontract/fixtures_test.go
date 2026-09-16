package branchcontract

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/policy"
)

// The fixtures in this file rebuild the real incident with real git topology:
// a cutover branch derived from an unmerged feature branch, the feature
// squash-merged into main, and the cutover PR opened against the new main.
// These tests are permanent regression fixtures for the contract.

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initRepo(t *testing.T, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init")
	gitCmd(t, dir, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	gitCmd(t, dir, "config", "user.email", "contract@example.com")
	gitCmd(t, dir, "config", "user.name", "contract-test")
	gitCmd(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

func commitFile(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "commit", "-m", msg)
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

func writePolicy(t *testing.T, dir, yaml string) *policy.Policy {
	t.Helper()
	path := filepath.Join(dir, ".release-policy.yml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := policy.Load(path)
	if err != nil {
		t.Fatalf("load policy: %v", err)
	}
	return p
}

// commitFileOnly commits a single file (leaving other worktree files, such as
// the policy fixture, untracked).
func commitFileOnly(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", name)
	gitCmd(t, dir, "commit", "-m", msg)
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

const basePolicyYAML = `apiVersion: releasegraph.dev/v1
kind: binary
versioning:
  mode: manual
  version: 1.0.0
assets:
  required: []
registries: {}
repository:
  git:
    productionOperations:
      base: default
      branches:
        - "chore/cutover-*"
        - "ops/*"
        - "release/*"
        - "hotfix/*"
      requireLatestBase: true
      operations:
        cutover:
          branches:
            - "chore/cutover-*"
          allowedPaths:
            - "fly/*.toml"
            - ".github/workflows/**"
`

// incidentFixture builds the real incident topology:
//
//	A---S          main (S = squash of feature B+C)
//	 \
//	  B---C        feat/provider
//	       \
//	        D      chore/cutover-provider-fly
func incidentFixture(t *testing.T) (dir string, p *policy.Policy) {
	t.Helper()
	dir = initRepo(t, "main")
	commitFile(t, dir, "config.toml", "base = true\n", "A: base config")
	gitCmd(t, dir, "switch", "-c", "feat/provider")
	commitFile(t, dir, "provider/impl.go", "package provider\n", "B: provider implementation")
	commitFile(t, dir, "provider/more.go", "package provider\n", "C: more provider implementation")
	gitCmd(t, dir, "switch", "-c", "chore/cutover-provider-fly")
	commitFile(t, dir, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "D: cutover provider to fly")
	gitCmd(t, dir, "switch", "main")
	// Squash merge the feature into main: same content, different commit identity.
	gitCmd(t, dir, "merge", "--squash", "feat/provider")
	gitCmd(t, dir, "commit", "-m", "feat: provider implementation (#1)")
	p = writePolicy(t, dir, basePolicyYAML)
	return dir, p
}

func evaluateIn(t *testing.T, dir string, p *policy.Policy, head, base, defaultBranch string) Result {
	t.Helper()
	result, err := Evaluate(Input{HeadRef: head, BaseRef: base, DefaultBranch: defaultBranch, Policy: p}, ExecGit{Dir: dir})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return result
}

func TestFixtureSquashMergedIncidentFails(t *testing.T) {
	dir, p := incidentFixture(t)
	result := evaluateIn(t, dir, p, "chore/cutover-provider-fly", "main", "main")
	if !result.Matched {
		t.Fatal("cutover branch must match the production-operation patterns")
	}
	if result.Compliant {
		t.Fatalf("the real incident topology must FAIL, got %+v", result)
	}
	found := false
	for _, violation := range result.Violations {
		if violation.Code == ViolationAncestry {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ancestry violation, got %+v", result.Violations)
	}
	if result.Evidence == nil || result.Evidence.MergeBaseSHA == result.Evidence.BaseSHA {
		t.Fatalf("merge-base must differ from base HEAD in the incident: %+v", result.Evidence)
	}
}

func TestFixtureRecreatedCutoverFromLatestMainPasses(t *testing.T) {
	dir, p := incidentFixture(t)
	// Failure recovery per the contract: recreate from the latest main and
	// reapply ONLY the intended production transition.
	gitCmd(t, dir, "switch", "-c", "chore/cutover-provider-fly-v2", "main")
	commitFileOnly(t, dir, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "cutover EXECUTION_PROVIDER github -> fly")
	result := evaluateIn(t, dir, p, "chore/cutover-provider-fly-v2", "main", "main")
	if !result.Compliant {
		t.Fatalf("recreated cutover must PASS, got %+v", result)
	}
	if result.Evidence == nil || result.Evidence.MergeBaseSHA != result.Evidence.BaseSHA {
		t.Fatalf("merge-base must equal base HEAD: %+v", result.Evidence)
	}
}

func TestFixtureFeatureStackingAllowed(t *testing.T) {
	dir := initRepo(t, "main")
	commitFile(t, dir, "config.toml", "base = true\n", "A: base")
	gitCmd(t, dir, "switch", "-c", "feat/provider")
	commitFile(t, dir, "provider/impl.go", "package provider\n", "B")
	gitCmd(t, dir, "switch", "-c", "feat/provider-tests")
	commitFile(t, dir, "provider/impl_test.go", "package provider\n", "C")
	p := writePolicy(t, dir, basePolicyYAML)
	// Feature branches from old main and stacked on feature are skipped.
	for _, head := range []string{"feat/provider", "feat/provider-tests"} {
		result := evaluateIn(t, dir, p, head, "main", "main")
		if result.Matched || !result.Compliant {
			t.Fatalf("feature branch %q must be unaffected: %+v", head, result)
		}
	}
}

func TestFixtureCutoverDirectlyFromLatestMainPasses(t *testing.T) {
	dir := initRepo(t, "main")
	commitFile(t, dir, "config.toml", "base = true\n", "A")
	gitCmd(t, dir, "switch", "-c", "chore/cutover-provider-fly")
	commitFile(t, dir, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "D")
	p := writePolicy(t, dir, basePolicyYAML)
	if result := evaluateIn(t, dir, p, "chore/cutover-provider-fly", "main", "main"); !result.Compliant {
		t.Fatalf("cutover from latest main must PASS: %+v", result)
	}
}

func TestFixtureMainAdvancedFails(t *testing.T) {
	dir := initRepo(t, "main")
	commitFile(t, dir, "config.toml", "base = true\n", "A")
	gitCmd(t, dir, "switch", "-c", "chore/cutover-provider-fly")
	commitFile(t, dir, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "D")
	gitCmd(t, dir, "switch", "main")
	commitFile(t, dir, "other.toml", "x = 1\n", "B: main advanced after the PR opened")
	p := writePolicy(t, dir, basePolicyYAML)
	result := evaluateIn(t, dir, p, "chore/cutover-provider-fly", "main", "main")
	if result.Compliant {
		t.Fatalf("open production PR must turn red after main advanced: %+v", result)
	}
}

func TestFixtureProductionBranchTargetsFeatureAsBaseFails(t *testing.T) {
	dir := initRepo(t, "main")
	commitFile(t, dir, "config.toml", "base = true\n", "A")
	gitCmd(t, dir, "switch", "-c", "feat/provider")
	commitFile(t, dir, "provider/impl.go", "package provider\n", "B")
	gitCmd(t, dir, "switch", "-c", "chore/cutover-provider-fly")
	commitFile(t, dir, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "D")
	p := writePolicy(t, dir, basePolicyYAML)
	result := evaluateIn(t, dir, p, "chore/cutover-provider-fly", "feat/provider", "main")
	if result.Compliant {
		t.Fatalf("production PR targeting a feature branch must FAIL: %+v", result)
	}
	found := false
	for _, violation := range result.Violations {
		if violation.Code == ViolationBaseTarget {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected base-target violation, got %+v", result.Violations)
	}
}

func TestFixtureReleaseHotfixOpsFromLatestMainPass(t *testing.T) {
	for _, name := range []string{"release/v1", "hotfix/urgent", "ops/fly-executor"} {
		t.Run(name, func(t *testing.T) {
			dir := initRepo(t, "main")
			commitFile(t, dir, "config.toml", "base = true\n", "A")
			gitCmd(t, dir, "switch", "-c", name)
			commitFile(t, dir, ".github/workflows/x.yml", "on: push\n", name)
			p := writePolicy(t, dir, basePolicyYAML)
			if result := evaluateIn(t, dir, p, name, "main", "main"); !result.Compliant {
				t.Fatalf("%s from latest main must PASS: %+v", name, result)
			}
		})
	}
}

func TestFixtureDefaultBranchMasterResolved(t *testing.T) {
	dir := initRepo(t, "master")
	commitFile(t, dir, "config.toml", "base = true\n", "A")
	gitCmd(t, dir, "switch", "-c", "chore/cutover-provider-fly")
	commitFile(t, dir, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "D")
	p := writePolicy(t, dir, basePolicyYAML)
	// base: default must resolve through the caller-provided default branch.
	if result := evaluateIn(t, dir, p, "chore/cutover-provider-fly", "master", "master"); !result.Compliant {
		t.Fatalf("default branch master must resolve correctly: %+v", result)
	}
	// Declaring base: master explicitly obeys the same topology.
	policyYAML := strings.Replace(basePolicyYAML, "base: default", "base: master", 1)
	p = writePolicy(t, dir, policyYAML)
	if result := evaluateIn(t, dir, p, "chore/cutover-provider-fly", "master", ""); !result.Compliant {
		t.Fatalf("explicit custom base must be honored: %+v", result)
	}
}

func TestFixtureScopeViolationFails(t *testing.T) {
	dir := initRepo(t, "main")
	commitFile(t, dir, "config.toml", "base = true\n", "A")
	gitCmd(t, dir, "switch", "-c", "chore/cutover-provider-fly")
	commitFile(t, dir, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "D1")
	commitFile(t, dir, "provider/impl.go", "package provider\n", "D2: implementation smuggled into the cutover")
	p := writePolicy(t, dir, basePolicyYAML)
	result := evaluateIn(t, dir, p, "chore/cutover-provider-fly", "main", "main")
	if result.Compliant {
		t.Fatalf("cutover carrying implementation must FAIL the scope check: %+v", result)
	}
	found := false
	for _, violation := range result.Violations {
		if violation.Code == ViolationScope && strings.Contains(violation.Detail, "provider/impl.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected scope violation naming provider/impl.go, got %+v", result.Violations)
	}
}

// TestFixtureRemoteBaseAdvanced models CI evidence: the production gate
// fetches origin/<base>, so the contract must evaluate against the remote
// base HEAD, not the local one.
func TestFixtureRemoteBaseAdvanced(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	remote := t.TempDir()
	gitCmd(t, remote, "init", "--bare")
	gitCmd(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	work := t.TempDir()
	gitCmd(t, work, "clone", remote, ".")
	gitCmd(t, work, "config", "user.email", "contract@example.com")
	gitCmd(t, work, "config", "user.name", "contract-test")
	commitFile(t, work, "config.toml", "base = true\n", "A")
	gitCmd(t, work, "push", "origin", "main")
	gitCmd(t, work, "switch", "-c", "chore/cutover-provider-fly")
	commitFile(t, work, "fly/service.toml", "EXECUTION_PROVIDER = \"fly\"\n", "D")
	p := writePolicy(t, work, basePolicyYAML)
	if result := evaluateIn(t, work, p, "chore/cutover-provider-fly", "main", "main"); !result.Compliant {
		t.Fatalf("cutover against current remote base must PASS: %+v", result)
	}
	// Another developer advances the remote main; the gate re-evaluates.
	other := t.TempDir()
	gitCmd(t, other, "clone", remote, ".")
	gitCmd(t, other, "config", "user.email", "contract@example.com")
	gitCmd(t, other, "config", "user.name", "contract-test")
	commitFile(t, other, "other.toml", "x = 1\n", "B")
	gitCmd(t, other, "push", "origin", "main")
	gitCmd(t, work, "fetch", "origin")
	result := evaluateIn(t, work, p, "chore/cutover-provider-fly", "main", "main")
	if result.Compliant {
		t.Fatalf("open production PR must fail after the remote base advanced: %+v", result)
	}
	if result.Evidence == nil || result.Evidence.BaseRef != "origin/main" {
		t.Fatalf("evidence must resolve the remote-tracking base ref: %+v", result.Evidence)
	}
}
