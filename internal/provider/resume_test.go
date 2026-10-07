package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
)

func TestResumeDraftPreservesOriginalSourceAndRefusesUnsafeStates(t *testing.T) {
	for _, name := range []string{"interrupted", "active", "no failure", "tag changed", "conflict", "waived", "published", "missing tag", "unknown expected commit"} {
		t.Run(name, func(t *testing.T) {
			a := Actual{TagExists: true, TagCommit: "original", ExpectedCommit: "original", ReleaseDraft: true}
			run := github.WorkflowRun{ID: 1, Status: "completed", Conclusion: "failure", Event: "push", HeadSHA: "original"}
			tag := "original"
			switch name {
			case "active":
				run.Status = "in_progress"
			case "no failure":
				run.Conclusion = "success"
			case "tag changed":
				tag = "different"
			case "conflict":
				a.ExpectedCommit = "different"
			case "waived":
				a.Waived = true
			case "published":
				a.ReleaseDraft, a.ReleaseExists = false, true
			case "missing tag":
				a.TagExists = false
			case "unknown expected commit":
				a.ExpectedCommit = ""
			}
			dispatches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch req.URL.Path {
				case "/repos/" + acme + "/git/ref/tags/v2.16.0":
					fmt.Fprintf(w, `{"object":{"type":"commit","sha":%q}}`, tag)
				case "/repos/" + acme + "/actions/workflows/release.yml/runs":
					json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []github.WorkflowRun{run}})
				case "/repos/" + acme:
					fmt.Fprint(w, `{"default_branch":"main"}`)
				case "/repos/" + acme + "/actions/workflows/release.yml/dispatches":
					dispatches++
					var payload struct {
						Ref    string            `json:"ref"`
						Inputs map[string]string `json:"inputs"`
					}
					json.NewDecoder(req.Body).Decode(&payload)
					if payload.Ref != "main" || payload.Inputs["source_ref"] != "original" || payload.Inputs["version"] != "2.16.0" {
						t.Errorf("dispatch = %+v", payload)
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request %s %s", req.Method, req.URL)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			client := github.NewForTest(server.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet})
			o := obs(StatePending)
			o.Actual = a
			r := &Report{Context: Context{Repository: acme, Version: "2.16.0", Tag: "v2.16.0"}, Observed: o, Verdict: Classify(o)}
			_, err := ResumeDraft(context.Background(), client, r, "", true)
			if dispatches != 0 {
				t.Fatal("dry run mutated state")
			}
			if name != "interrupted" {
				if err == nil {
					t.Fatal("unsafe recovery accepted")
				}
				_, err = ResumeDraft(context.Background(), client, r, "", false)
				if err == nil || dispatches != 0 {
					t.Fatal("unsafe apply dispatched")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = ResumeDraft(context.Background(), client, r, "", false)
			if err != nil || dispatches != 1 {
				t.Fatalf("apply = %v, dispatches = %d", err, dispatches)
			}
		})
	}
}
