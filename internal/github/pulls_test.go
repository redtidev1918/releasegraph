package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenPullRequestsRequestsOpenPulls(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		if r.URL.Path != "/repos/acme/app/pulls" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[{"number":42,"title":"fix","draft":true,"user":{"login":"hezzn"},
			"labels":[{"name":"keep-open"}],
			"head":{"ref":"fix/x","sha":"abc123"},
			"base":{"ref":"master"},
			"created_at":"2026-09-01T00:00:00Z","updated_at":"2026-09-10T22:26:28Z",
			"mergeable_state":"clean"}]`)
	}))
	defer server.Close()

	pulls, err := NewForTest(server.URL).OpenPullRequests(context.Background(), "acme/app")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(pulls) != 1 {
		t.Fatalf("pulls=%+v", pulls)
	}
	pull := pulls[0]
	if pull.Number != 42 || pull.Head.Ref != "fix/x" || pull.Head.Sha != "abc123" || pull.Base.Ref != "master" {
		t.Fatalf("pull=%+v", pull)
	}
	if !pull.Draft || pull.User.Login != "hezzn" || len(pull.Labels) != 1 || pull.Labels[0].Name != "keep-open" {
		t.Fatalf("pull=%+v", pull)
	}
	if pull.UpdatedAt.IsZero() || pull.UpdatedAt.UTC().Format("2006-01-02") != "2026-09-10" {
		t.Fatalf("updatedAt=%v", pull.UpdatedAt)
	}
	if pull.MergeableState != "clean" {
		t.Fatalf("mergeableState=%q", pull.MergeableState)
	}
	// The activity window is measured from updated_at, so the request must ask
	// for the most recently updated open pull requests.
	for _, want := range []string{"state=open", "sort=updated", "direction=desc", "per_page=100"} {
		if !contains(query, want) {
			t.Errorf("query %q is missing %q", query, want)
		}
	}
}

func TestCompareCommitsDistinguishesEmptyDiffFromMissingComparison(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/app/compare/master...fix/x":
			_, _ = io.WriteString(w, `{"ahead_by":3,"behind_by":1,"files":[{"filename":"a.go","status":"modified"}]}`)
		case "/repos/acme/app/compare/master...fix/identical":
			// Commits differ, content does not: the change already landed.
			_, _ = io.WriteString(w, `{"ahead_by":2,"behind_by":0,"files":[]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client := NewForTest(server.URL)

	compare, found, err := client.CompareCommits(context.Background(), "acme/app", "master", "fix/x")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if compare.AheadBy != 3 || len(compare.Files) != 1 {
		t.Fatalf("compare=%+v", compare)
	}

	empty, found, err := client.CompareCommits(context.Background(), "acme/app", "master", "fix/identical")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if empty.AheadBy != 2 || len(empty.Files) != 0 {
		t.Fatalf("empty diff must keep its commit count: %+v", empty)
	}

	// A deleted branch must report "no evidence", never "already landed".
	if _, found, err := client.CompareCommits(context.Background(), "acme/app", "master", "gone"); err != nil || found {
		t.Fatalf("missing comparison found=%v err=%v", found, err)
	}
}

func TestHeadCheckState(t *testing.T) {
	// Each sha names the verdict its check runs must produce, so the case list
	// and the fake stay in one place.
	payloads := map[string]string{
		"pending": `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"in_progress"}]}`,
		"failing": `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"timed_out"}]}`,
		"success": `{"check_runs":[{"status":"completed","conclusion":"success"},{"status":"completed","conclusion":"skipped"}]}`,
		"none":    `{"check_runs":[]}`,
	}
	wants := map[string]string{
		"pending": CheckStatePending,
		"failing": CheckStateFailing,
		"success": CheckStatePassing,
		"none":    CheckStateNone,
	}
	const prefix, suffix = "/repos/acme/app/commits/", "/check-runs"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
			t.Errorf("path=%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sha := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), suffix)
		payload, ok := payloads[sha]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, payload)
	}))
	defer server.Close()
	client := NewForTest(server.URL)

	for sha, want := range wants {
		got, err := client.HeadCheckState(context.Background(), "acme/app", sha)
		if err != nil {
			t.Fatalf("%s: err=%v", sha, err)
		}
		if got != want {
			t.Errorf("%s: state=%s want=%s", sha, got, want)
		}
	}
}

func TestLifecycleMutationsUseTheNarrowestEndpoints(t *testing.T) {
	type call struct {
		method string
		path   string
		body   map[string]any
	}
	calls := []call{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		entry := call{method: r.Method, path: r.URL.Path}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &entry.body)
		}
		calls = append(calls, entry)
		if r.URL.Path == "/repos/acme/app/issues" {
			_, _ = io.WriteString(w, `{"number":56}`)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := NewForTest(server.URL)
	ctx := context.Background()

	number, err := client.CreateIssue(ctx, "acme/app", "Parked work", "body text", nil)
	if err != nil || number != 56 {
		t.Fatalf("number=%d err=%v", number, err)
	}
	if err := client.CommentOnIssue(ctx, "acme/app", 42, "explanation"); err != nil {
		t.Fatalf("err=%v", err)
	}
	if err := client.ClosePullRequest(ctx, "acme/app", 42); err != nil {
		t.Fatalf("err=%v", err)
	}

	if len(calls) != 3 {
		t.Fatalf("calls=%+v", calls)
	}
	if calls[0].method != http.MethodPost || calls[0].path != "/repos/acme/app/issues" {
		t.Fatalf("create issue call=%+v", calls[0])
	}
	if calls[0].body["title"] != "Parked work" || calls[0].body["body"] != "body text" {
		t.Fatalf("create issue body=%+v", calls[0].body)
	}
	if calls[1].path != "/repos/acme/app/issues/42/comments" || calls[1].body["body"] != "explanation" {
		t.Fatalf("comment call=%+v", calls[1])
	}
	// Closing is a state change on the pull request resource. There is no
	// DELETE anywhere in this client for the contract to reach for.
	if calls[2].method != http.MethodPatch || calls[2].path != "/repos/acme/app/pulls/42" {
		t.Fatalf("close call=%+v", calls[2])
	}
	if calls[2].body["state"] != "closed" {
		t.Fatalf("close body=%+v", calls[2].body)
	}
}

func TestOpenIssuesSkipsEntriesThatArePullRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/app/issues" {
			t.Errorf("path=%s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `[
			{"number":56,"title":"Archived","body":"<!-- releasegraph:parked-pr:42 -->","state":"open"},
			{"number":57,"title":"A pull request","body":"","state":"open","pull_request":{"url":"x"}}
		]`)
	}))
	defer server.Close()

	issues, err := NewForTest(server.URL).OpenIssues(context.Background(), "acme/app")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("issues=%+v", issues)
	}
	if issues[0].Body == "" || issues[0].Number != 56 {
		t.Fatalf("issue=%+v", issues[0])
	}
	if issues[1].PullRequest == nil {
		t.Fatal("an entry that is really a pull request must be distinguishable")
	}
}

func TestFailedMutationCarriesTheStatusCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"Resource not accessible by integration"}`)
	}))
	defer server.Close()
	client := NewForTest(server.URL)

	err := client.ClosePullRequest(context.Background(), "acme/app", 42)
	if err == nil {
		t.Fatal("a refused mutation must not report success")
	}
	if !contains(err.Error(), "403") || !contains(err.Error(), "Resource not accessible") {
		t.Fatalf("err=%v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
