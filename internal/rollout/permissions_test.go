package rollout

import (
	"reflect"
	"strings"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/fleet"
)

// The released reusable workflow as it stands after the 2026-09-26 incident:
// the finalize job needs actions: write and id-token: write, on top of the
// permissions the earlier jobs already asked for.
const reusableReleaseYAML = `name: Release
on:
  workflow_call:
jobs:
  release_please:
    permissions:
      contents: write
      pull-requests: write
    steps:
      - uses: actions/checkout@v7
  build-plan:
    permissions: {contents: read}
    steps:
      - run: echo plan
  finalize:
    permissions:
      contents: write
      packages: write
      pull-requests: write
      actions: write
      id-token: write
    steps:
      - run: echo finalize
`

// A caller that grants the subset the fleet had before the incident: every
// permission except actions: write.
const callerWithoutActions = `name: Release
on:
  push:
    branches: [master]
permissions:
  contents: write
  pull-requests: write
  issues: write
  packages: write
  id-token: write
jobs:
  release:
    if: github.event_name != 'pull_request'
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@abc123 # ReleaseGraph v1.5.14
    secrets: inherit
`

// The documented caller shape, with the permissions written inline on the job.
const callerDocExample = `name: Release
jobs:
  release:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1
    permissions: {contents: write, packages: write, pull-requests: write, id-token: write}
    secrets: inherit
`

func TestRequestedPermissionsUnionsEveryJobBlock(t *testing.T) {
	requested := RequestedPermissions([]byte(reusableReleaseYAML))
	want := PermissionGrant{
		"contents":      PermissionWrite,
		"pull-requests": PermissionWrite,
		"packages":      PermissionWrite,
		"actions":       PermissionWrite,
		"id-token":      PermissionWrite,
	}
	if !reflect.DeepEqual(requested, want) {
		t.Fatalf("requested = %v, want %v", requested, want)
	}
}

// Acceptance: a caller that does not grant what the target workflow requests is
// reported before the rollout repins it — the run would fail at startup with
// zero jobs, exactly as it did on 2026-09-26.
func TestPermissionGapReportsTheMissingScope(t *testing.T) {
	requested := RequestedPermissions([]byte(reusableReleaseYAML))

	missing := PermissionGaps([]byte(callerWithoutActions), requested)
	if !reflect.DeepEqual(missing, []string{"actions"}) {
		t.Fatalf("missing = %v, want [actions]", missing)
	}

	granted := []byte(strings.Replace(callerWithoutActions, "  packages: write", "  packages: write\n  actions: write", 1))
	if missing := PermissionGaps(granted, requested); len(missing) != 0 {
		t.Fatalf("a caller that grants every requested scope still reports %v", missing)
	}

	// The documented example was the shape that broke: it lists four scopes.
	if missing := PermissionGaps([]byte(callerDocExample), requested); !reflect.DeepEqual(missing, []string{"actions"}) {
		t.Fatalf("documented caller = %v, want [actions]", missing)
	}
}

// Acceptance: job-level permissions replace the workflow-level ones for that
// job, so a workflow-level grant must not hide a job-level restriction.
func TestJobLevelPermissionsReplaceWorkflowLevel(t *testing.T) {
	caller := `name: Release
permissions: write-all
jobs:
  release:
    permissions: {contents: read}
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1
    secrets: inherit
`
	granted, declared := GrantOf([]byte(caller))
	if !declared {
		t.Fatal("the caller declares permissions")
	}
	if granted["contents"] != PermissionRead {
		t.Fatalf("granted contents = %q, want read (the job block wins)", granted["contents"])
	}
	if missing := MissingPermissions(granted, PermissionGrant{"contents": PermissionWrite}); !reflect.DeepEqual(missing, []string{"contents"}) {
		t.Fatalf("missing = %v, want [contents]", missing)
	}
}

// Acceptance: a caller that declares nothing gets GitHub's default, which grants
// no write scope, so every write request is reported instead of skipped.
func TestCallerWithoutPermissionsIsCheckedAgainstTheDefault(t *testing.T) {
	caller := `name: Release
jobs:
  release:
    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1
    secrets: inherit
`
	if _, declared := GrantOf([]byte(caller)); declared {
		t.Fatal("no permissions block is declared")
	}
	want := []string{"actions", "contents", "id-token", "packages", "pull-requests"}
	if missing := PermissionGaps([]byte(caller), RequestedPermissions([]byte(reusableReleaseYAML))); !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v, want %v", missing, want)
	}
}

// A workflow that never calls ReleaseGraph cannot mismatch its permissions.
func TestCallerWithoutAReleaseCallDeclaresNothing(t *testing.T) {
	caller := `name: CI
permissions:
  contents: read
jobs:
  test:
    steps:
      - run: go test ./...
`
	if _, declared := GrantOf([]byte(caller)); declared {
		t.Fatal("a workflow without a release call declares no release permissions")
	}
	// read-all satisfies read requests only.
	granted, declared := GrantOf([]byte("name: Release\npermissions: read-all\njobs:\n  release:\n    uses: redtidev1918/releasegraph/.github/workflows/reusable-release.yml@v1\n"))
	if !declared || granted["contents"] != PermissionRead || granted["actions"] != PermissionRead {
		t.Fatalf("read-all grant = %v (declared=%v)", granted, declared)
	}
	if missing := MissingPermissions(granted, PermissionGrant{"contents": PermissionRead, "actions": PermissionWrite}); !reflect.DeepEqual(missing, []string{"actions"}) {
		t.Fatalf("missing = %v, want [actions]", missing)
	}
}

// Acceptance: a caller that cannot grant the target's permissions never becomes
// ready — not even when its pin already points at the target.
func TestPermissionGapBlocksTheRollout(t *testing.T) {
	pinned := entry("acme/app", "v1.4.1", false, fleet.ClassificationManaged)
	pinned.MissingPermissions = []string{"actions"}
	plan := BuildPlan(testManifest(t), []Entry{pinned}, "v1.4.1", "", CanaryEvidence{}, Compatibility{Version: "v1.4.1"})

	if plan.Entries[0].Status != StatusPermission {
		t.Fatalf("status = %s, want PERMISSION_REQUIRED", plan.Entries[0].Status)
	}
	if !reflect.DeepEqual(plan.PermissionRequired, []string{"acme/app"}) {
		t.Fatalf("permissionRequired = %v", plan.PermissionRequired)
	}
	if len(plan.Ready) != 0 || len(plan.Blocked) != 0 {
		t.Fatalf("ready=%v blocked=%v, want the repository in neither batch", plan.Ready, plan.Blocked)
	}
	if !strings.Contains(plan.Entries[0].Reason, "actions") {
		t.Fatalf("reason does not name the missing scope: %q", plan.Entries[0].Reason)
	}
}
