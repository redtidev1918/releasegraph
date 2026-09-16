package policy

import "testing"

func TestLoadJSONPolicyAndComponentDesiredVersion(t *testing.T) {
	p, err := Load("../../testdata/policy/binary.json")
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != "binary" || !p.Checksums || p.Hash == "" {
		t.Fatalf("policy=%+v", p)
	}
	got, err := DesiredVersion(p, "", "../../testdata/policy")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.4.1" {
		t.Fatalf("desired=%q", got)
	}
}

func TestExplicitVersionNormalization(t *testing.T) {
	p := &Policy{Kind: "binary", Versioning: Versioning{Mode: "manual", Version: "1.2.3"}, Checksums: true}
	got, err := DesiredVersion(p, "v1.2.3", ".")
	if err != nil || got != "1.2.3" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestUnknownRegistryRejected(t *testing.T) {
	p := &Policy{Kind: "none", Versioning: Versioning{Mode: "manual", Version: "1.0.0"}, Registries: map[string]Registry{"bad": {}}, Checksums: true}
	if err := Validate(p); err == nil {
		t.Fatal("expected policy error")
	}
}

func TestReleaseTagPreservesLegacyTemplate(t *testing.T) {
	p := &Policy{Tag: Tag{Template: "cli-v{version}"}}
	if got := ReleaseTag(p, "1.2.3"); got != "cli-v1.2.3" {
		t.Fatalf("tag=%q", got)
	}
}

func TestRepositoryPolicyLoads(t *testing.T) {
	p, err := Load("../../.release-policy.yml")
	if err != nil {
		t.Fatal(err)
	}
	if p.APIVersion != "releasegraph.dev/v1" || p.Retention.Stable != 1 || !p.Metadata {
		t.Fatalf("policy=%+v", p)
	}
}

func validProductionOperations() *ProductionOperations {
	return &ProductionOperations{
		Base:     "default",
		Branches: []string{"chore/cutover-*", "ops/*", "release/*", "hotfix/*"},
		Operations: map[string]OperationScope{
			"cutover": {Branches: []string{"chore/cutover-*"}, AllowedPaths: []string{"fly/*.toml", ".github/workflows/**"}},
		},
	}
}

func TestProductionOperationsValidation(t *testing.T) {
	base := func() *Policy {
		return &Policy{Kind: "none", Versioning: Versioning{Mode: "manual", Version: "1.0.0"}, Repository: Repository{Git: GitPolicy{ProductionOperations: validProductionOperations()}}}
	}
	if err := Validate(base()); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Policy)
	}{
		{"empty base", func(p *Policy) { p.Repository.Git.ProductionOperations.Base = "" }},
		{"no branch patterns", func(p *Policy) { p.Repository.Git.ProductionOperations.Branches = nil }},
		{"requireLatestBase false", func(p *Policy) { v := false; p.Repository.Git.ProductionOperations.RequireLatestBase = &v }},
		{"invalid glob", func(p *Policy) { p.Repository.Git.ProductionOperations.Branches = []string{"chore/cutover-["} }},
		{"operation without branches", func(p *Policy) { p.Repository.Git.ProductionOperations.Operations["x"] = OperationScope{} }},
		{"operation invalid allowedPath", func(p *Policy) {
			p.Repository.Git.ProductionOperations.Operations["cutover"] = OperationScope{Branches: []string{"chore/cutover-*"}, AllowedPaths: []string{"a["}}
		}},
	}
	for _, tc := range cases {
		p := base()
		tc.mutate(p)
		if err := Validate(p); err == nil {
			t.Errorf("%s: expected policy error", tc.name)
		}
	}
}

func TestRequireLatestDefaultsTrue(t *testing.T) {
	po := validProductionOperations()
	if !po.RequireLatest() {
		t.Fatal("nil RequireLatestBase must default to enforced")
	}
	v := true
	po.RequireLatestBase = &v
	if !po.RequireLatest() {
		t.Fatal("explicit true must stay enforced")
	}
}

func validPRLifecycle() *PRLifecycle {
	return &PRLifecycle{
		Exempt: PRExemptions{
			Branches: []string{"release-please--*"},
			Actors:   []string{"github-actions[bot]", "dependabot[bot]"},
			Labels:   []string{"keep-open"},
		},
	}
}

func TestPRLifecycleValidation(t *testing.T) {
	base := func() *Policy {
		return &Policy{
			Kind:       "none",
			Versioning: Versioning{Mode: "manual", Version: "1.0.0"},
			Repository: Repository{PullRequests: PullRequests{Lifecycle: validPRLifecycle()}},
		}
	}
	if err := Validate(base()); err != nil {
		t.Fatalf("valid lifecycle rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Policy)
	}{
		{"deleteBranch true", func(p *Policy) { v := true; p.Repository.PullRequests.Lifecycle.DeleteBranch = &v }},
		{"parkedAfterDays zero", func(p *Policy) { v := 0; p.Repository.PullRequests.Lifecycle.ParkedAfterDays = &v }},
		{"parkedAfterDays negative", func(p *Policy) { v := -1; p.Repository.PullRequests.Lifecycle.ParkedAfterDays = &v }},
		{"parkedAfterDays too large", func(p *Policy) { v := 366; p.Repository.PullRequests.Lifecycle.ParkedAfterDays = &v }},
		{"close without archive", func(p *Policy) {
			closeParked, archive := true, false
			p.Repository.PullRequests.Lifecycle.CloseParked = &closeParked
			p.Repository.PullRequests.Lifecycle.ArchiveParkedToIssue = &archive
		}},
		{"invalid exempt branch glob", func(p *Policy) { p.Repository.PullRequests.Lifecycle.Exempt.Branches = []string{"release-please--["} }},
		{"empty exempt actor", func(p *Policy) { p.Repository.PullRequests.Lifecycle.Exempt.Actors = []string{""} }},
		{"multiline exempt label", func(p *Policy) { p.Repository.PullRequests.Lifecycle.Exempt.Labels = []string{"keep\nopen"} }},
	}
	for _, tc := range cases {
		p := base()
		tc.mutate(p)
		if err := Validate(p); err == nil {
			t.Errorf("%s: expected policy error", tc.name)
		}
	}
}

// An explicit false is how a repository declares the contract without letting
// it act. The archived-issue rule is what keeps that opt-out honest: closing
// parked work without recording it is never a valid policy.
func TestPRLifecycleArchiveOnlyIsValid(t *testing.T) {
	closeParked := false
	lifecycle := validPRLifecycle()
	lifecycle.CloseParked = &closeParked
	p := &Policy{Kind: "none", Versioning: Versioning{Mode: "manual", Version: "1.0.0"}, Repository: Repository{PullRequests: PullRequests{Lifecycle: lifecycle}}}
	if err := Validate(p); err != nil {
		t.Fatalf("archive-only lifecycle rejected: %v", err)
	}
	if lifecycle.ClosesParked() {
		t.Fatal("closeParked=false must not close parked pull requests")
	}
}

func TestPRLifecycleDefaults(t *testing.T) {
	var absent *PRLifecycle
	if absent.IsEnabled() {
		t.Fatal("an undeclared contract must not be enabled")
	}
	if absent.ParkedAfter() != DefaultParkedAfterDays {
		t.Fatalf("default window=%d", absent.ParkedAfter())
	}
	if !absent.ClosesParked() || !absent.ArchivesToIssue() {
		t.Fatal("a declared contract must default to archive-then-close")
	}
	declared := validPRLifecycle()
	if !declared.IsEnabled() {
		t.Fatal("a declared contract is enforced unless explicitly disabled")
	}
	disabled := false
	declared.Enabled = &disabled
	if declared.IsEnabled() {
		t.Fatal("enabled=false must not be enforced")
	}
}

func TestRetentionPruneStableIsOptInByDefault(t *testing.T) {
	p, err := Load("../../.release-policy.yml")
	if err != nil {
		t.Fatal(err)
	}
	if p.Retention.PruneStable {
		t.Fatal("published stable releases are history; pruning them must be opt-in")
	}
}

func TestNotesLanguageIsValidatedLikeThePythonHalf(t *testing.T) {
	base := Policy{
		Kind:       "none",
		Versioning: Versioning{Mode: "manual", Version: "1.0.0"},
		Assets:     Assets{Required: []string{}},
		Checksums:  true,
	}
	for _, language := range []string{"", "auto", "en", "zh"} {
		p := base
		p.Release.Notes.Language = language
		if err := Validate(&p); err != nil {
			t.Fatalf("language %q: %v", language, err)
		}
	}
	p := base
	p.Release.Notes.Language = "fr"
	if err := Validate(&p); err == nil {
		t.Fatal("expected a policy error for an unknown release.notes.language")
	}
}

func TestRetentionWithoutPruneStableStillLoads(t *testing.T) {
	p, err := Parse([]byte(`{"apiVersion":"releasegraph.dev/v1","kind":"none",` +
		`"versioning":{"mode":"manual","version":"1.0.0"},"assets":{"required":[]},` +
		`"registries":{},"retention":{"stable":1,"prerelease":1,"failed_draft":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Retention.PruneStable || p.Retention.Stable != 1 {
		t.Fatalf("retention=%+v", p.Retention)
	}
}

func TestPostReleaseValidation(t *testing.T) {
	base := func(actions []PostReleaseAction) *Policy {
		return &Policy{Kind: "none", Versioning: Versioning{Mode: "manual", Version: "1.0.0"},
			Release: Release{PostRelease: actions}}
	}
	valid := []PostReleaseAction{{ID: "refresh-docs", Type: "github-workflow", Required: true,
		Workflow: "update-download-page.yml", Inputs: map[string]string{"tag": "{{tag}}"}}}
	if err := Validate(base(valid)); err != nil {
		t.Fatalf("valid postRelease rejected: %v", err)
	}
	cases := []string{
		"missing id", "duplicated id", "unknown type", "missing workflow for github-workflow",
	}
	bad := [][]PostReleaseAction{
		{{Type: "github-workflow", Workflow: "x"}},
		{{ID: "a", Type: "github-workflow", Workflow: "x"}, {ID: "a", Type: "github-workflow", Workflow: "x"}},
		{{ID: "a", Type: "smoke"}},
		{{ID: "a", Type: "github-workflow"}},
	}
	for i, actions := range bad {
		if err := Validate(base(actions)); err == nil {
			t.Fatalf("case %d (%s) accepted", i, cases[i])
		}
	}
}
