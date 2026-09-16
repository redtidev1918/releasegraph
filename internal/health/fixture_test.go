package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/redtidev1918/releasegraph/internal/policy"
)

// The two fixtures are shared with the Python implementation: the same file
// drives release_infra/health.py from the Python test suite. A case failing here
// and passing there (or the reverse) means the two halves of the contract have
// drifted, which is exactly what these files exist to prevent.

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "health", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return raw
}

func TestSharedCases(t *testing.T) {
	var document struct {
		Cases []struct {
			Name        string          `json:"name"`
			Policy      json.RawMessage `json:"policy"`
			Observation Observation     `json:"observation"`
			Expect      struct {
				Health        string   `json:"health"`
				Reasons       []string `json:"reasons"`
				MissingAssets []string `json:"missing_assets"`
				EmptyAssets   []string `json:"empty_assets"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixture(t, "cases.json"), &document); err != nil {
		t.Fatalf("cases.json: %v", err)
	}
	if len(document.Cases) < 20 {
		t.Fatalf("cases.json looks truncated: %d cases", len(document.Cases))
	}

	for _, testCase := range document.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			parsed, err := policy.Parse(testCase.Policy)
			if err != nil {
				// The fixture is the contract; a policy in it must be valid.
				t.Fatalf("policy does not validate: %v", err)
			}
			got := Assess(parsed, testCase.Observation)
			if string(got.Health) != testCase.Expect.Health {
				t.Fatalf("health=%s want %s (reasons=%v)", got.Health, testCase.Expect.Health, got.Codes())
			}
			if strings.Join(got.Codes(), ",") != strings.Join(testCase.Expect.Reasons, ",") {
				t.Fatalf("reasons=%v want %v", got.Codes(), testCase.Expect.Reasons)
			}
			if strings.Join(got.MissingAssets, ",") != strings.Join(testCase.Expect.MissingAssets, ",") {
				t.Fatalf("missing=%v want %v", got.MissingAssets, testCase.Expect.MissingAssets)
			}
			if strings.Join(got.EmptyAssets, ",") != strings.Join(testCase.Expect.EmptyAssets, ",") {
				t.Fatalf("empty=%v want %v", got.EmptyAssets, testCase.Expect.EmptyAssets)
			}
			if got.OK() && len(got.Reasons) != 0 {
				t.Fatalf("HEALTHY carried reasons: %v", got.Codes())
			}
			if !got.OK() && len(got.Reasons) == 0 {
				t.Fatal("a non-HEALTHY assessment must carry at least one reason")
			}
		})
	}
}

func TestSharedPatterns(t *testing.T) {
	var document struct {
		Valid   []string `json:"valid"`
		Invalid []struct {
			Pattern string `json:"pattern"`
			Why     string `json:"why"`
		} `json:"invalid"`
		Matches []struct {
			Pattern string          `json:"pattern"`
			Matches map[string]bool `json:"matches"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(fixture(t, "patterns.json"), &document); err != nil {
		t.Fatalf("patterns.json: %v", err)
	}

	for _, pattern := range document.Valid {
		if err := policy.ValidatePattern(pattern); err != nil {
			t.Errorf("pattern %q is in the language but was rejected: %v", pattern, err)
		}
	}
	for _, rejected := range document.Invalid {
		if err := policy.ValidatePattern(rejected.Pattern); err == nil {
			t.Errorf("pattern %q must be rejected (%s)", rejected.Pattern, rejected.Why)
		}
	}
	if len(document.Matches) == 0 {
		t.Fatal("patterns.json has no match rows")
	}
	for _, row := range document.Matches {
		if err := policy.ValidatePattern(row.Pattern); err != nil {
			t.Fatalf("row pattern %q must be valid: %v", row.Pattern, err)
		}
		for name, want := range row.Matches {
			if strings.Contains(name, "/") {
				t.Fatalf("asset name %q contains /, which GitHub release assets cannot", name)
			}
			got, err := policy.MatchAsset(row.Pattern, name)
			if err != nil {
				t.Fatalf("policy.MatchAsset(%q, %q): %v", row.Pattern, name, err)
			}
			if got != want {
				t.Errorf("policy.MatchAsset(%q, %q)=%v want %v", row.Pattern, name, got, want)
			}
		}
	}
}

func TestPatternValidationRejectsReservedCharacters(t *testing.T) {
	for _, pattern := range []string{"a[b", "a\\b", "a/b", "a!b", "a^b", "]",
		"a\nb", "\t", ""} {
		if err := policy.ValidatePattern(pattern); err == nil {
			t.Errorf("pattern %q must be rejected", pattern)
		}
	}
}

func TestRequiredAssetsMirrorsThePlanner(t *testing.T) {
	declared, err := policy.Parse([]byte(`{"kind":"binary","versioning":{"mode":"manual","version":"1.0.0"},"assets":{"required":["app"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	got := RequiredAssets(declared)
	want := []string{"app", "RELEASE-METADATA.json", "SHA256SUMS"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("required=%v want %v", got, want)
	}

	everythingOff, err := policy.Parse([]byte(`{"kind":"binary","versioning":{"mode":"manual","version":"1.0.0"},"assets":{"required":["app"]},"checksums":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := RequiredAssets(everythingOff); len(got) != 2 {
		t.Fatalf("checksums disabled required=%v", got)
	}
}

func TestMatchAssetRefusesAnUnvalidatedPattern(t *testing.T) {
	// Silence is the failure mode this avoids: an unsupported pattern must not
	// quietly mean "no match".
	if _, err := policy.MatchAsset("app-[0-9].tgz", "app-1.tgz"); err == nil {
		t.Fatal("an unsupported pattern must be an error, not a non-match")
	}
}

func TestUnusablePatternIsABrokenPolicyNotADegradedRelease(t *testing.T) {
	// Same outcome as release_infra.health: BROKEN + policy_unparsable.
	// Policies are validated when loaded, so this only happens for a policy
	// assembled in memory -- and must not quietly mean "no match".
	assembled := &policy.Policy{Assets: policy.Assets{Required: []string{"app-[0-9].tgz"}}}
	got := Assess(assembled, Observation{Release: "v1.0.0", HasPolicy: true, PolicyParsable: true})
	if got.Health != "BROKEN" || got.Codes()[0] != "policy_unparsable" {
		t.Fatalf("health=%s reasons=%v", got.Health, got.Codes())
	}
}
