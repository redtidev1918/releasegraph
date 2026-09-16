package rollout

import (
	"strings"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/fleet"
)

const workflowYAML = `name: Release
jobs:
  release:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1
    secrets: inherit
`

func testManifest(t *testing.T) *fleet.Manifest {
	t.Helper()
	manifest := &fleet.Manifest{Version: 1, Repositories: []fleet.ManifestEntry{
		{Name: "acme/app", Canary: true},
		{Name: "acme/lib"},
		{Name: "acme/deploy"},
	}}
	return manifest
}

func entry(name, ref string, canary bool, classification string) Entry {
	return Entry{Repository: name, Classification: classification, Canary: canary, Target: "v1.4.1", Current: ParsePin([]byte(replaceRef(workflowYAML, ref)))}
}

func replaceRef(workflow, ref string) string {
	pin := ParsePin([]byte(workflow))
	if pin.Ref == "" {
		return workflow
	}
	return strings.ReplaceAll(workflow, "@"+pin.Ref, "@"+ref)
}

// Acceptance: a mutable channel alias is visible, and an exact version is not.
func TestParsePinDistinguishesMutableChannel(t *testing.T) {
	mutable := ParsePin([]byte(workflowYAML))
	if mutable.Ref != "v1" || !mutable.Mutable || mutable.Exact {
		t.Fatalf("mutable pin = %+v", mutable)
	}
	exact := ParsePin([]byte(replaceRef(workflowYAML, "v1.4.1")))
	if exact.Ref != "v1.4.1" || exact.Mutable || !exact.Exact {
		t.Fatalf("exact pin = %+v", exact)
	}
	if empty := ParsePin([]byte("name: ci\n")); empty.Ref != "" {
		t.Fatalf("unrelated workflow produced a pin: %+v", empty)
	}
}

// Acceptance: a new version goes to the canary first; the fleet stays blocked
// until the canary has actually passed on that version.
func TestCanaryGatesFleetRollout(t *testing.T) {
	entries := []Entry{
		entry("acme/app", "v1.4.0", true, fleet.ClassificationManaged),
		entry("acme/lib", "v1.4.0", false, fleet.ClassificationManaged),
		entry("acme/deploy", "v1.4.0", false, fleet.ClassificationManaged),
	}

	blocked := BuildPlan(testManifest(t), entries, "v1.4.1", "acme/app", CanaryEvidence{Reason: "canary is pinned to v1.4.0"}, Compatibility{Version: "v1.4.1"})
	if len(blocked.Blocked) != 2 {
		t.Fatalf("blocked = %v, want the two non-canary repositories", blocked.Blocked)
	}
	if len(blocked.Ready) != 1 || blocked.Ready[0] != "acme/app" {
		t.Fatalf("ready = %v, want only the canary", blocked.Ready)
	}

	passed := BuildPlan(testManifest(t), entries, "v1.4.1", "acme/app", CanaryEvidence{Pinned: true, Lifecycle: true}, Compatibility{Version: "v1.4.1"})
	if len(passed.Ready) != 3 || len(passed.Blocked) != 0 {
		t.Fatalf("after canary pass: ready=%v blocked=%v", passed.Ready, passed.Blocked)
	}
}

// A canary that is pinned but whose lifecycle never succeeded does not unblock.
func TestCanaryPinnedWithoutSuccessfulLifecycleStaysBlocked(t *testing.T) {
	entries := []Entry{entry("acme/lib", "v1.4.0", false, fleet.ClassificationManaged)}
	plan := BuildPlan(testManifest(t), entries, "v1.4.1", "acme/app", CanaryEvidence{Pinned: true, Reason: "canary run concluded failure"}, Compatibility{Version: "v1.4.1"})
	if len(plan.Blocked) != 1 {
		t.Fatalf("blocked = %v, want the repository to stay blocked", plan.Blocked)
	}
}

// Acceptance: a repository already on the target is a NOOP, not a repeated change.
func TestAlreadyOnTargetIsCurrent(t *testing.T) {
	entries := []Entry{entry("acme/lib", "v1.4.1", false, fleet.ClassificationManaged)}
	plan := BuildPlan(testManifest(t), entries, "v1.4.1", "acme/app", CanaryEvidence{Pinned: true, Lifecycle: true}, Compatibility{Version: "v1.4.1"})
	if plan.Entries[0].Status != StatusCurrent {
		t.Fatalf("status = %s, want CURRENT", plan.Entries[0].Status)
	}
	if len(plan.Ready) != 0 {
		t.Fatalf("ready = %v, want nothing to do", plan.Ready)
	}
}

// Acceptance: a repository outside fleet.yaml is never rolled out.
func TestUndeclaredRepositoryIsNotRolledOut(t *testing.T) {
	entries := []Entry{{Repository: "acme/mystery", Classification: fleet.ClassificationDiscoveredUnmanaged, Target: "v1.4.1", Current: ParsePin([]byte(workflowYAML))}}
	plan := BuildPlan(testManifest(t), entries, "v1.4.1", "acme/app", CanaryEvidence{Pinned: true, Lifecycle: true}, Compatibility{Version: "v1.4.1"})
	if plan.Entries[0].Status != StatusUnmanaged || len(plan.Ready) != 0 {
		t.Fatalf("undeclared repository was rolled out: %+v", plan.Entries[0])
	}
}

func TestRepinOnlyRewritesTheReleaseGraphReference(t *testing.T) {
	source := `name: Release
jobs:
  a:
    uses: actions/checkout@v7
  b:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1
`
	next, err := Repin(source, "v1.4.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "reusable-release.yml@v1.4.1") {
		t.Fatalf("ref not updated:\n%s", next)
	}
	if !strings.Contains(next, "actions/checkout@v7") {
		t.Fatalf("unrelated action was rewritten:\n%s", next)
	}
	if !strings.Contains(next, "reusable-release.yml@v1.4.1") {
		t.Fatalf("version comment missing:\n%s", next)
	}
	if _, err := Repin("name: nothing\n", "v1.4.1"); err == nil {
		t.Fatal("a workflow without a ReleaseGraph call must be an error")
	}
}

// A commit pin always resolves for reusable workflows, so rollout writes the
// commit and keeps the version as a trailing comment for humans.
func TestRepinToCommitRecordsTheVersionAsAComment(t *testing.T) {
	next, err := RepinToCommit(workflowYAML, "v1.4.1", "949a267b48306e34b597e968269c492a31090bf2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(next, "reusable-release.yml@949a267b48306e34b597e968269c492a31090bf2 # ReleaseGraph v1.4.1") {
		t.Fatalf("commit pin not written:\n%s", next)
	}
	pin := ParsePin([]byte(next))
	if pin.Ref != "949a267b48306e34b597e968269c492a31090bf2" || pin.Mutable {
		t.Fatalf("parsed pin = %+v", pin)
	}
}

// Acceptance: a release that changes one capability must not ask unrelated
// repositories to upgrade.
func TestCapabilityAwareFiltering(t *testing.T) {
	compat := Compatibility{Version: "v1.5.0", AffectedCapabilities: []string{"binary"}}

	// A binary-producing repository is affected.
	status, reason := Affected([]string{"binary", "github-release", "checksums"}, compat, 1)
	if status != CompatUpgradeRecommended {
		t.Fatalf("binary repository = %s (%s), want affected", status, reason)
	}
	// A registry-only repository is not.
	status, reason = Affected([]string{"github-release", "npm"}, compat, 1)
	if status != CompatUnaffected {
		t.Fatalf("npm-only repository = %s (%s), want UNAFFECTED", status, reason)
	}
	// An unknown scope (no declared capabilities) is never skipped silently.
	status, reason = Affected([]string{}, Compatibility{Version: "v1.5.0"}, 1)
	if status != CompatUpgradeRecommended {
		t.Fatalf("undeclared scope = %s (%s), want conservative affected", status, reason)
	}
	// Raising the minimum policy schema requires migration, and an incompatible
	// release is never applied.
	status, _ = Affected([]string{"binary"}, Compatibility{MinimumPolicySchema: 2}, 1)
	if status != CompatMigrationRequired {
		t.Fatalf("schema raise = %s, want MIGRATION_REQUIRED", status)
	}
	status, _ = Affected([]string{"binary"}, Compatibility{MinimumPolicySchema: 3, Breaking: true}, 1)
	if status != CompatIncompatible {
		t.Fatalf("breaking schema jump = %s, want INCOMPATIBLE", status)
	}
}

func TestPlanSkipsUnaffectedRepositories(t *testing.T) {
	entries := []Entry{
		{Repository: "acme/app", Classification: fleet.ClassificationManaged, Target: "v1.5.0", PolicySchema: 1,
			Capabilities: []string{"binary"}, Current: ParsePin([]byte(workflowYAML))},
		{Repository: "acme/lib", Classification: fleet.ClassificationManaged, Target: "v1.5.0", PolicySchema: 1,
			Capabilities: []string{"npm"}, Current: ParsePin([]byte(workflowYAML))},
	}
	compat := Compatibility{Version: "v1.5.0", AffectedCapabilities: []string{"binary"}, MinimumPolicySchema: 1}
	plan := BuildPlan(testManifest(t), entries, "v1.5.0", "", CanaryEvidence{Pinned: true, Lifecycle: true}, compat)

	if len(plan.Unaffected) != 1 || plan.Unaffected[0] != "acme/lib" {
		t.Fatalf("unaffected = %v, want the npm-only repository", plan.Unaffected)
	}
	if len(plan.Ready) != 1 || plan.Ready[0] != "acme/app" {
		t.Fatalf("ready = %v, want only the binary repository", plan.Ready)
	}

	// Parse the published document shape.
	parsed, err := ParseCompatibility([]byte(`{"version":"v1.5.0","workflow_api":4,"policy_schema":1,"minimum_policy_schema":1,"breaking":false,"affected_capabilities":["binary"]}`))
	if err != nil || parsed.WorkflowAPI != 4 || len(parsed.AffectedCapabilities) != 1 {
		t.Fatalf("parsed compatibility = %+v (%v)", parsed, err)
	}
}
