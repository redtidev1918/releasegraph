package cli

import (
	"bytes"
	"testing"
)

func TestPRHealthRequiresFleetCredential(t *testing.T) {
	t.Setenv("RELEASEGRAPH_FLEET_TOKEN", "")
	t.Setenv("PROFILE_REPO_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "not-a-fleet-token")
	var out, err bytes.Buffer
	code := Run([]string{"pr-health", "audit", "--manifest", "../../fleet.yaml"}, &out, &err)
	if code != 40 {
		t.Fatalf("%d %s", code, err.String())
	}
}

func TestPRHealthAlertCredentialSeparation(t *testing.T) {
	report := t.TempDir() + "/report.json"
	// The alert command must refuse the fleet credential: alerting writes
	// with the control repository's own token, never the fleet token.
	t.Setenv("GITHUB_TOKEN", "fleet-credential-value")
	t.Setenv("RELEASEGRAPH_FLEET_TOKEN", "fleet-credential-value")
	var out, err bytes.Buffer
	if code := Run([]string{"pr-health", "alert", "--report", report, "--repo", "redtidev1918/releasegraph"}, &out, &err); code != 50 {
		t.Fatalf("fleet token accepted for alerting: %d %s", code, err.String())
	}

	t.Setenv("RELEASEGRAPH_FLEET_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	out, err = bytes.Buffer{}, bytes.Buffer{}
	if code := Run([]string{"pr-health", "alert", "--report", report, "--repo", "redtidev1918/releasegraph"}, &out, &err); code != 40 {
		t.Fatalf("missing credential not reported: %d %s", code, err.String())
	}

	t.Setenv("GITHUB_TOKEN", "repo-token")
	t.Setenv("GITHUB_REPOSITORY", "")
	out, err = bytes.Buffer{}, bytes.Buffer{}
	if code := Run([]string{"pr-health", "alert", "--report", report}, &out, &err); code == 0 {
		t.Fatal("missing --repo accepted")
	}
}
