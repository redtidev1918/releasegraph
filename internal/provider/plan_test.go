package provider

import (
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

func report(state State) *Report {
	observed := obs(state)
	r := &Report{Observed: observed, Verdict: Classify(observed)}
	r.Context = Context{Repository: "acme/app", Version: "2.16.0", Tag: "v2.16.0", Provider: KindReleasePlease}
	return r
}

// Acceptance: a plan is bound to the observation it was made from.
func TestPlanFingerprintChangesWithRemoteState(t *testing.T) {
	base := report(StatePending)
	doc := BuildPlanDoc(base, domain.ScopeFleet)
	if doc.ObservedRevision == "" || len(doc.Actions) != 1 || doc.Actions[0].Type != "provider_ack" {
		t.Fatalf("plan doc = %+v", doc)
	}
	if err := ValidatePlan(doc, base); err != nil {
		t.Fatalf("unchanged state rejected: %v", err)
	}

	// Any material change invalidates the plan.
	mutators := map[string]func(*Report){
		"provider acked":  func(r *Report) { r.Observed.ProviderState = StateTagged },
		"release deleted": func(r *Report) { r.Observed.Actual.ReleaseExists = false },
		"tag moved":       func(r *Report) { r.Observed.Actual.TagCommit = "other" },
		"assets lost":     func(r *Report) { r.Observed.Actual.AssetsComplete = false },
		"draft appeared":  func(r *Report) { r.Observed.Actual.ReleaseDraft = true },
	}
	for name, mutate := range mutators {
		changed := report(StatePending)
		mutate(changed)
		changed.Verdict = Classify(changed.Observed)
		if err := ValidatePlan(doc, changed); !rgerrors.IsKind(err, rgerrors.PlanStale) {
			t.Errorf("%s: ValidatePlan = %v, want PLAN_STALE", name, err)
		}
	}
}

// Acceptance: a plan for one version cannot be applied to another.
func TestPlanRejectsDifferentTarget(t *testing.T) {
	doc := BuildPlanDoc(report(StatePending), domain.ScopeFleet)
	other := report(StatePending)
	other.Context.Version = "2.17.0"
	if err := ValidatePlan(doc, other); !rgerrors.IsKind(err, rgerrors.PlanStale) {
		t.Fatalf("ValidatePlan = %v, want PLAN_STALE", err)
	}
}

// Acceptance: a healthy or in-flight observation yields a plan with no actions.
func TestPlanIsEmptyWhenNothingSafeToDo(t *testing.T) {
	healthy := BuildPlanDoc(report(StateTagged), domain.ScopeFleet)
	if len(healthy.Actions) != 0 {
		t.Fatalf("healthy plan has actions: %+v", healthy.Actions)
	}
	forbidden := map[string]bool{}
	for _, item := range healthy.Forbidden {
		forbidden[item] = true
	}
	if !forbidden["move_existing_tag"] || !forbidden["fabricate_historical_release"] {
		t.Fatalf("plan must always forbid tag moves and fabricated releases: %v", healthy.Forbidden)
	}

	inFlight := obs(StatePending)
	inFlight.Actual.ReleaseExists = false
	inFlight.Actual.ReleaseDraft = true
	r := &Report{Observed: inFlight, Verdict: Classify(inFlight)}
	r.Context = Context{Repository: "acme/app", Version: "2.16.0"}
	doc := BuildPlanDoc(r, domain.ScopeFleet)
	if len(doc.Actions) != 0 {
		t.Fatalf("in-flight plan must be empty: %+v", doc.Actions)
	}
	for _, item := range doc.Forbidden {
		if item == "repair_in_flight_transaction" {
			return
		}
	}
	t.Fatal("in-flight plan must forbid repairing the in-flight transaction")
}
