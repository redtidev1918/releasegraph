package cli

import (
	"testing"

	"github.com/redtidev1918/releasegraph/internal/provider"
)

// A waived version is not healthy, so ACKAllowed stays false — but its merged
// release PR keeps release-please's `autorelease: pending` label, and that label
// blocks every newer release PR for the repository. `provider reconcile` must
// therefore acknowledge it, otherwise the documented waiver
// (`releasegraph provider waive`, then reconcile) cannot actually unblock the
// fleet, which is exactly the state pixivflow-webui v1.1.0 was stuck in.
func TestAcknowledgeTargetsIncludesWaivedVersions(t *testing.T) {
	cases := []struct {
		name    string
		verdict provider.Verdict
		want    bool
	}{
		{"acknowledgeable release", provider.Verdict{ACKAllowed: true}, true},
		{"explicitly waived historical version", provider.Verdict{Waived: true}, true},
		{"unrecoverable and unwaived", provider.Verdict{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := acknowledgeTargets(tc.verdict); got != tc.want {
				t.Fatalf("acknowledgeTargets(%+v) = %v, want %v", tc.verdict, got, tc.want)
			}
		})
	}
}
