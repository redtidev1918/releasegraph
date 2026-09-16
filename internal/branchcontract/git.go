package branchcontract

import (
	"fmt"
	"os/exec"
	"strings"
)

// GitRunner exposes the read-only git topology evidence the contract
// requires. Merge decisions come exclusively from git topology
// (merge-base / rev-parse): never from commit messages, PR titles,
// content similarity or patch equivalence.
type GitRunner interface {
	// RevParse resolves a revision to its full object name.
	RevParse(rev string) (string, error)
	// HasRef reports whether a revision resolves at all.
	HasRef(rev string) bool
	// MergeBase returns the best common ancestor of two revisions.
	MergeBase(a, b string) (string, error)
	// RevListCount counts commits reachable from to but not from.
	RevListCount(from, to string) (int, error)
	// DiffNames lists files changed between two revisions.
	DiffNames(from, to string) ([]string, error)
}

// ExecGit runs git against Dir using the git binary. It is the production
// implementation; tests substitute a fake.
type ExecGit struct {
	Dir string
}

func (g ExecGit) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if g.Dir != "" {
		cmd.Dir = g.Dir
	}
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n"), err
}

func (g ExecGit) RevParse(rev string) (string, error) {
	out, err := g.run("rev-parse", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", rev, err)
	}
	return out, nil
}

func (g ExecGit) HasRef(rev string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if g.Dir != "" {
		cmd.Dir = g.Dir
	}
	return cmd.Run() == nil
}

func (g ExecGit) MergeBase(a, b string) (string, error) {
	out, err := g.run("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("merge-base %s %s: %w", a, b, err)
	}
	return out, nil
}

func (g ExecGit) RevListCount(from, to string) (int, error) {
	out, err := g.run("rev-list", "--count", from+".."+to)
	if err != nil {
		return 0, fmt.Errorf("rev-list --count %s..%s: %w", from, to, err)
	}
	var n int
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		return 0, fmt.Errorf("rev-list --count %s..%s: unexpected output %q", from, to, out)
	}
	return n, nil
}

func (g ExecGit) DiffNames(from, to string) ([]string, error) {
	out, err := g.run("diff", "--name-only", from, to)
	if err != nil {
		return nil, fmt.Errorf("diff --name-only %s %s: %w", from, to, err)
	}
	files := []string{}
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// resolveBaseRef prefers the remote-tracking ref for the production base so
// the check reflects the remote state ("origin/main"), falling back to the
// local ref when no remote-tracking ref exists.
func resolveBaseRef(git GitRunner, base string) string {
	remote := "origin/" + base
	if git.HasRef(remote) {
		return remote
	}
	return base
}
