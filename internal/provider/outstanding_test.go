package provider

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/registry"
)

func githubBoundForTest(t *testing.T, f *fakeGitHub) *github.Bound {
	t.Helper()
	server := httptest.NewServer(f.handler())
	t.Cleanup(server.Close)
	return github.NewForTest(server.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet})
}

func TestOutstandingReleasePRsResolvesFromTitle(t *testing.T) {
	f := &fakeGitHub{prLabels: []string{"autorelease: pending"}}
	client := githubBoundForTest(t, f)

	outstanding, err := OutstandingReleasePRs(context.Background(), client, testPolicy(), acme)
	if err != nil {
		t.Fatalf("OutstandingReleasePRs: %v", err)
	}
	if len(outstanding) != 1 {
		t.Fatalf("expected 1 outstanding PR, got %d", len(outstanding))
	}
	if outstanding[0].Number != 30 || outstanding[0].Version != "2.16.0" || outstanding[0].Source != EvidenceTitle {
		t.Fatalf("unexpected resolution: %+v", outstanding[0])
	}
}

func TestOutstandingReleasePRsFallsBackToManifest(t *testing.T) {
	f := &fakeGitHub{
		prLabels: []string{"autorelease: tagged"},
		extraPRs: []string{
			`{"number":21,"title":"chore(release): bump version to 2.14.0","state":"closed","merged_at":"2026-09-01T08:00:00Z","merge_commit_sha":"m21","labels":[{"name":"autorelease: pending"}]}`,
		},
		commitsBySHA: map[string]string{"m21": "p21"},
		manifestByRef: map[string]string{
			"m21": `{".": "2.14.0"}`,
			"p21": `{".": "2.13.0"}`,
		},
	}
	client := githubBoundForTest(t, f)

	outstanding, err := OutstandingReleasePRs(context.Background(), client, testPolicy(), acme)
	if err != nil {
		t.Fatalf("OutstandingReleasePRs: %v", err)
	}
	if len(outstanding) != 1 {
		t.Fatalf("expected 1 outstanding PR, got %d: %+v", len(outstanding), outstanding)
	}
	if outstanding[0].Number != 21 || outstanding[0].Version != "2.14.0" || outstanding[0].Source != EvidenceManifest {
		t.Fatalf("unexpected resolution: %+v", outstanding[0])
	}
}

func TestScanOutstandingSkipsKnownAndSurfacesUnresolvable(t *testing.T) {
	f := &fakeGitHub{
		prLabels: []string{"autorelease: pending"},
		extraPRs: []string{
			`{"number":22,"title":"chore: dependency housekeeping","state":"closed","merged_at":"2026-08-01T08:00:00Z","merge_commit_sha":"m22","labels":[{"name":"autorelease: pending"}]}`,
		},
	}
	client := githubBoundForTest(t, f)

	reports, errs := ScanOutstanding(context.Background(), client, registry.New(), testPolicy(), acme, map[string]bool{"2.16.0": true})
	if len(reports) != 0 {
		t.Fatalf("expected no reports, got %+v", reports)
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "PR #22") {
		t.Fatalf("expected the unresolvable PR to surface as an error, got %v", errs)
	}
}

func TestAcknowledgeClearsWaivedStaleLabel(t *testing.T) {
	f := &fakeGitHub{
		extraPRs: []string{
			`{"number":20,"title":"chore(main): release 2.15.0","state":"closed","merged_at":"2026-09-01T08:00:00Z","merge_commit_sha":"m20","labels":[{"name":"autorelease: pending"},{"name":"releasegraph: historical-waived"}]}`,
		},
	}
	client := githubBoundForTest(t, f)

	reports, errs := ScanOutstanding(context.Background(), client, registry.New(), testPolicy(), acme, map[string]bool{"2.16.0": true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(reports) != 1 || !reports[0].Verdict.Waived {
		t.Fatalf("expected one waived report, got %+v", reports)
	}

	planned, err := Acknowledge(context.Background(), client, &reports[0], true)
	if err != nil {
		t.Fatalf("dry-run ACK refused: %v", err)
	}
	if len(planned) != 2 {
		t.Fatalf("expected add+remove mutations, got %+v", planned)
	}
	var addTagged, removePending bool
	for _, m := range planned {
		switch {
		case m.Action == "add" && m.Label == "autorelease: tagged":
			addTagged = true
		case m.Action == "remove" && m.Label == "autorelease: pending":
			removePending = true
		}
	}
	if !addTagged || !removePending {
		t.Fatalf("expected add tagged + remove pending, got %+v", planned)
	}

	if _, err := Acknowledge(context.Background(), client, &reports[0], false); err != nil {
		t.Fatalf("apply ACK refused: %v", err)
	}
	if len(f.mutations) != 2 {
		t.Fatalf("expected 2 label mutations, got %v", f.mutations)
	}
}

func TestAcknowledgeStillRefusesUnhealthyStaleVersion(t *testing.T) {
	f := &fakeGitHub{
		extraPRs: []string{
			`{"number":20,"title":"chore(main): release 2.15.0","state":"closed","merged_at":"2026-09-01T08:00:00Z","merge_commit_sha":"m20","labels":[{"name":"autorelease: pending"}]}`,
		},
	}
	client := githubBoundForTest(t, f)

	reports, errs := ScanOutstanding(context.Background(), client, registry.New(), testPolicy(), acme, map[string]bool{"2.16.0": true})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(reports) != 1 {
		t.Fatalf("expected one stale report, got %+v", reports)
	}
	if reports[0].Verdict.ACKAllowed || reports[0].Verdict.Waived {
		t.Fatalf("an incomplete stale version must not be acknowledged: %+v", reports[0].Verdict)
	}
	if _, err := Acknowledge(context.Background(), client, &reports[0], true); err == nil {
		t.Fatal("expected ACK refusal for an incomplete stale version")
	}
}
