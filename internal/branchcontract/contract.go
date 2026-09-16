// Package branchcontract implements the Production-Operation Branch Contract:
// branches that perform a production state transition (cutovers, releases,
// hotfixes, ops changes) must target the configured production base directly
// and must descend from its current HEAD. Feature branches may stack and are
// never evaluated.
//
// The check is a pure function of git topology: git merge-base(head, base)
// must equal git rev-parse(base). Squash merges preserve content, not commit
// identity, so a cutover built on a squash-merged feature never satisfies the
// ancestry invariant — by design.
package branchcontract

import (
	"fmt"
	"sort"
	"strings"

	rgdomain "github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/glob"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

// Violation codes are stable machine-readable identifiers.
const (
	ViolationBaseTarget = "BRANCH_CONTRACT_BASE_TARGET"
	ViolationAncestry   = "BRANCH_CONTRACT_ANCESTRY"
	ViolationScope      = "BRANCH_CONTRACT_SCOPE"
)

// Input declares what the evaluator needs. Git data is resolved through the
// GitRunner; policy comes from the repository .release-policy.yml.
type Input struct {
	// HeadRef is the PR head branch name (e.g. "chore/cutover-provider-fly").
	// It selects policy pattern matching; the evaluated commit is HeadSHA
	// when provided, otherwise HeadRef is resolved as a revision.
	HeadRef string
	// HeadSHA optionally pins the exact head commit. CI passes
	// github.event.pull_request.head.sha: a pull_request checkout contains
	// the merge commit, not the head branch, so the branch name may not
	// resolve there — and the merge commit itself must never be evaluated
	// (its ancestry against the base is trivially clean). Callers that
	// evaluate a real local branch may leave this empty.
	HeadSHA string
	// BaseRef is the PR base branch name as declared (e.g. "main").
	// Empty means the caller could not observe it; only the ancestry
	// invariant is checked in that case.
	BaseRef string
	// DefaultBranch is the repository default branch, used to resolve the
	// policy's base: default. May be empty when the caller cannot observe it.
	DefaultBranch string
	// Policy is the loaded repository policy.
	Policy *policy.Policy
}

// Violation is one failed invariant with actionable guidance.
type Violation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  string `json:"detail,omitempty"`
	Advice  string `json:"advice,omitempty"`
}

// Evidence is the diff proof attached to every evaluation of a matched
// production-operation branch.
type Evidence struct {
	ProductionBase string   `json:"productionBase"`
	BaseRef        string   `json:"baseRef"`
	BaseSHA        string   `json:"baseSha,omitempty"`
	HeadSHA        string   `json:"headSha,omitempty"`
	MergeBaseSHA   string   `json:"mergeBaseSha,omitempty"`
	CommitCount    int      `json:"commitCount,omitempty"`
	ChangedFiles   []string `json:"changedFiles,omitempty"`
}

// Result is the outcome of one evaluation.
type Result struct {
	// Matched reports whether the head branch is a production-operation
	// branch. Feature branches are skipped (Matched=false, Compliant=true).
	Matched bool `json:"matched"`
	// Compliant reports whether all invariants hold.
	Compliant bool   `json:"compliant"`
	Base      string `json:"base,omitempty"`
	// Outcome is the stable machine-readable code: RG_HEALTHY when the
	// branch is skipped or compliant, RG_BRANCH_CONTRACT_VIOLATED otherwise.
	Outcome    string      `json:"outcome"`
	Violations []Violation `json:"violations,omitempty"`
	Evidence   *Evidence   `json:"evidence,omitempty"`
}

// Evaluate runs the contract. It returns a Result for every well-formed
// input; errors are reserved for unusable git evidence or configuration.
func Evaluate(in Input, git GitRunner) (Result, error) {
	po := productionOperations(in.Policy)
	if po == nil {
		return Result{Matched: false, Compliant: true, Outcome: rgdomain.CodeHealthy}, nil
	}
	matched, err := matchesAny(po.Branches, in.HeadRef)
	if err != nil {
		return Result{}, err
	}
	if !matched {
		return Result{Matched: false, Compliant: true, Outcome: rgdomain.CodeHealthy}, nil
	}

	base := po.Base
	if strings.EqualFold(base, "default") {
		if in.DefaultBranch == "" {
			return Result{}, rgerrors.New(rgerrors.Policy, `base: default requires the repository default branch, but none was provided`)
		}
		base = in.DefaultBranch
	}
	result := Result{Matched: true, Base: base}

	// Invariant 1: the PR must target the production base directly.
	if in.BaseRef != "" && in.BaseRef != base {
		result.Violations = append(result.Violations, Violation{
			Code:    ViolationBaseTarget,
			Message: fmt.Sprintf("production-operation branch targets base %q; the production base is %q", in.BaseRef, base),
			Detail: fmt.Sprintf("A branch that performs a production state transition must be opened against %q directly. "+
				"Targeting another branch makes the transition depend on state that has not entered production.", base),
			Advice: fmt.Sprintf("Reopen or retarget the pull request so its base branch is %q.", base),
		})
	}

	// Invariant 2: the branch must descend from the current HEAD of the
	// production base. The evaluated head commit is the explicit HeadSHA when
	// provided (CI), otherwise the HeadRef revision (local runs).
	baseRef := resolveBaseRef(git, base)
	baseSHA, err := git.RevParse(baseRef)
	if err != nil {
		return Result{}, rgerrors.Wrap(rgerrors.Policy, fmt.Sprintf("resolve production base %q", baseRef), err)
	}
	headCommit := in.HeadSHA
	if headCommit == "" {
		headCommit, err = git.RevParse(in.HeadRef)
		if err != nil {
			return Result{}, rgerrors.Wrap(rgerrors.Policy, fmt.Sprintf("resolve head %q", in.HeadRef), err)
		}
	}
	mergeBase, err := git.MergeBase(headCommit, baseRef)
	if err != nil {
		return Result{}, rgerrors.Wrap(rgerrors.Policy, fmt.Sprintf("merge-base %q %q", in.HeadRef, baseRef), err)
	}
	commitCount, diffFiles := 0, []string{}
	if mergeBase != "" {
		if commitCount, err = git.RevListCount(mergeBase, headCommit); err != nil {
			return Result{}, err
		}
		if diffFiles, err = git.DiffNames(mergeBase, headCommit); err != nil {
			return Result{}, err
		}
	}
	result.Evidence = &Evidence{
		ProductionBase: base,
		BaseRef:        baseRef,
		BaseSHA:        baseSHA,
		HeadSHA:        headCommit,
		MergeBaseSHA:   mergeBase,
		CommitCount:    commitCount,
		ChangedFiles:   diffFiles,
	}

	if mergeBase != baseSHA {
		if mergeBase == "" {
			result.Violations = append(result.Violations, Violation{
				Code:    ViolationAncestry,
				Message: fmt.Sprintf("production-operation branch shares no history with the production base %q", base),
				Advice:  recreateAdvice(base),
			})
		} else {
			result.Violations = append(result.Violations, Violation{
				Code: ViolationAncestry,
				Message: fmt.Sprintf("production-operation branch is not based on the latest production base %s@%s (merge-base is %s)",
					base, short(baseSHA), short(mergeBase)),
				Detail: fmt.Sprintf("This branch appears to depend on commits that are not part of the production base history — "+
					"typically another branch that has not entered %q. Squash merges preserve content, not commit identity, "+
					"so a branch built on a squash-merged feature is still not based on the current production base.", base),
				Advice: recreateAdvice(base),
			})
		}
	}

	// Invariant 3 (optional, repo policy): the change scope of the operation.
	for _, violation := range evaluateScopes(po, in.HeadRef, diffFiles) {
		result.Violations = append(result.Violations, violation)
	}

	result.Compliant = len(result.Violations) == 0
	if result.Compliant {
		result.Outcome = rgdomain.CodeHealthy
	} else {
		result.Outcome = rgdomain.CodeBranchContractViolated
	}
	return result, nil
}

// evaluateScopes applies every declared operation scope whose branch patterns
// match the head ref. A scope with declared allowedPaths requires every
// changed file to match at least one pattern; undeclared scopes (no branch
// match, or no allowedPaths) are not enforced.
func evaluateScopes(po *policy.ProductionOperations, headRef string, changedFiles []string) []Violation {
	violations := []Violation{}
	for _, name := range operationNames(po) {
		scope := po.Operations[name]
		matched, err := matchesAny(scope.Branches, headRef)
		if err != nil || !matched || len(scope.AllowedPaths) == 0 {
			continue
		}
		offending := []string{}
		for _, file := range changedFiles {
			allowed := false
			for _, pattern := range scope.AllowedPaths {
				ok, err := glob.Match(pattern, file)
				if err != nil {
					continue
				}
				if ok {
					allowed = true
					break
				}
			}
			if !allowed {
				offending = append(offending, file)
			}
		}
		if len(offending) == 0 {
			continue
		}
		sort.Strings(offending)
		violations = append(violations, Violation{
			Code:    ViolationScope,
			Message: fmt.Sprintf("production-operation %q carries changes outside its declared scope (%d file(s))", name, len(offending)),
			Detail: fmt.Sprintf("A production state transition is a configuration/state change, not an implementation drop. "+
				"Files outside the declared scope: %s.", strings.Join(offending, ", ")),
			Advice: fmt.Sprintf("Move implementation changes to a feature branch; keep %q limited to the paths declared under operations.%s.allowedPaths.", headRef, name),
		})
	}
	return violations
}

func operationNames(po *policy.ProductionOperations) []string {
	names := make([]string, 0, len(po.Operations))
	for name := range po.Operations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func recreateAdvice(base string) string {
	return fmt.Sprintf("Recreate the production-operation branch from the latest %s: "+
		"1) git fetch origin %s; 2) create a fresh branch from the exact %s HEAD "+
		"(releasegraph branch-contract new <name>); 3) cherry-pick or reapply only the intended production transition; "+
		"4) verify the diff; 5) replace or update this pull request.", base, base, base)
}

func productionOperations(p *policy.Policy) *policy.ProductionOperations {
	if p == nil {
		return nil
	}
	return p.Repository.Git.ProductionOperations
}

func matchesAny(patterns []string, name string) (bool, error) {
	for _, pattern := range patterns {
		ok, err := glob.Match(pattern, name)
		if err != nil {
			return false, rgerrors.New(rgerrors.Policy, fmt.Sprintf("invalid branch glob %q: %v", pattern, err))
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
