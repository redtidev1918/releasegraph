package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
		wantErr bool
	}{
		{"chore/cutover-*", "chore/cutover-provider-fly", true, false},
		{"chore/cutover-*", "chore/other", false, false},
		{"chore/*", "chore/cutover-x", true, false},
		{"chore/*", "chore/a/b", false, false},
		{"ops/*", "ops/fly-executor", true, false},
		{"release/*", "release/v1", true, false},
		{"hotfix/*", "feat/provider", false, false},
		{"*", "main", true, false},
		{"*", "a/b", false, false},
		{".github/workflows/**", ".github/workflows/branch-contract.yml", true, false},
		{".github/workflows/**", ".github/workflows/sub/dir/f.yml", true, false},
		{".github/workflows/**", "control-plane/wrangler.toml", false, false},
		{"control-plane/wrangler.toml", "control-plane/wrangler.toml", true, false},
		{"fly/*.toml", "fly/service.toml", true, false},
		{"fly/*.toml", "fly/sub/service.toml", false, false},
		{"a?c", "abc", true, false},
		{"a?c", "a/c", false, false},
	}
	for _, tc := range cases {
		got, err := Match(tc.pattern, tc.name)
		if tc.wantErr {
			if err == nil {
				t.Errorf("Match(%q, %q) expected ErrBadPattern", tc.pattern, tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("Match(%q, %q) unexpected error: %v", tc.pattern, tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.want)
		}
	}
}

func TestReservedCharactersRejected(t *testing.T) {
	// The branch-contract language deliberately excludes character classes so
	// the accepted syntax stays unambiguous across implementations (see
	// internal/policy/pattern.go for the same decision on asset patterns).
	for _, bad := range []string{"bad[", "a!b", "a^b", `a\b`, ""} {
		if err := Check(bad); err == nil {
			t.Errorf("Check(%q) expected ErrBadPattern", bad)
		}
		if _, err := Match(bad, "x"); err == nil {
			t.Errorf("Match(%q, ...) expected ErrBadPattern", bad)
		}
	}
	for _, good := range []string{"chore/cutover-*", ".github/workflows/**", "plain", "a?c"} {
		if err := Check(good); err != nil {
			t.Errorf("Check(%q) unexpected error: %v", good, err)
		}
	}
}
