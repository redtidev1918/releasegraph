package prhealth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/github"
)

func incidentA() Incident {
	return Incident{Repository: "o/r", Number: 1, SHA: "sha-a", Identity: "github-actions:42:test", Check: "test", Category: "CI_CONFIGURATION", URL: "https://example/check"}
}

// scanAt synthesizes a report whose repositories mirror the incidents: each
// incident's pull is discovered and fully observed (green after resolution).
// degraded adds a pull whose verdict could not be verified (permission gap)
// while its checks were still fully observed.
func scanAt(start time.Time, complete bool, incidents ...Incident) Report {
	return scanObserving(start, complete, nil, incidents...)
}

func scanObserving(start time.Time, complete bool, degraded []Pull, incidents ...Incident) Report {
	r := Report{SchemaVersion: 1, StartedAt: start, FinishedAt: start.Add(time.Minute), Status: "FAIL", Complete: complete, Incidents: map[string]Incident{}}
	if len(incidents) == 0 {
		r.Status = "PASS"
	}
	for _, i := range incidents {
		r.Incidents[i.Fingerprint()] = i
	}
	repos := map[string]*Repository{}
	ensure := func(name string) *Repository {
		if repo, ok := repos[name]; ok {
			return repo
		}
		repo := &Repository{Name: name, Complete: true, Discovered: true, Pulls: []Pull{}}
		repos[name] = repo
		return repo
	}
	for _, i := range incidents {
		repo := ensure(i.Repository)
		repo.Pulls = append(repo.Pulls, Pull{Number: i.Number, SHA: i.SHA, Status: "FAIL", ChecksObserved: true})
	}
	for _, p := range degraded {
		repo := ensure("o/r")
		repo.Pulls = append(repo.Pulls, p)
	}
	if len(repos) == 0 {
		ensure("o/r")
	}
	for _, repo := range repos {
		r.Repositories = append(r.Repositories, *repo)
	}
	return r
}

func degradedPull(sha string) Pull {
	return Pull{Number: 1, SHA: sha, Status: "UNKNOWN", Error: "API_PERMISSION", ChecksObserved: true}
}

func TestNextStateEpisodesAndRecovery(t *testing.T) {
	t1 := time.Now().UTC().Add(-3 * time.Hour)
	t2 := t1.Add(time.Hour)
	t3 := t2.Add(time.Hour)
	t32 := t3.Add(10 * time.Minute)
	t33 := t3.Add(20 * time.Minute)
	t34 := t3.Add(30 * time.Minute)
	t35 := t3.Add(40 * time.Minute)
	t4 := t3.Add(time.Hour)
	t5 := t4.Add(time.Hour)

	plan, err := NextState(AlertState{}, scanAt(t1, true, incidentA()))
	if err != nil || len(plan.NewEpisodes) != 1 || plan.NewEpisodes[0].Episode != 1 || plan.Active != 1 {
		t.Fatalf("first scan: %+v err=%v", plan, err)
	}
	state := plan.State

	plan, err = NextState(state, scanAt(t2, true, incidentA()))
	if err != nil || len(plan.NewEpisodes) != 0 || plan.Active != 1 {
		t.Fatalf("repeat scan must not re-notify: %+v err=%v", plan, err)
	}
	if rec := plan.State.Incidents[incidentA().Fingerprint()]; !rec.LastSeen.Equal(t2) || !rec.FirstSeen.Equal(t1) {
		t.Fatalf("seen timestamps wrong: %+v", rec)
	}
	state = plan.State

	// A scan that failed to look at the incident's own repository (here: an
	// undiscovered repository in an overall incomplete scan) neither recovers
	// nor overwrites confirmed health.
	unobserved := scanAt(t3, false)
	unobserved.Repositories[0].Discovered = false
	plan, err = NextState(state, unobserved)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Active != 1 || len(plan.Recovered) != 0 {
		t.Fatalf("unobserving scan recovered incidents: %+v", plan)
	}
	if rec := plan.State.Incidents[incidentA().Fingerprint()]; !rec.LastSeen.Equal(t2) {
		t.Fatalf("unobserving scan rewrote last-seen: %+v", rec)
	}
	state = plan.State

	// Degraded elsewhere must not freeze recovery: an overall-incomplete scan
	// whose own pull was fully observed (checks green after a fix) resolves
	// the incident even though another pull stays permission-degraded.
	degradedScan := scanObserving(t32, false, []Pull{degradedPull("other-sha")})
	plan, err = NextState(state, degradedScan)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Active != 0 || len(plan.Recovered) != 1 {
		t.Fatalf("observed recovery blocked by unrelated degradation: %+v", plan)
	}
	state = plan.State

	// A permission-degraded verdict freezes only same-head verdict incidents;
	// a check incident on a fully observed head still resolves, and a newer
	// head retires the old fingerprint.
	frozen := scanObserving(t33, true, []Pull{degradedPull("sha-a")})
	frozen.Incidents[Incident{Repository: "o/r", Number: 1, SHA: "sha-a", Identity: "pr:UNKNOWN"}.Fingerprint()] = Incident{Repository: "o/r", Number: 1, SHA: "sha-a", Identity: "pr:UNKNOWN", Check: "UNKNOWN"}
	plan, err = NextState(AlertState{}, frozen)
	if err != nil || plan.Active != 1 {
		t.Fatalf("degraded scan setup wrong: %+v err=%v", plan, err)
	}
	resolved := scanObserving(t34, true, []Pull{degradedPull("sha-a")})
	plan, err = NextState(plan.State, resolved)
	if err != nil || plan.Active != 1 || len(plan.Recovered) != 0 {
		t.Fatalf("same-head unknown verdict must stay frozen: %+v err=%v", plan, err)
	}
	moved := scanObserving(t35, true, []Pull{degradedPull("sha-b")})
	plan, err = NextState(plan.State, moved)
	if err != nil || plan.Active != 0 || len(plan.Recovered) != 1 {
		t.Fatalf("superseding head must retire the old fingerprint: %+v err=%v", plan, err)
	}
	state = plan.State

	plan, err = NextState(state, scanAt(t4, true))
	if err != nil || plan.Active != 0 || len(plan.Recovered) != 0 {
		t.Fatalf("recovery failed: %+v err=%v", plan, err)
	}
	state = AlertState{At: t2, Incidents: map[string]*IncidentRecord{incidentA().Fingerprint(): {Incident: incidentA(), FirstSeen: t1, LastSeen: t2, Episode: 1}}, Recovered: nil}
	plan, err = NextState(state, scanAt(t4, true))
	if err != nil || plan.Active != 0 || len(plan.Recovered) != 1 || plan.Recovered[0].FirstSeen.Format(time.RFC3339) != t1.Format(time.RFC3339) {
		t.Fatalf("recovery failed: %+v err=%v", plan, err)
	}
	state = plan.State

	plan, err = NextState(state, scanAt(t5, true, incidentA()))
	if err != nil || len(plan.NewEpisodes) != 1 || plan.NewEpisodes[0].Episode != 2 {
		t.Fatalf("recurrence must open episode 2: %+v err=%v", plan, err)
	}
	if _, err := NextState(plan.State, scanAt(t5, true, incidentA())); err == nil {
		t.Fatal("stale scan accepted")
	}
}

// issueServer fakes the control repository's issue API with real state.
type issueServer struct {
	mu         sync.Mutex
	issues     map[int]map[string]any
	comments   map[int][]string
	nextNumber int
	failPatch  bool
	t          *testing.T
}

func newIssueServer(t *testing.T, marked bool) (*issueServer, *github.Bound) {
	t.Helper()
	s := &issueServer{issues: map[int]map[string]any{}, comments: map[int][]string{}, nextNumber: 7, t: t}
	if marked {
		state, err := json.Marshal(AlertState{At: time.Now().UTC().Add(-2 * time.Hour), Complete: true, Incidents: map[string]*IncidentRecord{}})
		if err != nil {
			t.Fatal(err)
		}
		s.issues[7] = map[string]any{"number": 7, "state": "closed", "title": "[Fleet Health] PR checks degraded",
			"body": "old\n" + AlertMarker + encodeStateForTest(t, state) + " -->\n"}
	}
	srv := httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(srv.Close)
	return s, github.NewForTest(srv.URL).Bind(domain.ExecutionContext{Scope: domain.ScopeRepository, Repository: "o/ctl"})
}

func encodeStateForTest(t *testing.T, raw []byte) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString(raw)
}

func (s *issueServer) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/comments"):
		n := issueNumber(strings.TrimSuffix(path, "/comments"))
		var out []map[string]any
		for _, body := range s.comments[n] {
			out = append(out, map[string]any{"body": body})
		}
		if out == nil {
			out = []map[string]any{}
		}
		json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/comments"):
		var payload struct{ Body string }
		json.NewDecoder(r.Body).Decode(&payload)
		n := issueNumber(strings.TrimSuffix(path, "/comments"))
		s.comments[n] = append(s.comments[n], payload.Body)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"id": len(s.comments[n])})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/issues"):
		var payload struct {
			Title string
			Body  string
		}
		json.NewDecoder(r.Body).Decode(&payload)
		s.nextNumber++
		n := s.nextNumber
		s.issues[n] = map[string]any{"number": n, "state": "open", "title": payload.Title, "body": payload.Body}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"number": n})
	case r.Method == http.MethodPatch:
		if s.failPatch {
			w.WriteHeader(422)
			return
		}
		var payload struct {
			Body  string
			State string
		}
		json.NewDecoder(r.Body).Decode(&payload)
		n := issueNumber(path)
		issue, ok := s.issues[n]
		if !ok {
			w.WriteHeader(404)
			return
		}
		if payload.Body != "" {
			issue["body"] = payload.Body
		}
		if payload.State != "" {
			issue["state"] = payload.State
		}
		json.NewEncoder(w).Encode(issue)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/issues"):
		var out []map[string]any
		for _, n := range sortedIssueNumbers(s.issues) {
			out = append(out, s.issues[n])
		}
		if out == nil {
			out = []map[string]any{}
		}
		json.NewEncoder(w).Encode(out)
	default:
		s.t.Errorf("unexpected %s %s", r.Method, path)
		w.WriteHeader(404)
	}
}

func issueNumber(path string) int {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	n := 0
	fmt.Sscanf(parts[len(parts)-1], "%d", &n)
	return n
}

func sortedIssueNumbers(issues map[int]map[string]any) []int {
	numbers := make([]int, 0, len(issues))
	for n := range issues {
		numbers = append(numbers, n)
	}
	for i := 0; i < len(numbers); i++ {
		for j := i + 1; j < len(numbers); j++ {
			if numbers[j] < numbers[i] {
				numbers[i], numbers[j] = numbers[j], numbers[i]
			}
		}
	}
	return numbers
}

func TestAlertPlanNeverWrites(t *testing.T) {
	store, client := newIssueServer(t, false)
	_, err := Alert(context.Background(), client, "o/ctl", scanAt(time.Now().UTC(), true, incidentA()), false)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.issues) != 0 {
		t.Fatal("plan mode wrote an issue")
	}
}

func TestAlertLifecycle(t *testing.T) {
	store, client := newIssueServer(t, false)
	now := time.Now().UTC().Truncate(time.Second)

	// 1. First failing scan creates the marked issue and notifies.
	plan, err := Alert(context.Background(), client, "o/ctl", scanAt(now, true, incidentA()), true)
	if err != nil || plan.Action != "create" || plan.Issue != 8 {
		t.Fatalf("create: %+v err=%v", plan, err)
	}
	store.mu.Lock()
	body := store.issues[8]["body"].(string)
	store.mu.Unlock()
	state, err := parseState(body)
	if err != nil || len(state.Incidents) != 1 {
		t.Fatalf("created body unreadable: %v", err)
	}
	if !strings.Contains(body, "API") && !strings.Contains(body, incidentA().Category) {
		t.Fatal("created body lost incident detail")
	}

	// 2. Same incident again: update only, no duplicate notification.
	later := now.Add(time.Hour)
	plan, err = Alert(context.Background(), client, "o/ctl", scanAt(later, true, incidentA()), true)
	if err != nil || plan.Action != "update" || len(plan.NewEpisodes) != 0 {
		t.Fatalf("repeat: %+v err=%v", plan, err)
	}
	store.mu.Lock()
	if len(store.comments[8]) != 0 {
		t.Fatal("repeat scan notified again")
	}
	store.mu.Unlock()

	// 3. Recovery: comment once and close the issue.
	recovered := later.Add(time.Hour)
	plan, err = Alert(context.Background(), client, "o/ctl", scanAt(recovered, true), true)
	if err != nil || plan.Action != "close" || len(plan.Recovered) != 1 {
		t.Fatalf("recovery: %+v err=%v", plan, err)
	}
	store.mu.Lock()
	if len(store.comments[8]) != 1 || !strings.Contains(store.comments[8][0], recoveredMarker) {
		t.Fatalf("recovery comment wrong: %q", store.comments[8])
	}
	if store.issues[8]["state"] != "closed" {
		t.Fatal("issue not closed on recovery")
	}
	store.mu.Unlock()

	// 4. Recurrence: episode 2, new notification, issue reopened.
	recurrence := recovered.Add(time.Hour)
	store.failPatch = true
	_, err = Alert(context.Background(), client, "o/ctl", scanAt(recurrence, true, incidentA()), true)
	if err == nil {
		t.Fatal("injected update failure must surface")
	}
	store.mu.Lock()
	posted := len(store.comments[8])
	store.mu.Unlock()
	if posted != 2 {
		t.Fatalf("notification missing before failed update: %d", posted)
	}
	store.failPatch = false
	plan, err = Alert(context.Background(), client, "o/ctl", scanAt(recurrence, true, incidentA()), true)
	if err != nil || len(plan.NewEpisodes) != 1 || plan.NewEpisodes[0].Episode != 2 {
		t.Fatalf("recurrence: %+v err=%v", plan, err)
	}
	store.mu.Lock()
	if len(store.comments[8]) != 2 {
		t.Fatal("retry duplicated the notification comment")
	}
	if !strings.Contains(store.comments[8][1], "episode 2") {
		t.Fatalf("recurrence comment lacks episode: %q", store.comments[8][1])
	}
	if store.issues[8]["state"] != "open" {
		t.Fatal("closed issue not reopened on recurrence")
	}
	finalBody := store.issues[8]["body"].(string)
	store.mu.Unlock()
	final, err := parseState(finalBody)
	if err != nil {
		t.Fatal(err)
	}
	rec := final.Incidents[incidentA().Fingerprint()]
	if rec == nil || rec.Episode != 2 || !rec.FirstSeen.Equal(recurrence) {
		t.Fatalf("final state wrong: %+v", rec)
	}
	if len(final.Recovered) != 1 {
		t.Fatalf("recovery log lost: %+v", final.Recovered)
	}
}

func TestAlertRefusesAmbiguousIssues(t *testing.T) {
	store, client := newIssueServer(t, true)
	store.mu.Lock()
	store.issues[9] = map[string]any{"number": 9, "state": "open", "body": store.issues[7]["body"]}
	store.mu.Unlock()
	if _, err := Alert(context.Background(), client, "o/ctl", scanAt(time.Now().UTC(), true, incidentA()), true); err == nil {
		t.Fatal("ambiguous marked issues accepted")
	}
}

func TestAlertRejectsOversizedBody(t *testing.T) {
	store, client := newIssueServer(t, true)
	many := make([]Incident, 0, 500)
	for i := 0; i < 500; i++ {
		many = append(many, Incident{Repository: "o/very-long-repository-name", Number: 10000 + i, SHA: "0123456789abcdef0123456789abcdef01234567", Identity: fmt.Sprintf("github-actions:%d:workflow-check-with-a-long-name-%d", i, i), Check: "workflow check", Category: "CI_CONFIGURATION", URL: "https://example.com/very/long/evidence/url"})
	}
	_, err := Alert(context.Background(), client, "o/ctl", scanAt(time.Now().UTC(), true, many...), true)
	if err == nil || !strings.Contains(err.Error(), "safe issue size") {
		t.Fatalf("oversized body accepted: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.comments[7]) != 0 || len(store.issues) != 1 {
		t.Fatal("failed alert mutated persistent state")
	}
}

func TestAlertBodyDisclosesCapabilityGaps(t *testing.T) {
	state := AlertState{At: time.Now().UTC(), Complete: false, Incidents: map[string]*IncidentRecord{
		incidentA().Fingerprint(): {Incident: func() Incident { i := incidentA(); i.Evidence = "API_PERMISSION"; return i }(), FirstSeen: time.Now().UTC(), LastSeen: time.Now().UTC(), Episode: 1},
	}}
	body := alertBody(state, Report{SchemaVersion: 1, StartedAt: state.At, Status: "DEGRADED"})
	if !strings.Contains(body, "Monitoring capability gaps") || !strings.Contains(body, "Administration") {
		t.Fatal("capability gap not disclosed in alert body")
	}
	if !strings.Contains(body, "API_PERMISSION") {
		t.Fatal("failure code missing from alert body")
	}
}
