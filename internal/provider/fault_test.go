package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/policy"
	"github.com/redtidev1918/releasegraph/internal/registry"
)

// faultServer injects failures into the provider API surface. Every scenario
// asserts the same invariant: an interruption plus a retry converges on the same
// version, never a new one, and never moves an existing tag.
type faultServer struct {
	// failTagRefs: number of leading tag lookups answered with 429.
	failTagRefs int32
	// failLabelPosts: number of leading label POSTs answered with 500.
	failLabelPosts int32
	// draft: the release exists but is a draft (crash after draft creation).
	draft bool
	// assets: asset names present on the release.
	assets []string
	// tagSHA: the commit the tag resolves to.
	tagSHA string
	// labels: current provider labels on the merged release PR.
	labels []string

	tagRefCalls  int32
	labelPosts   int32
	labelDeletes int32
	tagMoves     int32
}

func (f *faultServer) handler() http.Handler {
	mux := http.NewServeMux()
	const repo = "acme/app"

	mux.HandleFunc("/repos/"+repo+"/pulls", func(w http.ResponseWriter, r *http.Request) {
		labels := ""
		for _, l := range f.labels {
			labels += fmt.Sprintf(`{"name":%q},`, l)
		}
		fmt.Fprintf(w, `[{"number":30,"title":"chore(main): release 2.16.0","merged_at":"2026-09-10T08:00:00Z","merge_commit_sha":"good","labels":[%s]}]`,
			strings.TrimSuffix(labels, ","))
	})

	mux.HandleFunc("/repos/"+repo+"/git/ref/tags/v2.16.0", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&f.tagRefCalls, 1) <= f.failTagRefs {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		sha := f.tagSHA
		if sha == "" {
			sha = "good"
		}
		fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, sha)
	})

	mux.HandleFunc("/repos/"+repo+"/releases/tags/v2.16.0", func(w http.ResponseWriter, r *http.Request) {
		if f.draft {
			// Drafts are invisible to the by-tag endpoint.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if len(f.assets) == 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, releaseJSON(f.assets, false))
	})

	mux.HandleFunc("/repos/"+repo+"/releases", func(w http.ResponseWriter, r *http.Request) {
		if len(f.assets) == 0 && !f.draft {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, "[%s]", releaseJSON(f.assets, f.draft))
	})

	mux.HandleFunc("/repos/"+repo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if len(f.assets) == 0 || f.draft {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprint(w, `{"tag_name":"v2.16.0"}`)
	})

	mux.HandleFunc("/repos/"+repo+"/releases/assets/9", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"assets":["app-linux","app-macos"],"policy_hash":""}`)
	})
	mux.HandleFunc("/repos/"+repo+"/releases/assets/10", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "aaaa  app-linux\nbbbb  app-macos\n")
	})

	mux.HandleFunc("/repos/"+repo+"/issues/30/labels", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&f.labelPosts, 1) <= f.failLabelPosts {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/repos/"+repo+"/issues/30/labels/", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.labelDeletes, 1)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch || r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/git/refs") {
			atomic.AddInt32(&f.tagMoves, 1)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	return mux
}

// releaseJSON renders one release object; the by-tag endpoint returns an object
// and the list endpoint an array of them.
func releaseJSON(assets []string, draft bool) string {
	items := make([]string, 0, len(assets))
	for i, name := range assets {
		id := 100 + i
		switch name {
		case "RELEASE-METADATA.json":
			id = 9
		case "SHA256SUMS":
			id = 10
		}
		items = append(items, fmt.Sprintf(`{"id":%d,"name":%q,"size":10}`, id, name))
	}
	return fmt.Sprintf(`{"tag_name":"v2.16.0","draft":%t,"prerelease":false,"assets":[%s]}`, draft, strings.Join(items, ","))
}

func faultPolicy() *policy.Policy {
	return &policy.Policy{
		Kind:       "binary",
		Versioning: policy.Versioning{Mode: "release-please"},
		Assets:     policy.Assets{Required: []string{"app-linux", "app-macos"}},
		Registries: map[string]policy.Registry{"github": {Required: true}},
		Checksums:  true,
	}
}

func observe(t *testing.T, f *faultServer) *Report {
	t.Helper()
	server := httptest.NewServer(f.handler())
	t.Cleanup(server.Close)
	client := github.NewForTest(server.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet})
	report, err := Inspect(context.Background(), client, registry.New(), faultPolicy(), "acme/app", "2.16.0")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	return report
}

// A transient rate limit on the tag lookup must not be misread as "tag missing",
// which would have produced a bogus repair of an already-correct release.
func TestFaultRateLimitedTagLookupConverges(t *testing.T) {
	f := &faultServer{
		failTagRefs: 2,
		assets:      []string{"app-linux", "app-macos", "RELEASE-METADATA.json", "SHA256SUMS"},
		labels:      []string{labelTagged},
	}
	report := observe(t, f)
	if report.Observed.Actual.TagCommit != "good" {
		t.Fatalf("tag commit = %q, want the real commit after retries", report.Observed.Actual.TagCommit)
	}
	if report.Verdict.Health != domain.HealthHealthy {
		t.Fatalf("verdict = %+v, want HEALTHY after transient retries", report.Verdict)
	}
	if atomic.LoadInt32(&f.tagMoves) != 0 {
		t.Fatal("a transient failure caused a tag mutation")
	}
}

// Crash after the tag but before the release: recoverable, same version, and the
// engine must never propose creating a new version.
func TestFaultCrashAfterTagIsRecoverable(t *testing.T) {
	f := &faultServer{assets: nil, draft: false, labels: []string{labelPending}}
	report := observe(t, f)
	if report.Verdict.Drift != DriftReleaseMissing || !report.Verdict.RepairSameVersion {
		t.Fatalf("verdict = %+v, want RELEASE_MISSING with same-version repair", report.Verdict)
	}
	allowed, _, inputs := RepairPlan(report)
	if !allowed || inputs["version"] != "2.16.0" {
		t.Fatalf("repair plan = %v %v, want the same version", allowed, inputs)
	}
	if report.Verdict.ACKAllowed {
		t.Fatal("an incomplete transaction must never be acknowledged")
	}
}

// Crash after the draft release with only part of the assets uploaded.
func TestFaultPartialAssetUploadStaysIncomplete(t *testing.T) {
	f := &faultServer{draft: false, assets: []string{"app-linux", "RELEASE-METADATA.json", "SHA256SUMS"}, labels: []string{labelPending}}
	report := observe(t, f)
	if report.Observed.Actual.AssetsComplete {
		t.Fatal("partial upload reported as complete")
	}
	if report.Verdict.Health != domain.HealthRecoverable || !report.Verdict.RepairSameVersion {
		t.Fatalf("verdict = %+v, want RECOVERABLE same-version repair", report.Verdict)
	}
	if report.Verdict.ACKAllowed {
		t.Fatal("partial upload must never be acknowledged")
	}
}

// A crash mid-draft leaves a draft: the transaction is in flight, and repair or
// ACK would race whatever is still running.
func TestFaultCrashMidDraftDoesNotTriggerRepair(t *testing.T) {
	f := &faultServer{draft: true, assets: []string{"app-linux"}, labels: []string{labelPending}}
	report := observe(t, f)
	if report.Verdict.Drift != DriftReleaseInProgress || report.Verdict.Health != domain.HealthRunning {
		t.Fatalf("verdict = %+v, want RELEASE_IN_PROGRESS", report.Verdict)
	}
	if report.Verdict.RepairSameVersion || report.Verdict.ACKAllowed {
		t.Fatal("an in-flight draft must not be repaired or acknowledged")
	}
}

// A duplicated reconciliation must not repeat mutations once acknowledged.
func TestFaultDuplicateReconcileIsIdempotent(t *testing.T) {
	f := &faultServer{
		assets: []string{"app-linux", "app-macos", "RELEASE-METADATA.json", "SHA256SUMS"},
		labels: []string{labelPending},
	}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	client := github.NewForTest(server.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet})

	first := observe(t, f)
	if _, err := Acknowledge(context.Background(), client, first, false); err != nil {
		t.Fatalf("first ACK: %v", err)
	}
	posts := atomic.LoadInt32(&f.labelPosts)
	if posts == 0 {
		t.Fatal("first ACK performed no mutation")
	}

	// The provider is now tagged: a second pass plans nothing.
	f.labels = []string{labelTagged}
	second, err := Inspect(context.Background(), client, registry.New(), faultPolicy(), "acme/app", "2.16.0")
	if err != nil {
		t.Fatal(err)
	}
	if second.Verdict.ACKAllowed {
		t.Fatal("second reconciliation still wants to ACK")
	}
	if _, err := Acknowledge(context.Background(), client, second, false); err == nil {
		t.Fatal("second reconciliation mutated again")
	}
	if atomic.LoadInt32(&f.labelPosts) != posts {
		t.Fatal("duplicate reconciliation issued another mutation")
	}
}

// A provider API failure must not be treated as a reason to create a version:
// the retry converges on the same version once the API recovers.
func TestFaultProviderAPIFailureRecoversOnRetry(t *testing.T) {
	f := &faultServer{
		failLabelPosts: 3,
		assets:         []string{"app-linux", "app-macos", "RELEASE-METADATA.json", "SHA256SUMS"},
		labels:         []string{labelPending},
	}
	server := httptest.NewServer(f.handler())
	defer server.Close()
	client := github.NewForTest(server.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet})

	report := observe(t, f)
	if _, err := Acknowledge(context.Background(), client, report, false); err == nil {
		t.Fatal("failing label API reported success")
	}
	if atomic.LoadInt32(&f.tagMoves) != 0 {
		t.Fatal("failure caused a tag mutation")
	}

	f.failLabelPosts = 0
	retry, err := Inspect(context.Background(), client, registry.New(), faultPolicy(), "acme/app", "2.16.0")
	if err != nil {
		t.Fatal(err)
	}
	if retry.Context.Version != report.Context.Version {
		t.Fatalf("retry targeted a different version: %s -> %s", report.Context.Version, retry.Context.Version)
	}
	if _, err := Acknowledge(context.Background(), client, retry, false); err != nil {
		t.Fatalf("retry ACK: %v", err)
	}
}
