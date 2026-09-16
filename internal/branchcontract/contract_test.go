package branchcontract

import (
	"context"
	"fmt"
	"strings"
	"testing"

	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

// fakeGit simulates the git topology evidence.
type fakeGit struct {
	baseSHA   string
	headSHA   string
	mergeBase string
	commits   int
	files     []string
	failRev   string
}

func (f *fakeGit) RevParse(rev string) (string, error) {
	if f.failRev != "" && strings.Contains(rev, f.failRev) {
		return "", fmt.Errorf("unknown revision %s", rev)
	}
	switch {
	case strings.HasPrefix(rev, "origin/"), rev == "main", rev == "master", rev == "prod":
		return f.baseSHA, nil
	default:
		return f.headSHA, nil
	}
}

func (f *fakeGit) HasRef(rev string) bool { return strings.HasPrefix(rev, "origin/") }

func (f *fakeGit) MergeBase(a, b string) (string, error) { return f.mergeBase, nil }

func (f *fakeGit) RevListCount(from, to string) (int, error) { return f.commits, nil }

func (f *fakeGit) DiffNames(from, to string) ([]string, error) { return f.files, nil }

func contractPolicy() *policy.Policy {
	return &policy.Policy{
		Kind:       "binary",
		Versioning: policy.Versioning{Mode: "manual", Version: "1.0.0"},
		Repository: policy.Repository{Git: policy.GitPolicy{ProductionOperations: &policy.ProductionOperations{
			Base:     "default",
			Branches: []string{"chore/cutover-*", "ops/*", "release/*", "hotfix/*"},
			Operations: map[string]policy.OperationScope{
				"cutover": {Branches: []string{"chore/cutover-*"}, AllowedPaths: []string{"fly/*.toml", ".github/workflows/**"}},
			},
		}}},
	}
}

var ctx = context.Background()

func TestEvaluateFeatureBranchSkipped(t *testing.T) {
	git := &fakeGit{baseSHA: "b", headSHA: "h", mergeBase: "b"}
	for _, head := range []string{"feat/provider", "feat/provider-tests", "main", "chore/regular-cleanup"} {
		result, err := Evaluate(Input{HeadRef: head, BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
		if err != nil {
			t.Fatal(err)
		}
		if result.Matched || !result.Compliant || len(result.Violations) != 0 {
			t.Fatalf("head %q: %+v", head, result)
		}
	}
}

func TestEvaluateNoPolicyConfigured(t *testing.T) {
	p := &policy.Policy{Kind: "none", Versioning: policy.Versioning{Mode: "manual", Version: "1.0.0"}}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-x", BaseRef: "main", Policy: p}, &fakeGit{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched || !result.Compliant {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateCutoverFromLatestMainPasses(t *testing.T) {
	git := &fakeGit{baseSHA: "ecc4103deadbeef", headSHA: "cutover1", mergeBase: "ecc4103deadbeef", commits: 1, files: []string{"fly/service.toml"}}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-provider-fly", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || !result.Compliant || len(result.Violations) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if result.Base != "main" || result.Evidence == nil || result.Evidence.BaseSHA != "ecc4103deadbeef" ||
		result.Evidence.MergeBaseSHA != "ecc4103deadbeef" || result.Evidence.CommitCount != 1 ||
		len(result.Evidence.ChangedFiles) != 1 {
		t.Fatalf("evidence=%+v", result.Evidence)
	}
}

func TestEvaluateCutoverFromFeatureFails(t *testing.T) {
	// A---B (main) <- F1---F2 (feat) <- D (cutover): merge-base is F1, not main HEAD.
	git := &fakeGit{baseSHA: "mainB", headSHA: "cutoverD", mergeBase: "featF1", commits: 2,
		files: []string{"fly/service.toml"}}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-provider-fly", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched || result.Compliant {
		t.Fatalf("expected violation, got %+v", result)
	}
	if len(result.Violations) != 1 || result.Violations[0].Code != ViolationAncestry {
		t.Fatalf("violations=%+v", result.Violations)
	}
	if result.Violations[0].Advice == "" {
		t.Fatal("ancestry violation must carry recovery advice")
	}
}

func TestEvaluateMainAdvancedFails(t *testing.T) {
	// PR opened legally from main@A; main then advanced to B.
	git := &fakeGit{baseSHA: "mainB", headSHA: "cutoverD", mergeBase: "mainA", commits: 1, files: []string{"fly/service.toml"}}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-provider-fly", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compliant || len(result.Violations) != 1 || result.Violations[0].Code != ViolationAncestry {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateSquashMergedParentFails(t *testing.T) {
	// content(S) ~= content(F1+F2) but commit identity differs: merge-base(F2's
	// child D, main) stays A because D descends from F2, not from S.
	git := &fakeGit{baseSHA: "squashS", headSHA: "cutoverD", mergeBase: "mainA", commits: 3, files: []string{"fly/service.toml"}}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-provider-fly", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compliant || len(result.Violations) != 1 || result.Violations[0].Code != ViolationAncestry {
		t.Fatalf("squash equivalence must not count as ancestry: %+v", result)
	}
}

func TestEvaluateTargetsFeatureAsBaseFails(t *testing.T) {
	git := &fakeGit{baseSHA: "mainB", headSHA: "cutoverD", mergeBase: "mainB", commits: 1, files: []string{"fly/service.toml"}}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-provider-fly", BaseRef: "feat/provider", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compliant || len(result.Violations) != 1 || result.Violations[0].Code != ViolationBaseTarget {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateScopeViolationFails(t *testing.T) {
	git := &fakeGit{baseSHA: "mainB", headSHA: "cutoverD", mergeBase: "mainB", commits: 2,
		files: []string{"fly/service.toml", "src/implementation.go"}}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-provider-fly", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compliant || len(result.Violations) != 1 || result.Violations[0].Code != ViolationScope {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Violations[0].Detail, "src/implementation.go") {
		t.Fatalf("scope violation must name offending files: %+v", result.Violations[0])
	}
}

func TestEvaluateScopeNotEnforcedForOtherBranches(t *testing.T) {
	// ops/* matches the contract but no operation scope declares allowedPaths for it.
	git := &fakeGit{baseSHA: "mainB", headSHA: "opsD", mergeBase: "mainB", commits: 1,
		files: []string{"anything/else.go"}}
	result, err := Evaluate(Input{HeadRef: "ops/fly-executor", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compliant || len(result.Violations) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateExplicitBaseNotDefault(t *testing.T) {
	p := contractPolicy()
	p.Repository.Git.ProductionOperations.Base = "prod"
	git := &fakeGit{baseSHA: "prod1", headSHA: "rel1", mergeBase: "prod1", commits: 1, files: []string{"x"}}
	result, err := Evaluate(Input{HeadRef: "release/v1", BaseRef: "prod", DefaultBranch: "main", Policy: p}, git)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compliant || result.Base != "prod" {
		t.Fatalf("result=%+v", result)
	}
}

func TestEvaluateDefaultBaseWithoutDefaultBranchIsConfigError(t *testing.T) {
	_, err := Evaluate(Input{HeadRef: "chore/cutover-x", BaseRef: "main", DefaultBranch: "", Policy: contractPolicy()}, &fakeGit{})
	if !rgerrors.IsKind(err, rgerrors.Policy) {
		t.Fatalf("expected policy error, got %v", err)
	}
}

func TestEvaluateGitFailureIsError(t *testing.T) {
	git := &fakeGit{baseSHA: "b", headSHA: "h", mergeBase: "b", failRev: "origin/main"}
	_, err := Evaluate(Input{HeadRef: "chore/cutover-x", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err == nil {
		t.Fatal("expected error when base cannot be resolved")
	}
}

func TestEvaluateExplicitHeadSHASkipsBranchResolution(t *testing.T) {
	// Regression for the pilot end-to-end finding: inside a pull_request
	// checkout the head branch name does not resolve (the checkout contains
	// the merge commit, not the branch). With an explicit HeadSHA the
	// evaluator must never try to resolve HeadRef as a revision, and the
	// merge commit itself must never be evaluated.
	git := &fakeGit{baseSHA: "mainB", headSHA: "mergeCommit", mergeBase: "mainB", commits: 1,
		files: []string{"fly/service.toml"}, failRev: "chore/cutover-x"}
	result, err := Evaluate(Input{HeadRef: "chore/cutover-x", HeadSHA: "prHeadSha", BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compliant {
		t.Fatalf("explicit head SHA must be evaluated: %+v", result)
	}
	if result.Evidence == nil || result.Evidence.HeadSHA != "prHeadSha" {
		t.Fatalf("evidence must carry the explicit head SHA: %+v", result.Evidence)
	}
}

func TestEvaluateAllProductionPatternsMatch(t *testing.T) {
	git := &fakeGit{baseSHA: "b", headSHA: "h", mergeBase: "b", commits: 1}
	for _, head := range []string{"release/v1", "hotfix/urgent", "ops/fly-executor", "chore/cutover-x"} {
		result, err := Evaluate(Input{HeadRef: head, BaseRef: "main", DefaultBranch: "main", Policy: contractPolicy()}, git)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Matched || !result.Compliant {
			t.Fatalf("head %q: %+v", head, result)
		}
	}
}
