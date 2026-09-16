package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// prLifecyclePolicy declares the contract the way a repository would.
const prLifecyclePolicy = `kind: binary
versioning:
  mode: release-please
assets:
  required:
    - app.zip
registries:
  github:
    required: true
repository:
  pullRequests:
    lifecycle:
      parkedAfterDays: 7
      exempt:
        actors:
          - github-actions[bot]
`

// requestLog records what a fake provider was asked, in order. The order is the
// subject of the safety assertions below, so it is recorded rather than just
// counted.
type requestLog struct {
	mu       sync.Mutex
	requests []string
}

func (l *requestLog) add(method, path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, method+" "+path)
}

func (l *requestLog) indexOf(entry string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, recorded := range l.requests {
		if recorded == entry {
			return i
		}
	}
	return -1
}

func (l *requestLog) countMatching(method string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := 0
	for _, recorded := range l.requests {
		if strings.HasPrefix(recorded, method+" ") {
			total++
		}
	}
	return total
}

func prLifecycleJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// openPullRequest renders one open pull request the way the provider does.
func openPullRequest(number int, head, author, mergeable string, draft bool, updatedAt time.Time, labels ...string) map[string]any {
	labelList := []map[string]string{}
	for _, label := range labels {
		labelList = append(labelList, map[string]string{"name": label})
	}
	return map[string]any{
		"number":          number,
		"title":           fmt.Sprintf("pull request %d", number),
		"draft":           draft,
		"user":            map[string]string{"login": author},
		"labels":          labelList,
		"head":            map[string]string{"ref": head, "sha": "sha-" + head},
		"base":            map[string]string{"ref": "main"},
		"created_at":      updatedAt.Add(-time.Hour).Format(time.RFC3339),
		"updated_at":      updatedAt.Format(time.RFC3339),
		"mergeable_state": mergeable,
	}
}

func TestPRLifecycleAuditReportsTheQueueAndWritesTheDashboardSidecar(t *testing.T) {
	log := &requestLog{}
	stale := time.Now().UTC().Add(-10 * 24 * time.Hour)

	// Three pull requests, one per interesting verdict:
	//   #11 green and stale  -> MERGE_READY (a human must merge it)
	//   #12 already landed   -> OBSOLETE    (leaves the queue)
	//   #13 release-please   -> RELEASE     (a publish queue, never touched)
	pulls := []map[string]any{
		openPullRequest(11, "feat/green", "alice", "clean", false, stale),
		openPullRequest(12, "feat/landed", "bob", "clean", false, stale),
		openPullRequest(13, "release-please--branches--main", "github-actions[bot]", "clean", false, stale),
	}
	compare := map[string]map[string]any{
		"sha-feat/green":  {"ahead_by": 2, "files": []map[string]string{{"filename": "a.go"}}},
		"sha-feat/landed": {"ahead_by": 0, "files": []map[string]string{}},
		"sha-release-please--branches--main": {"ahead_by": 1, "files": []map[string]string{
			{"filename": "CHANGELOG.md"},
		}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method, r.URL.Path)
		switch {
		case r.URL.Path == "/repos/acme/app/contents/.release-policy.yml":
			prLifecycleJSON(t, w, map[string]string{
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString([]byte(prLifecyclePolicy)),
			})
		case r.URL.Path == "/repos/acme/app/pulls":
			prLifecycleJSON(t, w, pulls)
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			prLifecycleJSON(t, w, map[string]any{
				"check_runs": []map[string]string{{"status": "completed", "conclusion": "success"}},
			})
		case strings.Contains(r.URL.Path, "/compare/"):
			head := r.URL.Path[strings.Index(r.URL.Path, "...")+len("..."):]
			result, ok := compare[head]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			prLifecycleJSON(t, w, result)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	prLifecycleSetEnv(t, server.URL, "acme/app")
	reportPath := filepath.Join(t.TempDir(), "pr-lifecycle.json")
	stdout, err := captureRun(t, []string{"pr-lifecycle", "audit", "--repo", "acme/app", "--format", "json", "--report", reportPath})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}

	var report struct {
		SchemaVersion int    `json:"schema_version"`
		GeneratedAt   string `json:"generated_at"`
		Mode          string `json:"mode"`
		Scope         string `json:"scope"`
		Repositories  []struct {
			Repository  string         `json:"repository"`
			Status      string         `json:"status"`
			Enabled     bool           `json:"enabled"`
			Open        int            `json:"open"`
			InQueue     int            `json:"inQueue"`
			ShouldLeave int            `json:"shouldLeave"`
			Counts      map[string]int `json:"counts"`
			Results     []struct {
				Number     int    `json:"number"`
				State      string `json:"state"`
				KeepBranch bool   `json:"keepBranch"`
			} `json:"results"`
		} `json:"repositories"`
		Open        int `json:"open"`
		InQueue     int `json:"inQueue"`
		ShouldLeave int `json:"shouldLeave"`
	}
	sidecar, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if err := json.Unmarshal(sidecar, &report); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}

	if report.SchemaVersion != 1 {
		t.Errorf("schema_version=%d, want 1 (the dashboard keys on this)", report.SchemaVersion)
	}
	if report.Mode != "audit" || report.Scope != "repository" {
		t.Errorf("mode=%q scope=%q, want audit/repository", report.Mode, report.Scope)
	}
	if report.GeneratedAt == "" {
		t.Error("generated_at is empty; a consumer cannot tell how fresh the report is")
	}
	if len(report.Repositories) != 1 {
		t.Fatalf("repositories=%d, want 1", len(report.Repositories))
	}
	entry := report.Repositories[0]
	if entry.Repository != "acme/app" || entry.Status != "evaluated" || !entry.Enabled {
		t.Errorf("entry=%+v, want evaluated acme/app with the contract enabled", entry)
	}
	if entry.Open != 3 || entry.InQueue != 2 || entry.ShouldLeave != 1 {
		t.Errorf("open=%d inQueue=%d shouldLeave=%d, want 3/2/1", entry.Open, entry.InQueue, entry.ShouldLeave)
	}
	if entry.Counts["MERGE_READY"] != 1 || entry.Counts["OBSOLETE"] != 1 || entry.Counts["RELEASE"] != 1 {
		t.Errorf("counts=%v, want one MERGE_READY, OBSOLETE and RELEASE", entry.Counts)
	}
	if report.Open != 3 || report.InQueue != 2 || report.ShouldLeave != 1 {
		t.Errorf("totals=%d/%d/%d, want 3/2/1", report.Open, report.InQueue, report.ShouldLeave)
	}
	for _, result := range entry.Results {
		if !result.KeepBranch {
			t.Errorf("pull request %d does not report keepBranch; a lease of the work must always be visible", result.Number)
		}
	}

	// The dashboard reader (release_infra.inventory) keys on these exact names.
	// A rename here would degrade the PR Lifecycle column to "—" without
	// failing anything anywhere, so the producer pins them.
	var raw struct {
		Repositories []map[string]any `json:"repositories"`
	}
	if err := json.Unmarshal(sidecar, &raw); err != nil {
		t.Fatalf("decode sidecar keys: %v", err)
	}
	for _, required := range []string{"enabled", "inQueue", "open", "repository", "shouldLeave", "status"} {
		if _, ok := raw.Repositories[0][required]; !ok {
			t.Errorf("the sidecar entry is missing %q, which the dashboard reads", required)
		}
	}

	// The sidecar is the same document that was printed, not a second
	// rendering of the verdicts: the dashboard and the terminal cannot drift.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode stdout envelope: %v", err)
	}
	var printed, written any
	if err := json.Unmarshal(envelope.Data, &printed); err != nil {
		t.Fatalf("decode printed report: %v", err)
	}
	if err := json.Unmarshal(sidecar, &written); err != nil {
		t.Fatalf("decode written report: %v", err)
	}
	if !reflect.DeepEqual(printed, written) {
		t.Error("the sidecar differs from the printed report")
	}
}

func TestPRLifecycleApplyArchivesBeforeItCloses(t *testing.T) {
	log := &requestLog{}
	var commentBody string
	paused := time.Now().UTC().Add(-30 * 24 * time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method, r.URL.Path)
		switch {
		case r.URL.Path == "/repos/acme/app/contents/.release-policy.yml":
			prLifecycleJSON(t, w, map[string]string{
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString([]byte(prLifecyclePolicy)),
			})
		case r.URL.Path == "/repos/acme/app/pulls":
			// A draft that has not moved in 30 days: PARKED.
			prLifecycleJSON(t, w, []map[string]any{
				openPullRequest(21, "feat/paused", "carol", "draft", true, paused),
			})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			prLifecycleJSON(t, w, map[string]any{
				"check_runs": []map[string]string{{"status": "completed", "conclusion": "success"}},
			})
		case strings.Contains(r.URL.Path, "/compare/"):
			prLifecycleJSON(t, w, map[string]any{
				"ahead_by": 3,
				"files":    []map[string]string{{"filename": "a.go"}, {"filename": "b.go"}},
			})
		case r.URL.Path == "/repos/acme/app/issues" && r.Method == http.MethodGet:
			// The entries are deliberately adversarial: the marker belongs to
			// #21, but the entry is really a pull request, so it must not be
			// reused as the archive for it.
			prLifecycleJSON(t, w, []map[string]any{
				{
					"number":       5,
					"title":        "not really an issue",
					"body":         "<!-- releasegraph:parked-pr:21 -->",
					"state":        "open",
					"pull_request": map[string]string{"url": "https://api.github.com/repos/acme/app/pulls/5"},
				},
			})
		case r.URL.Path == "/repos/acme/app/issues" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			prLifecycleJSON(t, w, map[string]any{"number": 7})
		case r.URL.Path == "/repos/acme/app/issues/21/comments":
			var payload struct {
				Body string `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode comment: %v", err)
			}
			commentBody = payload.Body
			w.WriteHeader(http.StatusCreated)
			prLifecycleJSON(t, w, map[string]any{"id": 1})
		case r.URL.Path == "/repos/acme/app/pulls/21":
			prLifecycleJSON(t, w, map[string]any{"number": 21, "state": "closed"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	prLifecycleSetEnv(t, server.URL, "acme/app")
	stdout, err := captureRun(t, []string{"pr-lifecycle", "apply", "--repo", "acme/app", "--format", "json"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	create := log.indexOf("POST /repos/acme/app/issues")
	comment := log.indexOf("POST /repos/acme/app/issues/21/comments")
	close := log.indexOf("PATCH /repos/acme/app/pulls/21")
	if create < 0 || comment < 0 || close < 0 {
		t.Fatalf("expected an archive, a comment and a close; got %v", log.requests)
	}
	if !(create < comment && comment < close) {
		t.Errorf("archive must precede the comment and the close; got %v", log.requests)
	}
	// The reused-issue path would have skipped the archive entirely.
	if log.countMatching("POST") != 2 {
		t.Errorf("expected exactly one issue and one comment to be created; got %v", log.requests)
	}
	if log.countMatching("DELETE") != 0 {
		t.Errorf("the contract must never DELETE; got %v", log.requests)
	}
	if !strings.Contains(commentBody, "#7") {
		t.Errorf("the archive comment does not reference the created issue: %q", commentBody)
	}
	// The comment is posted on the pull request itself, so it names the branch
	// and the verdict rather than repeating the number.
	for _, want := range []string{"PARKED", "feat/paused", "kept"} {
		if !strings.Contains(commentBody, want) {
			t.Errorf("comment=%q, want it to mention %q", commentBody, want)
		}
	}

	var envelope struct {
		Data struct {
			Applied      int `json:"applied"`
			Repositories []struct {
				ShouldLeave int      `json:"shouldLeave"`
				Applied     []string `json:"applied"`
			} `json:"repositories"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	// `applied` counts actions, not pull requests: parking one pull request is
	// an archive, a comment and a close. `shouldLeave` is the pull request count.
	if envelope.Data.Applied != 3 {
		t.Errorf("applied=%d, want 3 (archive + comment + close)", envelope.Data.Applied)
	}
	applied := strings.Join(envelope.Data.Repositories[0].Applied, "; ")
	for _, want := range []string{"issue #7 archives pull request #21", "closed pull request #21 (branch kept)"} {
		if !strings.Contains(applied, want) {
			t.Errorf("applied=%q, want it to contain %q", applied, want)
		}
	}
}

func TestPRLifecycleApplyLimitLeavesTheRestInTheQueue(t *testing.T) {
	log := &requestLog{}
	stale := time.Now().UTC().Add(-40 * 24 * time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method, r.URL.Path)
		switch {
		case r.URL.Path == "/repos/acme/app/contents/.release-policy.yml":
			prLifecycleJSON(t, w, map[string]string{
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString([]byte(prLifecyclePolicy)),
			})
		case r.URL.Path == "/repos/acme/app/pulls":
			// Both already landed, so both would leave the queue unchecked.
			prLifecycleJSON(t, w, []map[string]any{
				openPullRequest(31, "feat/one", "alice", "clean", false, stale),
				openPullRequest(32, "feat/two", "bob", "clean", false, stale),
			})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			prLifecycleJSON(t, w, map[string]any{
				"check_runs": []map[string]string{{"status": "completed", "conclusion": "success"}},
			})
		case strings.Contains(r.URL.Path, "/compare/"):
			prLifecycleJSON(t, w, map[string]any{"ahead_by": 0, "files": []map[string]string{}})
		case r.URL.Path == "/repos/acme/app/issues" && r.Method == http.MethodGet:
			prLifecycleJSON(t, w, []map[string]any{})
		case r.URL.Path == "/repos/acme/app/issues/31/comments" || r.URL.Path == "/repos/acme/app/pulls/31":
			prLifecycleJSON(t, w, map[string]any{"id": 1})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	prLifecycleSetEnv(t, server.URL, "acme/app")
	stdout, err := captureRun(t, []string{"pr-lifecycle", "apply", "--repo", "acme/app", "--limit", "1", "--format", "json"})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	var envelope struct {
		Data struct {
			ShouldLeave  int `json:"shouldLeave"`
			Applied      int `json:"applied"`
			Repositories []struct {
				Note    string `json:"note"`
				Results []struct {
					Number     int    `json:"number"`
					Actionable bool   `json:"actionable"`
					Reason     string `json:"reason"`
				} `json:"results"`
			} `json:"repositories"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode stdout: %v", err)
	}
	// One pull request leaves the queue (so two actions: comment and close),
	// and the other is reported as still in it.
	if envelope.Data.ShouldLeave != 1 || envelope.Data.Applied != 2 {
		t.Errorf("shouldLeave=%d applied=%d, want 1/2", envelope.Data.ShouldLeave, envelope.Data.Applied)
	}
	capped := 0
	for _, result := range envelope.Data.Repositories[0].Results {
		if !result.Actionable && strings.Contains(result.Reason, "--limit") {
			capped++
		}
	}
	if capped != 1 {
		t.Errorf("capped pull requests=%d, want 1 reported as left in the queue", capped)
	}
	if log.countMatching("PATCH") != 1 {
		t.Errorf("exactly one pull request should have been closed; got %v", log.requests)
	}
}

// TestPRLifecycleFleetApplyRefusesWithoutAFleetCredential also pins that the
// refusal happens before any request: a fleet-wide mutation must not be
// discovered halfway through as a 403.
func TestPRLifecycleFleetApplyRefusesWithoutAFleetCredential(t *testing.T) {
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	prLifecycleSetEnv(t, server.URL, "acme/app")
	t.Setenv("RELEASEGRAPH_FLEET_TOKEN", "")

	_, err := captureRun(t, []string{"pr-lifecycle", "apply", "--scope", "fleet", "--repo", "acme/app"})
	if err == nil {
		t.Fatal("a fleet-wide apply without a fleet credential must fail")
	}
	if !strings.Contains(err.Error(), "RELEASEGRAPH_FLEET_TOKEN") {
		t.Errorf("error=%q, want it to name the missing credential", err.Error())
	}
	if total := log.countMatching("GET") + log.countMatching("POST") + log.countMatching("PATCH"); total != 0 {
		t.Errorf("the refusal must precede every request; got %v", log.requests)
	}
}

func TestPRLifecycleAuditNeverMutates(t *testing.T) {
	// `audit` must produce no mutating verb at all, on a pull request that a
	// contract-enabled repository would otherwise park.
	log := &requestLog{}
	paused := time.Now().UTC().Add(-30 * 24 * time.Hour)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r.Method, r.URL.Path)
		switch {
		case r.URL.Path == "/repos/acme/app/contents/.release-policy.yml":
			prLifecycleJSON(t, w, map[string]string{
				"encoding": "base64",
				"content":  base64.StdEncoding.EncodeToString([]byte(prLifecyclePolicy)),
			})
		case r.URL.Path == "/repos/acme/app/pulls":
			prLifecycleJSON(t, w, []map[string]any{
				openPullRequest(41, "feat/paused", "carol", "draft", true, paused),
			})
		case strings.HasSuffix(r.URL.Path, "/check-runs"):
			prLifecycleJSON(t, w, map[string]any{
				"check_runs": []map[string]string{{"status": "completed", "conclusion": "success"}},
			})
		case strings.Contains(r.URL.Path, "/compare/"):
			prLifecycleJSON(t, w, map[string]any{"ahead_by": 2, "files": []map[string]string{{"filename": "a.go"}}})
		default:
			t.Errorf("audit made an unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	prLifecycleSetEnv(t, server.URL, "acme/app")
	stdout, err := captureRun(t, []string{"pr-lifecycle", "audit", "--repo", "acme/app", "--format", "json"})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if log.countMatching("POST") != 0 || log.countMatching("PATCH") != 0 || log.countMatching("DELETE") != 0 || log.countMatching("PUT") != 0 {
		t.Errorf("audit must be read-only; got %v", log.requests)
	}
	if !strings.Contains(stdout, "PARKED") || !strings.Contains(stdout, "\"shouldLeave\":1") {
		t.Errorf("expected the parked pull request to be reported as leaving the queue:\n%s", stdout)
	}
	// Reported, but the report is the only thing that happened.
	if !strings.Contains(stdout, "\"applied\":0") {
		t.Errorf("audit report claims work was applied:\n%s", stdout)
	}
}

func prLifecycleSetEnv(t *testing.T, apiURL, repository string) {
	t.Helper()
	t.Setenv("GITHUB_API_URL", apiURL)
	t.Setenv("GITHUB_REPOSITORY", repository)
	t.Setenv("GITHUB_TOKEN", "test-token")
	t.Setenv("GITHUB_ACTOR", "tester")
	t.Setenv("RELEASEGRAPH_FLEET_TOKEN", "")
}
