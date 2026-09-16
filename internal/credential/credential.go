// Package credential separates the credential classes ReleaseGraph uses.
//
//	repo  : GITHUB_TOKEN            — bound to one repository
//	fleet : RELEASEGRAPH_FLEET_TOKEN — cross-repository control plane
//
// A fleet operation must never silently fall back to a repository token: it
// fails fast with FLEET_CREDENTIAL_REQUIRED instead of surfacing a 403 later.
package credential

import (
	"fmt"
	"os"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
)

const (
	// FleetTokenEnv is the credential of record for fleet operations.
	FleetTokenEnv = "RELEASEGRAPH_FLEET_TOKEN"
	// LegacyFleetTokenEnv is a temporary fallback kept for migration. It is
	// deprecated: it was named for an unrelated purpose and only happens to
	// have sufficient scope.
	LegacyFleetTokenEnv = "PROFILE_REPO_TOKEN"
	// RepoTokenEnv is the repository-scoped token.
	RepoTokenEnv = "GITHUB_TOKEN"
)

// FleetSource describes where the fleet credential came from.
type FleetSource string

const (
	FleetSourceDedicated FleetSource = FleetTokenEnv
	FleetSourceLegacy    FleetSource = LegacyFleetTokenEnv + " (deprecated)"
	FleetSourceMissing   FleetSource = "missing"
)

// Fleet returns the fleet credential and its source.
func Fleet() (string, FleetSource) {
	if token := os.Getenv(FleetTokenEnv); token != "" {
		return token, FleetSourceDedicated
	}
	if token := os.Getenv(LegacyFleetTokenEnv); token != "" {
		return token, FleetSourceLegacy
	}
	return "", FleetSourceMissing
}

// FleetAvailable reports whether a fleet credential exists.
func FleetAvailable() bool {
	token, _ := Fleet()
	return token != ""
}

// UseFleetCredential exports the fleet token as GITHUB_TOKEN for the GitHub
// client and reports the source. It returns an error when none is configured.
func UseFleetCredential() error {
	token, source := Fleet()
	if token == "" {
		return rgerrors.New(rgerrors.FleetCredentialRequired, fleetRequirement())
	}
	if source == FleetSourceLegacy {
		fmt.Fprintf(os.Stderr, "note: using deprecated %s; migrate to %s\n", LegacyFleetTokenEnv, FleetTokenEnv)
	}
	return os.Setenv(RepoTokenEnv, token)
}

// RequireFleet fails fast when a fleet-scoped operation has no fleet credential.
func RequireFleet(purpose string) error {
	if FleetAvailable() {
		return nil
	}
	return rgerrors.New(rgerrors.FleetCredentialRequired, fleetRequirement()+" (needed for "+purpose+")")
}

func fleetRequirement() string {
	return fmt.Sprintf("%s is not set; fleet operations cannot use the repository-scoped %s", FleetTokenEnv, RepoTokenEnv)
}

// RepoToken returns the repository-scoped token.
func RepoToken() string { return os.Getenv(RepoTokenEnv) }
