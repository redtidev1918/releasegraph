package cli

import (
	"flag"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/branchcontract"
	rgdomain "github.com/redtidev1918/releasegraph/internal/domain"
	rgerrors "github.com/redtidev1918/releasegraph/internal/errors"
	"github.com/redtidev1918/releasegraph/internal/policy"
)

// branchContractCommand implements the production-operation branch contract
// CLI. Local runs and CI share the same core evaluator.
func branchContractCommand(w io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("branch-contract requires a subcommand: check or new")
	}
	switch args[0] {
	case "check":
		return branchContractCheck(w, args[1:])
	case "new":
		return branchContractNew(w, args[1:])
	default:
		return fmt.Errorf("unknown branch-contract subcommand %q (want check or new)", args[0])
	}
}

func branchContractCheck(w io.Writer, args []string) error {
	var format, policyPath, head, headSHA, base, defaultBranch string
	fs := flags(&format)
	fs.StringVar(&policyPath, "path", ".release-policy.yml", "policy path")
	fs.StringVar(&head, "head", "", "head branch ref (default: current branch)")
	fs.StringVar(&headSHA, "head-sha", "", "exact head commit to evaluate; CI passes the PR head SHA because a pull_request checkout contains the merge commit, not the head branch (default: resolve --head as a revision)")
	fs.StringVar(&base, "base", "", "declared pull-request base branch (default: resolved production base)")
	fs.StringVar(&defaultBranch, "default-branch", "", "repository default branch (default: resolved from origin/HEAD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	p, err := policy.Load(policyPath)
	if err != nil {
		return err
	}
	if head == "" {
		current, err := currentBranch()
		if err != nil {
			return rgerrors.Wrap(rgerrors.Policy, "resolve current branch (pass --head)", err)
		}
		head = current
	}
	if defaultBranch == "" {
		defaultBranch = resolveDefaultBranch()
	}
	if base == "" {
		base = productionBaseRef(p, defaultBranch)
	}
	input := branchcontract.Input{HeadRef: head, HeadSHA: headSHA, BaseRef: base, DefaultBranch: defaultBranch, Policy: p}
	result, err := branchcontract.Evaluate(input, branchcontract.ExecGit{})
	if err != nil {
		return err
	}
	if result.Matched && !result.Compliant {
		exitCode = rgdomain.ExitBlocked
	}
	return write(w, format, result, func(w io.Writer, _ any) {
		humanBranchContract(w, input, &result)
	})
}

// branchContractNew is the convenience creation helper: fetch the production
// base, verify a clean worktree, and create the branch from the exact remote
// base HEAD. It is convenience only; enforcement lives in CI.
func branchContractNew(w io.Writer, args []string) error {
	fs := flag.NewFlagSet("branch-contract new", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var policyPath, from string
	fs.StringVar(&policyPath, "path", ".release-policy.yml", "policy path")
	fs.StringVar(&from, "from", "", "branch to create from (default: policy production base, else resolved default branch)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	name := fs.Arg(0)
	if name == "" {
		return fmt.Errorf("branch-contract new requires a branch name")
	}
	p, err := policy.Load(policyPath)
	if err != nil {
		return err
	}
	if from == "" {
		from = productionBaseRef(p, resolveDefaultBranch())
	}
	if from == "" {
		return rgerrors.New(rgerrors.Policy, "cannot determine production base (pass --from <branch>)")
	}

	if out, err := gitOut("status", "--porcelain"); err != nil {
		return rgerrors.Wrap(rgerrors.Policy, "check worktree", err)
	} else if strings.TrimSpace(out) != "" {
		return rgerrors.New(rgerrors.Policy, "worktree is not clean; commit or stash before creating a production-operation branch")
	}
	fmt.Fprintf(w, "fetch origin %s\n", from)
	if _, err := gitOut("fetch", "origin", from); err != nil {
		return rgerrors.Wrap(rgerrors.Policy, fmt.Sprintf("fetch origin %s", from), err)
	}
	sha, err := gitOut("rev-parse", "--verify", "origin/"+from+"^{commit}")
	if err != nil {
		sha, err = gitOut("rev-parse", "--verify", from+"^{commit}")
		if err != nil {
			return rgerrors.Wrap(rgerrors.Policy, fmt.Sprintf("resolve %s HEAD", from), err)
		}
	}
	if _, err := gitOut("switch", "--create", name, strings.TrimSpace(sha)); err != nil {
		return rgerrors.Wrap(rgerrors.Policy, fmt.Sprintf("create branch %s", name), err)
	}
	fmt.Fprintf(w, "CREATED %s @ %s (from %s)\n", name, strings.TrimSpace(sha), from)
	return nil
}

// productionBaseRef resolves the policy production base to a concrete branch
// name, honoring base: default.
func productionBaseRef(p *policy.Policy, defaultBranch string) string {
	po := p.Repository.Git.ProductionOperations
	if po == nil {
		return ""
	}
	if strings.EqualFold(po.Base, "default") {
		return defaultBranch
	}
	return po.Base
}

// resolveDefaultBranch resolves the repository default branch from the
// local checkout without hardcoding "main".
func resolveDefaultBranch() string {
	out, err := currentGit().Output()
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(out))
	if idx := strings.LastIndex(ref, "/"); idx >= 0 {
		return ref[idx+1:]
	}
	return ref
}

func currentBranch() (string, error) {
	out, err := gitOut("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(out)
	if branch == "HEAD" {
		return "", fmt.Errorf("detached HEAD")
	}
	return branch, nil
}

func currentGit() *exec.Cmd {
	return exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
}

func gitOut(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	return string(out), err
}

func humanBranchContract(w io.Writer, input branchcontract.Input, result *branchcontract.Result) {
	if !result.Matched {
		fmt.Fprintln(w, "SKIP not a production-operation branch (feature branches are not evaluated)")
		return
	}
	if result.Compliant {
		fmt.Fprintln(w, "PASS production-operation branch contract")
	} else {
		fmt.Fprintln(w, "FAIL production-operation branch contract")
	}
	if ev := result.Evidence; ev != nil {
		fmt.Fprintf(w, "  head: %s\n", input.HeadRef)
		if input.BaseRef != "" {
			fmt.Fprintf(w, "  declared PR base: %s\n", input.BaseRef)
		}
		fmt.Fprintf(w, "  required base: %s (%s) @ %s\n", ev.ProductionBase, ev.BaseRef, shortSHA(ev.BaseSHA))
		fmt.Fprintf(w, "  merge-base: %s\n", shortSHA(ev.MergeBaseSHA))
		fmt.Fprintf(w, "  commit count: %d\n", ev.CommitCount)
		fmt.Fprintf(w, "  changed files (%d):\n", len(ev.ChangedFiles))
		for _, file := range ev.ChangedFiles {
			fmt.Fprintf(w, "    - %s\n", file)
		}
	}
	for _, violation := range result.Violations {
		fmt.Fprintf(w, "\n%s\n%s\n", violation.Message, violation.Detail)
		if violation.Advice != "" {
			fmt.Fprintf(w, "\n%s\n", violation.Advice)
		}
	}
	if result.Compliant {
		return
	}
	fmt.Fprintln(w, "\nThis pull request cannot merge until the production-operation branch contract holds.")
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
