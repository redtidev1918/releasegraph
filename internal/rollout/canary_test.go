package rollout

import (
	"strings"
	"testing"
)

// Acceptance: a new version goes to the repository whose pin is furthest behind
// it, so the largest engine delta is proven by one repository instead of the
// whole fleet.
func TestSelectCanaryPicksTheOldestPin(t *testing.T) {
	movable := []string{"acme/app", "acme/lib", "acme/deploy"}
	distance := map[string]int{"acme/app": 3, "acme/lib": 11, "acme/deploy": 7}

	choice := SelectCanary(movable, distance, "acme/app", "v1.4.1")
	if choice.Repository != "acme/lib" {
		t.Fatalf("canary = %q, want acme/lib", choice.Repository)
	}
	if !strings.Contains(choice.Reason, "11") || !strings.Contains(choice.Reason, "v1.4.1") {
		t.Fatalf("reason = %q, want the measured distance", choice.Reason)
	}
}

// A tie is broken on the repository name, so the same fleet always produces the
// same plan.
func TestSelectCanaryTieBreaksOnName(t *testing.T) {
	choice := SelectCanary([]string{"acme/lib", "acme/app"}, map[string]int{"acme/app": 5, "acme/lib": 5}, "", "v1.4.1")
	if choice.Repository != "acme/app" {
		t.Fatalf("canary = %q, want the lexicographically first tie", choice.Repository)
	}
}

// Acceptance: when no distance can be measured the manifest stays the authority.
func TestSelectCanaryKeepsTheDeclaredCanaryWithoutDistance(t *testing.T) {
	choice := SelectCanary([]string{"acme/lib"}, map[string]int{}, "acme/app", "v1.4.1")
	if choice.Repository != "acme/app" || !strings.Contains(choice.Reason, "declared canary") {
		t.Fatalf("choice = %+v, want the declared canary with a stated reason", choice)
	}
	if unset := SelectCanary([]string{"acme/lib"}, map[string]int{}, "", "v1.4.1"); unset.Repository != "" {
		t.Fatalf("choice = %+v, want no canary when nothing can be measured or declared", unset)
	}
}

// Nothing to move means no canary at all: the plan must not name one.
func TestSelectCanaryWithNothingMovable(t *testing.T) {
	choice := SelectCanary(nil, map[string]int{}, "acme/app", "v1.4.1")
	if choice.Repository != "" || !strings.Contains(choice.Reason, "no repository needs v1.4.1") {
		t.Fatalf("choice = %+v", choice)
	}
}
