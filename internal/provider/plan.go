package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

// PlanVersion is the schema version of a plan document.
const PlanVersion = 1

// PlannedAction is one mutation a plan would perform through a primitive.
type PlannedAction struct {
	Type    string `json:"type"`
	Version string `json:"version,omitempty"`
	Risk    string `json:"risk"`
}

// PlanDoc is a reviewable, revalidated plan. Applying it re-observes remote
// state first: a plan is a statement about a specific observation, never a
// permission to mutate whatever happens to be there now (TOCTOU).
type PlanDoc struct {
	PlanVersion      int             `json:"planVersion"`
	Repository       string          `json:"repository"`
	Version          string          `json:"version"`
	ObservedRevision string          `json:"observedRevision"`
	Health           string          `json:"health"`
	Diagnosis        string          `json:"diagnosis"`
	Code             string          `json:"code"`
	Actions          []PlannedAction `json:"actions"`
	Forbidden        []string        `json:"forbiddenActions"`
	CreatedAt        string          `json:"createdAt"`
	Scope            string          `json:"executionScope"`
}

// Fingerprint is the observed revision a plan is bound to. Any material change
// in remote state produces a different fingerprint.
func Fingerprint(r *Report) string {
	a := r.Observed.Actual
	parts := []string{
		r.Context.Repository, string(r.Context.Version), r.Context.Tag,
		fmt.Sprintf("%t/%s/%s", a.TagExists, a.TagCommit, a.ExpectedCommit),
		fmt.Sprintf("%t/%t/%t", a.ReleaseExists, a.ReleaseDraft, a.Latest),
		fmt.Sprintf("%t/%t", a.AssetsComplete, a.ChecksumsVerified),
		fmt.Sprintf("%t/%t", a.RegistriesHealthy, a.NoRegistryRequired),
		string(r.Observed.Provider), string(r.Observed.ProviderState),
		fmt.Sprintf("%t", r.Context.PolicyHashMatches),
		string(r.Verdict.Drift), string(r.Verdict.Health),
	}
	sort.Strings(parts[3:]) // ordering-independent for the flag tuples
	sum := sha256.Sum256([]byte(fmt.Sprintf("%v", parts)))
	return hex.EncodeToString(sum[:])[:16]
}

// BuildPlanDoc turns a verdict into a plan document.
func BuildPlanDoc(r *Report, scope domain.ExecutionScope) PlanDoc {
	doc := PlanDoc{
		PlanVersion:      PlanVersion,
		Repository:       r.Context.Repository,
		Version:          string(r.Context.Version),
		ObservedRevision: Fingerprint(r),
		Health:           string(r.Verdict.Health),
		Diagnosis:        string(r.Verdict.Drift),
		Code:             string(r.Verdict.Drift),
		Actions:          []PlannedAction{},
		Forbidden:        forbiddenFor(r),
		CreatedAt:        time.Now().UTC().Format(time.RFC3339),
		Scope:            string(scope),
	}
	switch {
	case r.Verdict.ACKAllowed:
		doc.Actions = append(doc.Actions, PlannedAction{Type: "provider_ack", Version: doc.Version, Risk: "low"})
	case r.Verdict.RepairSameVersion && !r.Verdict.HardFail:
		doc.Actions = append(doc.Actions, PlannedAction{Type: "same_version_repair", Version: doc.Version, Risk: "medium"})
	}
	return doc
}

// forbiddenFor is the plan-level deny list: what must never be done for this
// observation, regardless of who asks.
func forbiddenFor(r *Report) []string {
	forbidden := []string{"move_existing_tag", "fabricate_historical_release"}
	if r.Verdict.Health == domain.HealthRunning {
		forbidden = append(forbidden, "repair_in_flight_transaction", "provider_ack_while_draft")
	}
	if r.Verdict.Health != domain.HealthHealthy {
		forbidden = append(forbidden, "create_new_version")
	}
	if !r.Verdict.ACKAllowed {
		forbidden = append(forbidden, "manual_provider_label_edit")
	}
	if r.Verdict.HardFail {
		forbidden = append(forbidden, "automatic_repair")
	}
	return forbidden
}

// ValidatePlan rejects a plan whose observation no longer matches reality.
func ValidatePlan(doc PlanDoc, current *Report) error {
	if doc.PlanVersion != PlanVersion {
		return rgerrors.New(rgerrors.InvariantViolation, fmt.Sprintf("unsupported plan version %d", doc.PlanVersion))
	}
	if doc.Repository != current.Context.Repository || doc.Version != string(current.Context.Version) {
		return rgerrors.New(rgerrors.PlanStale, fmt.Sprintf(
			"plan targets %s@%s but the current observation is %s@%s",
			doc.Repository, doc.Version, current.Context.Repository, current.Context.Version))
	}
	if observed := Fingerprint(current); observed != doc.ObservedRevision {
		return rgerrors.New(rgerrors.PlanStale, fmt.Sprintf(
			"remote state changed since the plan was created (%s -> %s); re-plan before applying",
			doc.ObservedRevision, observed))
	}
	return nil
}

// LoadPlan reads a plan document from disk.
func LoadPlan(path string) (PlanDoc, error) {
	var doc PlanDoc
	raw, err := os.ReadFile(path)
	if err != nil {
		return doc, fmt.Errorf("read plan %s: %w", path, err)
	}
	if err := jsonUnmarshal(raw, &doc); err != nil {
		return doc, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if doc.Repository == "" {
		// The CLI wraps documents in a standard envelope; accept both shapes.
		var envelope struct {
			Data PlanDoc `json:"data"`
		}
		if err := jsonUnmarshal(raw, &envelope); err != nil {
			return doc, fmt.Errorf("parse plan %s: %w", path, err)
		}
		if envelope.Data.Repository != "" {
			return envelope.Data, nil
		}
	}
	return doc, nil
}

// jsonUnmarshal is a tiny indirection so tests can exercise the parse error path.
func jsonUnmarshal(raw []byte, target any) error { return json.Unmarshal(raw, target) }
