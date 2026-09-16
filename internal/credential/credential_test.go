package credential

import (
	"testing"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

// Acceptance: fleet scope without a fleet credential fails fast, before any API
// call, instead of surfacing a 403 later.
func TestFleetScopeRequiresFleetCredential(t *testing.T) {
	t.Setenv(FleetTokenEnv, "")
	t.Setenv(LegacyFleetTokenEnv, "")
	t.Setenv(RepoTokenEnv, "repo-token-present")

	if FleetAvailable() {
		t.Fatal("fleet credential reported available with only a repository token")
	}
	err := RequireFleet("provider reconcile --all")
	if !rgerrors.IsKind(err, rgerrors.FleetCredentialRequired) {
		t.Fatalf("RequireFleet = %v, want FLEET_CREDENTIAL_REQUIRED", err)
	}
	if err := UseFleetCredential(); !rgerrors.IsKind(err, rgerrors.FleetCredentialRequired) {
		t.Fatalf("UseFleetCredential = %v, want FLEET_CREDENTIAL_REQUIRED", err)
	}
	// The repository token must not have been overwritten.
	if RepoToken() != "repo-token-present" {
		t.Fatalf("repository token changed to %q", RepoToken())
	}
}

func TestDedicatedFleetTokenIsPreferred(t *testing.T) {
	t.Setenv(FleetTokenEnv, "fleet-dedicated")
	t.Setenv(LegacyFleetTokenEnv, "legacy-profile-token")

	token, source := Fleet()
	if token != "fleet-dedicated" || source != FleetSourceDedicated {
		t.Fatalf("Fleet() = %q %q, want the dedicated token", token, source)
	}
	if err := RequireFleet("fleet audit"); err != nil {
		t.Fatalf("RequireFleet: %v", err)
	}
}

func TestLegacyTokenIsAFallbackOnly(t *testing.T) {
	t.Setenv(FleetTokenEnv, "")
	t.Setenv(LegacyFleetTokenEnv, "legacy-profile-token")

	token, source := Fleet()
	if token != "legacy-profile-token" {
		t.Fatalf("fallback token = %q", token)
	}
	if source != FleetSourceLegacy {
		t.Fatalf("source = %q, want the deprecated fallback marker", source)
	}
}
