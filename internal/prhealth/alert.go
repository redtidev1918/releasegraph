package prhealth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redtidev1918/releasegraph/internal/github"
)

// The single marked control-repository issue is the authority for incident
// state. Its body embeds the serialized AlertState; comments carry idempotent
// notification markers derived from the scan timestamp, so a retry after a
// partial write cannot double-notify.
const (
	AlertMarker     = "<!-- releasegraph:pr-health:v2 "
	noticeMarker    = "<!-- releasegraph:pr-health-notice:v2 "
	recoveredMarker = "<!-- releasegraph:pr-health-recovered:v2 "
	maxRecoveries   = 50
	maxIssueBody    = 60000
	// ControlActor is mentioned once per notification so GitHub delivers it.
	ControlActor = "@redtidev1918"
)

// IncidentRecord is one incident across scans of one episode.
type IncidentRecord struct {
	Incident  Incident  `json:"incident"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	Episode   int       `json:"episode"`
}

// RecoveryRecord is an incident that a complete scan no longer observes.
type RecoveryRecord struct {
	Fingerprint string    `json:"fingerprint"`
	Incident    Incident  `json:"incident"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	Episode     int       `json:"episode"`
	RecoveredAt time.Time `json:"recovered_at"`
}

// AlertState is the durable state serialized into the alert issue body.
type AlertState struct {
	At        time.Time                  `json:"at"`
	Complete  bool                       `json:"complete"`
	Incidents map[string]*IncidentRecord `json:"incidents"`
	Recovered []RecoveryRecord           `json:"recovered,omitempty"`
}

// AlertPlan is the outcome of folding a report into the previous state.
type AlertPlan struct {
	Issue       int              `json:"issue"`
	Action      string           `json:"action"`
	NewEpisodes []IncidentRecord `json:"new_episodes,omitempty"`
	Recovered   []RecoveryRecord `json:"recovered,omitempty"`
	Active      int              `json:"active"`
	State       AlertState       `json:"state"`
}

// observationAuthority records, per pull request, what a scan actually
// observed. Recovery of an incident is decided by the evidence quality of its
// own repository and pull request — a monitoring failure elsewhere (or a
// permission gap on another aspect of the same pull request) must not freeze
// unrelated confirmed health, and confirmed health must not be cleared by a
// scan that failed to look at it.
type pullObservation struct {
	present         bool
	checksObserved  bool // check rollup, runs and classification fully observed
	verdictObserved bool // definitive verdict (no observation error)
	sha             string
}

type observationAuthority struct {
	discovered map[string]bool
	pulls      map[string]pullObservation
}

func observationKey(repo string, number int) string {
	return fmt.Sprintf("%s#%d", repo, number)
}

func authorityFrom(r Report) observationAuthority {
	a := observationAuthority{
		discovered: map[string]bool{},
		pulls:      map[string]pullObservation{},
	}
	for _, repo := range r.Repositories {
		a.discovered[repo.Name] = repo.Discovered
		for _, p := range repo.Pulls {
			a.pulls[observationKey(repo.Name, p.Number)] = pullObservation{
				present:         true,
				checksObserved:  p.ChecksObserved,
				verdictObserved: p.Error == "",
				sha:             p.SHA,
			}
		}
	}
	return a
}

// canRecover reports whether this scan authoritatively resolved the incident.
func (a observationAuthority) canRecover(i Incident) bool {
	if discovered, ok := a.discovered[i.Repository]; !ok || !discovered {
		// The scan could not even establish this repository's open-PR set.
		return false
	}
	if i.Identity == "SCAN_INCOMPLETE" {
		// Repository-level degradation: resolved once discovery succeeds and
		// the scan no longer reports it.
		return true
	}
	obs, present := a.pulls[observationKey(i.Repository, i.Number)]
	if !present {
		// Discovered repository, absent pull: merged or closed. Its head
		// cannot come back through this pull request.
		return true
	}
	superseded := obs.sha != i.SHA
	if strings.HasPrefix(i.Identity, "pr:") {
		// Pull-verdict incidents: a definitive verdict resolves the question,
		// on this head or a superseding one. An unknown verdict freezes the
		// same head; a superseding head with fully observed checks also
		// resolves it, because the old head no longer exists.
		return obs.verdictObserved || (obs.checksObserved && superseded)
	}
	// Check-identity incidents: fully observed checks on this head mean this
	// check no longer fails; any superseding head retires the old fingerprint.
	return obs.checksObserved || (obs.verdictObserved && superseded)
}

// NextState folds a scan report into the previous alert state.
//
// Recovery requires an authoritative observation of the incident's own
// repository and pull request (see observationAuthority); anything the scan
// failed to observe keeps its record untouched, so a monitoring failure can
// neither clear confirmed health nor be cleared by stale data. A fingerprint
// that reappears after recovery starts a new episode and notifies again.
func NextState(previous AlertState, r Report) (*AlertPlan, error) {
	if !previous.At.IsZero() && !r.StartedAt.After(previous.At) {
		return nil, fmt.Errorf("stale scan cannot replace alert state")
	}
	now := r.StartedAt
	authority := authorityFrom(r)
	next := AlertState{At: now, Complete: r.Complete, Incidents: map[string]*IncidentRecord{}, Recovered: previous.Recovered}
	plan := &AlertPlan{State: next}
	for fp, rec := range previous.Incidents {
		next.Incidents[fp] = rec
	}
	for fp, rec := range previous.Incidents {
		if _, still := r.Incidents[fp]; still {
			continue
		}
		if !authority.canRecover(rec.Incident) {
			continue
		}
		recovered := RecoveryRecord{
			Fingerprint: fp, Incident: rec.Incident,
			FirstSeen: rec.FirstSeen, LastSeen: rec.LastSeen,
			Episode: rec.Episode, RecoveredAt: now,
		}
		plan.Recovered = append(plan.Recovered, recovered)
		next.Recovered = append([]RecoveryRecord{recovered}, next.Recovered...)
		delete(next.Incidents, fp)
	}
	fingerprints := make([]string, 0, len(r.Incidents))
	for fp := range r.Incidents {
		fingerprints = append(fingerprints, fp)
	}
	sort.Strings(fingerprints)
	for _, fp := range fingerprints {
		incident := r.Incidents[fp]
		if rec, ok := next.Incidents[fp]; ok {
			rec.LastSeen = now
			rec.Incident = incident
			continue
		}
		episode := 1
		for _, rr := range previous.Recovered {
			if rr.Fingerprint == fp && rr.Episode >= episode {
				episode = rr.Episode + 1
			}
		}
		rec := &IncidentRecord{Incident: incident, FirstSeen: now, LastSeen: now, Episode: episode}
		next.Incidents[fp] = rec
		plan.NewEpisodes = append(plan.NewEpisodes, *rec)
	}
	if len(next.Recovered) > maxRecoveries {
		next.Recovered = next.Recovered[:maxRecoveries]
	}
	plan.Active = len(next.Incidents)
	// plan.State snapshots next when the plan is created; reassignments such
	// as the recovery log above must be carried into the returned state.
	plan.State = next
	return plan, nil
}

func parseState(body string) (AlertState, error) {
	var state AlertState
	start := strings.Index(body, AlertMarker)
	if start < 0 {
		return state, fmt.Errorf("alert state missing")
	}
	rest := body[start+len(AlertMarker):]
	end := strings.Index(rest, " -->")
	if end < 0 {
		return state, fmt.Errorf("alert state truncated")
	}
	raw, err := base64.StdEncoding.DecodeString(rest[:end])
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, err
	}
	if state.At.IsZero() || state.Incidents == nil {
		return state, fmt.Errorf("invalid alert state")
	}
	return state, nil
}

func alertBody(state AlertState, r Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Fleet PR Health — active incidents: %d\n\n", len(state.Incidents))
	fmt.Fprintf(&b, "This single issue is the persistent incident record. `releasegraph pr-health alert` advances it; the state below is the deduplication and recovery authority.\n\n")
	fmt.Fprintf(&b, "Scan %s — %s; status **%s**; complete=%t; coverage %d/%d repositories.\n\n",
		r.StartedAt.Format(time.RFC3339), r.FinishedAt.Format(time.RFC3339), r.Status, r.Complete, len(r.Repositories), len(r.Expected))
	if codes := capabilityGaps(state); len(codes) > 0 {
		b.WriteString("## Monitoring capability gaps\n\n")
		for _, code := range codes {
			switch code {
			case "API_PERMISSION":
				b.WriteString("- `API_PERMISSION`: the ReleaseGraph App installation lacks a read permission (currently Administration for branch protection / required checks). Observed checks are preserved; affected pull requests stay UNKNOWN and are never reported healthy. Resolution: the app owner grants the missing read permission in GitHub App settings.\n")
			case "API_RATE_LIMIT":
				b.WriteString("- `API_RATE_LIMIT`: the scan hit GitHub rate limits; coverage is incomplete for affected repositories.\n")
			case "API_TIMEOUT":
				b.WriteString("- `API_TIMEOUT`: GitHub API timeouts degraded part of the scan.\n")
			default:
				fmt.Fprintf(&b, "- `%s`: scan capability degraded for affected repositories.\n", code)
			}
		}
		b.WriteString("\n")
	}
	if len(state.Incidents) > 0 {
		b.WriteString("## Active incidents\n\n")
		b.WriteString("| Repository | PR | Head | Check | Category | Episode | First seen | Last seen | Evidence |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
		for _, rec := range sortedRecords(state.Incidents) {
			i := rec.Incident
			head := i.SHA
			if len(head) > 10 {
				head = head[:10]
			}
			evidence := i.Evidence
			if evidence == "" {
				evidence = "—"
			}
			evidence = strings.ReplaceAll(evidence, "\n", " ")
			if evidence != "—" && len(evidence) > 80 {
				evidence = evidence[:80] + "…"
			}
			link := "—"
			if i.URL != "" {
				link = fmt.Sprintf("[view](%s)", i.URL)
			}
			fmt.Fprintf(&b, "| %s | #%d | `%s` | %s | %s | %d | %s | %s | %s %s |\n",
				i.Repository, i.Number, head, strings.ReplaceAll(i.Check, "\n", " "), i.Category,
				rec.Episode, rec.FirstSeen.Format(time.RFC3339), rec.LastSeen.Format(time.RFC3339), evidence, link)
		}
		b.WriteString("\n")
	}
	if len(state.Recovered) > 0 {
		b.WriteString("## Recovered\n\n")
		b.WriteString("| Repository | PR | Check | First seen | Recovered at |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, rr := range state.Recovered {
			fmt.Fprintf(&b, "| %s | #%d | %s | %s | %s |\n",
				rr.Incident.Repository, rr.Incident.Number, strings.ReplaceAll(rr.Incident.Check, "\n", " "),
				rr.FirstSeen.Format(time.RFC3339), rr.RecoveredAt.Format(time.RFC3339))
		}
		b.WriteString("\n")
	}
	if !state.Complete {
		b.WriteString("Incomplete scans retain previously confirmed incidents; recovery requires a complete scan.\n\n")
	}
	raw, _ := json.Marshal(state)
	b.WriteString(AlertMarker + base64.StdEncoding.EncodeToString(raw) + " -->\n")
	return b.String()
}

// capabilityGaps lists the distinct degradation codes among active incidents
// so the alert discloses the monitor's own failures, not only fleet failures.
func capabilityGaps(state AlertState) []string {
	seen := map[string]bool{}
	for _, rec := range state.Incidents {
		code := rec.Incident.Evidence
		if code == "" {
			continue
		}
		for _, known := range []string{"API_PERMISSION", "API_RATE_LIMIT", "API_TIMEOUT", "SCAN_INCOMPLETE", "PR_DISCOVERY_CHANGED"} {
			if code == known {
				seen[code] = true
			}
		}
	}
	codes := make([]string, 0, len(seen))
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func sortedRecords(incidents map[string]*IncidentRecord) []*IncidentRecord {
	fingerprints := make([]string, 0, len(incidents))
	for fp := range incidents {
		fingerprints = append(fingerprints, fp)
	}
	sort.Strings(fingerprints)
	records := make([]*IncidentRecord, 0, len(fingerprints))
	for _, fp := range fingerprints {
		records = append(records, incidents[fp])
	}
	return records
}

// markedIssue finds the single marked alert issue, if any.
func markedIssue(ctx context.Context, c *github.Bound, repo string) (int, string, AlertState, error) {
	issues, err := c.Paginate(ctx, fmt.Sprintf("repos/%s/issues?state=all&sort=created&direction=asc&per_page=100", repo))
	if err != nil {
		return 0, "", AlertState{}, err
	}
	number := 0
	state := ""
	var previous AlertState
	for _, issue := range issues {
		if issue["pull_request"] != nil {
			continue
		}
		body, _ := issue["body"].(string)
		if !strings.Contains(body, AlertMarker) {
			continue
		}
		if number != 0 {
			return 0, "", AlertState{}, fmt.Errorf("multiple marked fleet-health issues; refusing ambiguous update")
		}
		parsed, err := parseState(body)
		if err != nil {
			return 0, "", AlertState{}, fmt.Errorf("marked issue #%v has unreadable state: %w", issue["number"], err)
		}
		number = int(issue["number"].(float64))
		state, _ = issue["state"].(string)
		previous = parsed
	}
	return number, state, previous, nil
}

// Alert plans by default; apply writes the single marked issue. Plan never
// mutates. Write order keeps a retry safe: idempotent notification comments
// first, then the state-bearing body update that completes the transaction.
func Alert(ctx context.Context, c *github.Bound, repo string, r Report, apply bool) (*AlertPlan, error) {
	number, issueState, previous, err := markedIssue(ctx, c, repo)
	if err != nil {
		return nil, err
	}
	plan, err := NextState(previous, r)
	if err != nil {
		return nil, err
	}
	plan.Issue = number
	switch {
	case number == 0 && plan.Active == 0 && len(plan.Recovered) == 0:
		plan.Action = "none"
	case number == 0:
		plan.Action = "create"
	case plan.Active == 0:
		plan.Action = "close"
	default:
		plan.Action = "update"
	}
	if !apply || plan.Action == "none" {
		return plan, nil
	}
	body := alertBody(plan.State, r)
	if len(body) > maxIssueBody {
		return plan, fmt.Errorf("alert body exceeds safe issue size (%d); prior state preserved", len(body))
	}
	if plan.Action == "create" {
		created, err := c.CreateIssue(ctx, repo,
			"[Fleet Health] PR checks degraded",
			fmt.Sprintf("%s Fleet health monitoring started reporting incidents.\n\n%s", ControlActor, body),
			nil)
		if err != nil {
			return plan, err
		}
		plan.Issue = created
		return plan, nil
	}
	if len(plan.Recovered) > 0 {
		if err := postMarkedComment(ctx, c, repo, number, recoveredMarker, plan.State.At, fingerprintsComment(plan.Recovered), recoveryComment(plan.Recovered)); err != nil {
			return plan, err
		}
	}
	if len(plan.NewEpisodes) > 0 {
		if err := postMarkedComment(ctx, c, repo, number, noticeMarker, plan.State.At, episodeFingerprints(plan.NewEpisodes), episodeComment(plan.NewEpisodes)); err != nil {
			return plan, err
		}
	}
	// An explicitly named state reopens a closed issue on recurrence and is
	// idempotent when the state already matches.
	nextState := "open"
	if plan.Active == 0 {
		nextState = "closed"
	}
	_ = issueState
	return plan, c.UpdateIssue(ctx, repo, number, body, nextState)
}

func episodeFingerprints(records []IncidentRecord) string {
	fps := make([]string, 0, len(records))
	for _, rec := range records {
		fps = append(fps, rec.Incident.Fingerprint())
	}
	return strings.Join(fps, ",")
}

func episodeComment(records []IncidentRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s Fleet health: %d new incident episode(s).\n\n", ControlActor, len(records))
	for _, rec := range records {
		i := rec.Incident
		fmt.Fprintf(&b, "- %s #%d — %s — %s", i.Repository, i.Number, strings.ReplaceAll(i.Check, "\n", " "), i.Category)
		if i.URL != "" {
			fmt.Fprintf(&b, " ([evidence](%s))", i.URL)
		}
		if rec.Episode > 1 {
			fmt.Fprintf(&b, " (episode %d: recovered earlier and recurred)", rec.Episode)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func recoveryComment(records []RecoveryRecord) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Fleet health: %d incident(s) recovered on a complete scan.\n\n", len(records))
	for _, rr := range records {
		fmt.Fprintf(&b, "- %s #%d — %s (first seen %s, recovered %s)\n",
			rr.Incident.Repository, rr.Incident.Number, strings.ReplaceAll(rr.Incident.Check, "\n", " "),
			rr.FirstSeen.Format(time.RFC3339), rr.RecoveredAt.Format(time.RFC3339))
	}
	return b.String()
}

func fingerprintsComment(records []RecoveryRecord) string {
	fps := make([]string, 0, len(records))
	for _, rr := range records {
		fps = append(fps, rr.Fingerprint)
	}
	return strings.Join(fps, ",")
}

// postMarkedComment posts body with an idempotent marker once per scan; a
// retry of the same scan finds the marker and skips the notification.
func postMarkedComment(ctx context.Context, c *github.Bound, repo string, number int, prefix string, at time.Time, fingerprints, body string) error {
	marker := fmt.Sprintf("%s%s %s -->", prefix, at.Format(time.RFC3339Nano), fingerprints)
	comments, err := c.Paginate(ctx, fmt.Sprintf("repos/%s/issues/%d/comments?per_page=100", repo, number))
	if err != nil {
		return err
	}
	for _, comment := range comments {
		if text, _ := comment["body"].(string); strings.Contains(text, marker) {
			return nil
		}
	}
	return c.CommentOnIssue(ctx, repo, number, body+"\n"+marker)
}
