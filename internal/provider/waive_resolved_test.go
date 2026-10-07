package provider

import (
	"context"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/domain"
)

func TestWaiveResolvedSafety(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		number                    int
		sha                       string
		unmerged, dryRun, wantErr bool
	}{
		{"component plan", 30, "b6c2", false, true, false},
		{"component apply", 30, "b6c2", false, false, false},
		{"wrong PR", 31, "b6c2", false, false, true},
		{"changed commit", 30, "stale", false, false, true},
		{"unmerged PR", 30, "b6c2", true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeGitHub{prTitle: "chore: release main", prUnmerged: tc.unmerged}
			client := githubBoundForTest(t, f)
			report := &Report{Context: Context{Repository: acme, Version: domain.Version("0.4.1"), Provider: KindReleasePlease, ReleasePR: 30, PRMergeSHA: tc.sha}}
			err := WaiveResolved(context.Background(), client, report, tc.number, tc.dryRun)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.dryRun || tc.wantErr {
				if len(f.mutations) != 0 {
					t.Fatalf("unsafe mutation: %v", f.mutations)
				}
			} else if len(f.mutations) != 1 {
				t.Fatalf("expected one waiver mutation: %v", f.mutations)
			}
		})
	}
}
