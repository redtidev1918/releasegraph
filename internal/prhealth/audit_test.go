package prhealth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
)

func TestHealthStates(t *testing.T) {
	pass := Check{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS", IsRequired: true}
	fail := Check{Name: "test", Status: "COMPLETED", Conclusion: "FAILURE"}
	pending := Check{Name: "build", Status: "IN_PROGRESS"}
	for _, tc := range []struct {
		name     string
		checks   []Check
		required []string
		want     string
	}{
		{"skipped", []Check{{Status: "COMPLETED", Conclusion: "SKIPPED"}}, nil, "SKIPPED"},
		{"required skipped", []Check{pass, {Name: "build", Status: "COMPLETED", Conclusion: "SKIPPED", IsRequired: true}}, []string{"build"}, "SKIPPED"},
		{"optional skipped", []Check{pass, {Name: "release", Status: "COMPLETED", Conclusion: "SKIPPED"}}, []string{"test"}, "PASS"},
		{"neutral alone", []Check{{Status: "COMPLETED", Conclusion: "NEUTRAL"}}, nil, "SKIPPED"},
		{"green", []Check{pass}, []string{"test"}, "PASS"},
		{"failed", []Check{fail}, nil, "FAIL"},
		{"failed and running", []Check{pending, fail}, nil, "FAIL"},
		{"pending", []Check{pass, pending}, nil, "PENDING"},
		{"missing", nil, nil, "MISSING"},
		{"required absent", []Check{pass}, []string{"deploy"}, "MISSING"},
		{"unrecognized", []Check{{Status: "COMPLETED", Conclusion: "FUTURE"}}, nil, "UNKNOWN"},
		{"wrong app", []Check{{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"}}, []string{"test"}, "MISSING"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Evaluate(tc.checks, tc.required); got != tc.want {
				t.Fatalf("%s want %s", got, tc.want)
			}
		})
	}
}
func TestEvidenceClassification(t *testing.T) {
	for evidence, want := range map[string]string{
		"unsupported dependency major version": "DEPENDENCY_BREAKING", "error TS2345: incompatible": "TYPE_ERROR",
		"docsite drift": "CONFIG_DRIFT", "invalid workflow file": "CI_CONFIGURATION", "missing production secret": "SECRET_BOUNDARY",
		"contract mismatch": "CONTRACT_MISMATCH", "ECONNRESET": "INFRASTRUCTURE", "Bump express 4 to 5": "UNCLASSIFIED",
	} {
		if got := Classify(evidence); got != want {
			t.Errorf("%q: %s want %s", evidence, got, want)
		}
	}
	_, summary := classifyEvidence("error TS2322 secret-value-never-persisted")
	if strings.Contains(summary, "secret-value") {
		t.Fatal("raw diagnostic leaked")
	}
}

// fixtureClient serves a two-PR repository. broken corrupts check pagination;
// guardedBranch makes the branch-protection GraphQL query fail with a
// permission error, as happens without the administration permission;
// guardedRules does the same for the REST rulesets endpoint.
func fixtureClient(t *testing.T, broken, guardedBranch, guardedRules bool) *github.Bound {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "outside") {
			t.Error("queried outside manifest")
		}
		if strings.Contains(r.URL.Path, "bad") {
			w.WriteHeader(504)
			return
		}
		switch {
		case r.URL.Path == "/graphql":
			var payload struct {
				Query     string
				Variables map[string]any
			}
			json.NewDecoder(r.Body).Decode(&payload)
			if payload.Variables["name"] == "bad" {
				w.WriteHeader(403)
				return
			}
			if strings.Contains(payload.Query, "OpenCount") {
				fmt.Fprint(w, `{"data":{"repository":{"pullRequests":{"totalCount":2}}}}`)
				return
			}
			if strings.Contains(payload.Query, "RequiredContexts") {
				if guardedBranch {
					w.WriteHeader(403)
					return
				}
				fmt.Fprint(w, `{"data":{"repository":{"ref":{"branchProtectionRule":{"requiredStatusCheckContexts":["test"]}}}}}`)
				return
			}
			n := int(payload.Variables["number"].(float64))
			total := 1
			if broken {
				total = 2
			}
			fmt.Fprintf(w, `{"data":{"repository":{"pullRequest":{"headRefOid":"sha","baseRefOid":"basesha","baseRefName":"main","mergeable":"MERGEABLE","commits":{"nodes":[{"commit":{"oid":"sha","statusCheckRollup":{"contexts":{"totalCount":%d,"pageInfo":{"hasNextPage":false,"endCursor":null},"nodes":[{"id":"check%d","name":"test","status":"COMPLETED","conclusion":"FAILURE","isRequired":true,"detailsUrl":"https://github.com/o/good/actions/runs/1"}]}}}}]}}}}}`, total, n)
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			fmt.Fprint(w, `[{"number":1,"title":"dep1","user":{"login":"dependabot[bot]"},"head":{"ref":"dep1","sha":"sha"},"base":{"ref":"main"}},{"number":2,"title":"dep2","user":{"login":"dependabot[bot]"},"head":{"ref":"dep2","sha":"sha"},"base":{"ref":"main"}}]`)
		case strings.Contains(r.URL.Path, "/rules/"):
			if guardedRules {
				w.WriteHeader(403)
				return
			}
			fmt.Fprint(w, `[]`)
		case strings.HasSuffix(r.URL.Path, "/actions/runs"):
			fmt.Fprint(w, `{"total_count":0,"workflow_runs":[]}`)
		case strings.Contains(r.URL.Path, "/contents/"):
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	return github.NewForTest(srv.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeFleet})
}
func TestScanIncludesDependabotAndIsolatesRepositories(t *testing.T) {
	disabled := false
	m := &fleet.Manifest{Version: 1, Repositories: []fleet.ManifestEntry{{Name: "o/bad"}, {Name: "o/good"}, {Name: "o/outside", Enabled: &disabled}}}
	r := Scan(context.Background(), fixtureClient(t, false, false, false), m)
	if r.Complete || r.Status != "DEGRADED" || len(r.Repositories) != 2 {
		t.Fatalf("%+v", r)
	}
	good := r.Repositories[1]
	if !good.Complete || len(good.Pulls) != 2 || good.Pulls[0].Status != "FAIL" || good.Pulls[1].Status != "FAIL" {
		t.Fatalf("%+v", good)
	}
	if good.Pulls[0].Author != "dependabot[bot]" {
		t.Fatal("dependabot omitted")
	}
	if good.Pulls[0].Required[0] != "test" {
		t.Fatalf("required contexts missing: %+v", good.Pulls[0].Required)
	}
	if len(r.Incidents) != 3 {
		t.Fatalf("%+v", r.Incidents)
	}
}
func TestIncompleteChecksNeverPass(t *testing.T) {
	r := Scan(context.Background(), fixtureClient(t, true, false, false), &fleet.Manifest{Version: 1, Repositories: []fleet.ManifestEntry{{Name: "o/good"}}})
	if r.Complete || r.Status != "DEGRADED" || r.Repositories[0].Pulls[0].Status != "UNKNOWN" {
		t.Fatalf("%+v", r)
	}
}

// A branch-protection permission failure must degrade the pull request to
// UNKNOWN without discarding the check evidence already collected.
func TestGuardedBranchProtectionKeepsCheckEvidence(t *testing.T) {
	for name, guarded := range map[string]struct{ branch, rules bool }{
		"graphql branch protection": {true, false},
		"rest rulesets":             {false, true},
	} {
		t.Run(name, func(t *testing.T) {
			r := Scan(context.Background(), fixtureClient(t, false, guarded.branch, guarded.rules), &fleet.Manifest{Version: 1, Repositories: []fleet.ManifestEntry{{Name: "o/good"}}})
			if r.Complete || r.Status != "DEGRADED" {
				t.Fatalf("%+v", r)
			}
			pull := r.Repositories[0].Pulls[0]
			if pull.Status != "UNKNOWN" || pull.Error != "API_PERMISSION" {
				t.Fatalf("pull did not degrade honestly: %+v", pull)
			}
			if len(pull.Checks) != 1 || pull.Checks[0].Name != "test" {
				t.Fatalf("check evidence lost: %+v", pull.Checks)
			}
			found := false
			for _, i := range r.Incidents {
				if i.Repository == "o/good" && i.Number == 1 && i.Check == "test" && i.Category != "" {
					found = true
				}
			}
			if !found {
				t.Fatalf("failed-check incident lost: %+v", r.Incidents)
			}
		})
	}
}

func TestFingerprintUsesHeadAndCheckIdentityNotDiagnosis(t *testing.T) {
	a := Incident{Repository: "o/r", Number: 1, SHA: "a", Identity: "app:workflow:test", Category: "UNCLASSIFIED"}
	b := a
	b.Category = "TYPE_ERROR"
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("diagnosis created a new incident")
	}
	b.SHA = "b"
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("new head reused old verdict")
	}
	b = a
	b.Identity = "app:other-workflow:test"
	if a.Fingerprint() == b.Fingerprint() {
		t.Fatal("different checks collided")
	}
}
