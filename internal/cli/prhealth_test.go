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
