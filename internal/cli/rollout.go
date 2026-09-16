package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/redtidev1918/releasegraph/internal/credential"
	"github.com/redtidev1918/releasegraph/internal/domain"
	"github.com/redtidev1918/releasegraph/internal/fleet"
	"github.com/redtidev1918/releasegraph/internal/github"
	"github.com/redtidev1918/releasegraph/internal/rollout"
)

// rolloutCommand implements:
//
//	releasegraph rollout status [--version vX.Y.Z]      # current pins + canary
//	releasegraph rollout plan   --version vX.Y.Z        # side-effect free
//	releasegraph rollout apply  --version vX.Y.Z --apply # opens upgrade PRs
//
// Rollout is a fleet (control plane) operation: it needs
// RELEASEGRAPH_FLEET_TOKEN and reads its repository list from fleet.yaml.
func rolloutCommand(w io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: releasegraph rollout [status|plan|apply]")
	}
	sub := args[0]
	var format, manifestPath, version, releaseRepo, rollbackTo, rollbackFrom string
	var apply bool
	var only []string
	fs := flags(&format)
	fs.Var((*repoList)(&only), "repos", "limit the rollout to these repositories (batch migration)")
	fs.StringVar(&manifestPath, "manifest", "fleet.yaml", "fleet manifest")
	fs.StringVar(&version, "version", "", "target ReleaseGraph version, e.g. v1.4.1")
	fs.StringVar(&rollbackTo, "to", "", "rollback target version (rollback only)")
	fs.StringVar(&rollbackFrom, "from", "", "bad version to roll back from (rollback only)")
	fs.StringVar(&releaseRepo, "release-repo", "redtidev1918/releasegraph", "repository whose releases define versions")
	fs.BoolVar(&apply, "apply", false, "open upgrade pull requests (default: plan only)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if sub != "status" && sub != "plan" && sub != "apply" && sub != "rollback" {
		return fmt.Errorf("unknown rollout subcommand %q", sub)
	}

	ctx := context.Background()
	if err := credential.RequireFleet("rollout " + sub); err != nil {
		return err
	}
	if err := credential.UseFleetCredential(); err != nil {
		return err
	}
	client := github.New().Bind(domain.ExecutionContext{
		Scope:           domain.ScopeFleet,
		Actor:           actorName(),
		CredentialClass: domain.CredentialFleet,
	})

	manifest, err := fleet.LoadManifest(manifestPath)
	if err != nil {
		return err
	}

	if sub == "rollback" {
		return rolloutRollback(ctx, w, format, client, manifest, releaseRepo, rollbackFrom, rollbackTo, apply, only)
	}
	if version == "" {
		if sub == "apply" {
			return fmt.Errorf("rollout apply requires --version")
		}
		version, err = latestStableVersion(ctx, client, releaseRepo)
		if err != nil {
			return err
		}
	}
	if !rollout.MutableChannel.MatchString(version) && !strings.HasPrefix(version, "v") {
		version = "v" + version
	}

	managed, _ := resolveManaged(ctx, client, manifest)
	// Resolve the target version to its commit once: pins are commits, and both
	// the plan and the canary gate compare against that commit.
	targetCommit, commitErr := resolveRefCommit(ctx, client, releaseRepo, version)
	if commitErr != nil {
		targetCommit = ""
	}
	entries := rollout.InspectPins(ctx, client, manifest, managed, version)
	for i := range entries {
		entries[i].TargetCommit = targetCommit
	}
	canary, _ := manifest.Canary()
	evidence := canaryEvidence(ctx, client, canary, version, targetCommit)
	compat, compatNote := fetchCompatibility(ctx, client, releaseRepo, version)
	if compatNote != "" && format != "json" {
		fmt.Fprintf(w, "note: %s\n", compatNote)
	}
	plan := rollout.BuildPlan(manifest, entries, version, canary, evidence, compat)

	if len(only) > 0 {
		plan = limitPlan(plan, only)
	}

	if sub == "plan" || sub == "status" {
		if format == "json" {
			return write(w, "json", plan, nil)
		}
		humanRolloutPlan(w, plan)
		return nil
	}

	// apply
	if !apply {
		if format == "json" {
			return write(w, "json", map[string]any{"mode": "dry-run", "plan": plan}, nil)
		}
		humanRolloutPlan(w, plan)
		fmt.Fprintln(w, "\ndry-run only; pass --apply to open upgrade pull requests")
		return nil
	}
	if targetCommit == "" {
		return fmt.Errorf("cannot resolve %s to a commit in %s", version, releaseRepo)
	}
	return rolloutApply(ctx, w, format, client, plan, version, targetCommit)
}

// fetchCompatibility reads the target release's own compatibility metadata. When
// it is absent the release is treated as affecting every repository: skipping a
// repository is only ever based on declared facts.
func fetchCompatibility(ctx context.Context, client *github.Bound, repo, version string) (rollout.Compatibility, string) {
	// The tag annotation is the durable source: it is immutable and survives
	// release pruning, while a release asset can be removed by retention.
	if compat, ok := compatibilityFromTag(ctx, client, repo, version); ok {
		compat.Version = version
		return compat, ""
	}
	var release struct {
		Assets []struct {
			Name string `json:"name"`
			ID   int64  `json:"id"`
		} `json:"assets"`
	}
	found, err := client.GetOptional(ctx, fmt.Sprintf("repos/%s/releases/tags/%s", repo, version), &release)
	if err != nil || !found {
		return rollout.Compatibility{Version: version}, "target release " + version + " not readable; treating every repository as affected"
	}
	for _, asset := range release.Assets {
		if asset.Name != rollout.MetadataAsset {
			continue
		}
		text, ok, err := client.ReleaseAssetText(ctx, repo, asset.ID)
		if err != nil || !ok {
			break
		}
		compat, err := rollout.ParseCompatibility([]byte(text))
		if err != nil {
			return rollout.Compatibility{Version: version}, err.Error()
		}
		if compat.Version == "" {
			compat.Version = version
		}
		return compat, ""
	}
	return rollout.Compatibility{Version: version}, "release " + version + " declares no " + rollout.MetadataAsset + "; treating every repository as affected"
}

func humanRolloutPlan(w io.Writer, plan rollout.Plan) {
	fmt.Fprintf(w, "target: %s\n", plan.Target)
	if plan.Canary != "" {
		state := "NOT PASSED"
		if plan.CanaryPassed {
			state = "passed"
		}
		fmt.Fprintf(w, "canary: %s (%s", plan.Canary, state)
		if plan.CanaryReason != "" {
			fmt.Fprintf(w, " — %s", plan.CanaryReason)
		}
		fmt.Fprintln(w, ")")
	}
	for _, entry := range plan.Entries {
		marker := ""
		if entry.Canary {
			marker = " [canary]"
		}
		compat := ""
		if entry.Compat != "" && entry.Status != rollout.StatusCurrent {
			compat = " [" + entry.Compat + "]"
		}
		mutable := ""
		if entry.Current.Mutable {
			mutable = " (mutable channel alias)"
		}
		current := entry.Current.Ref
		if current == "" {
			current = "none"
		}
		fmt.Fprintf(w, "  %-42s %-17s %s -> %s%s%s%s\n", entry.Repository, entry.Status, current, entry.Target, marker, mutable, compat)
	}
	fmt.Fprintf(w, "\nready: %d  blocked: %d  unaffected: %d  migration: %d  incompatible: %d\n",
		len(plan.Ready), len(plan.Blocked), len(plan.Unaffected), len(plan.Migration), len(plan.Incompatible))
}

// resolveRefCommit resolves the version tag to the commit it points at, so the
// pin written into business workflows is always resolvable.
func resolveRefCommit(ctx context.Context, client *github.Bound, repo, version string) (string, error) {
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if _, err := client.GetOptional(ctx, fmt.Sprintf("repos/%s/git/ref/tags/%s", repo, version), &ref); err != nil {
		return "", err
	}
	if ref.Object.SHA == "" {
		return "", fmt.Errorf("cannot resolve %s in %s", version, repo)
	}
	if ref.Object.Type == "tag" {
		// Annotated tag: peel it to the commit.
		var annotated struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if err := client.Get(ctx, fmt.Sprintf("repos/%s/git/tags/%s", repo, ref.Object.SHA), &annotated); err != nil {
			return "", err
		}
		return annotated.Object.SHA, nil
	}
	return ref.Object.SHA, nil
}

func rolloutApply(ctx context.Context, w io.Writer, format string, client *github.Bound, plan rollout.Plan, version, commit string) error {
	results := []map[string]string{}
	for _, entry := range plan.Entries {
		if entry.Status != rollout.StatusReady {
			continue
		}
		pr, err := client.OpenChangePR(ctx, github.FileUpdate{
			Repo:    entry.Repository,
			Path:    rollout.WorkflowPath,
			Branch:  "chore/releasegraph-" + version,
			Message: "chore(release-infra): bump ReleaseGraph to " + version,
			Title:   "chore(release-infra): bump ReleaseGraph to " + version,
			Body: "Pins the release infrastructure to `" + version + "` (commit `" + commit + "`).\n\n" +
				"Opened by `releasegraph rollout` after the canary repository completed a release lifecycle on this version.\n" +
				"Reverting this pull request returns the repository to its previous pin.",
			Transform: func(current string) (string, error) { return rollout.RepinToCommit(current, version, commit) },
		})
		item := map[string]string{"repository": entry.Repository, "target": version}
		if err != nil {
			item["status"] = "failed"
			item["error"] = err.Error()
		} else {
			item["status"] = "pr-opened"
			item["pullRequest"] = pr
		}
		results = append(results, item)
	}
	if format == "json" {
		return write(w, "json", map[string]any{"mode": "apply", "target": version, "results": results}, nil)
	}
	for _, item := range results {
		if item["status"] == "pr-opened" {
			fmt.Fprintf(w, "opened  %s -> %s %s\n", item["repository"], version, item["pullRequest"])
		} else {
			fmt.Fprintf(w, "failed  %s: %s\n", item["repository"], item["error"])
		}
	}
	return nil
}

func resolveManaged(ctx context.Context, client *github.Bound, manifest *fleet.Manifest) ([]fleet.Resolved, []fleet.Resolved) {
	owner := ""
	if names := manifest.EnabledNames(); len(names) > 0 {
		owner, _, _ = strings.Cut(names[0], "/")
	}
	if owner == "" {
		return fleet.Resolve(manifest, nil)
	}
	discovered, err := fleet.Discover(ctx, client, owner, false)
	if err != nil {
		return fleet.Resolve(manifest, nil)
	}
	return fleet.Resolve(manifest, discovered.Repositories)
}

func canaryEvidence(ctx context.Context, client *github.Bound, canary, version, targetCommit string) rollout.CanaryEvidence {
	if canary == "" {
		return rollout.CanaryEvidence{Reason: "no canary declared in fleet.yaml"}
	}
	raw, found, err := client.ReadFile(ctx, canary, rollout.WorkflowPath, "")
	if err != nil || !found {
		return rollout.CanaryEvidence{Reason: "cannot read " + canary + " " + rollout.WorkflowPath}
	}
	pin := rollout.ParsePin(raw)
	if !pin.PinnedTo(version, targetCommit) {
		return rollout.CanaryEvidence{Reason: canary + " is pinned to " + pin.Ref + ", not " + version}
	}
	run, ok, err := client.LatestCompletedWorkflowRun(ctx, canary, "release.yml")
	if err != nil || !ok {
		return rollout.CanaryEvidence{Pinned: true, Reason: "no completed release workflow run found for " + canary}
	}
	if run.Conclusion != "success" {
		return rollout.CanaryEvidence{Pinned: true, Reason: "canary run " + run.HTMLURL + " concluded " + run.Conclusion}
	}
	return rollout.CanaryEvidence{Pinned: true, Lifecycle: true, Reason: "canary run succeeded on " + version + ": " + run.HTMLURL}
}

func latestStableVersion(ctx context.Context, client *github.Bound, repo string) (string, error) {
	var release struct {
		TagName    string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if _, err := client.GetOptional(ctx, fmt.Sprintf("repos/%s/releases/latest", repo), &release); err != nil {
		return "", err
	}
	if release.TagName == "" {
		return "", fmt.Errorf("no latest release found for %s", repo)
	}
	return release.TagName, nil
}

// limitPlan narrows a rollout to an explicit batch while keeping the canary gate
// intact: a batch can never bypass a failing canary.
func limitPlan(plan rollout.Plan, only []string) rollout.Plan {
	wanted := map[string]bool{}
	for _, name := range only {
		wanted[name] = true
	}
	limited := rollout.Plan{Target: plan.Target, Canary: plan.Canary, CanaryPassed: plan.CanaryPassed, CanaryReason: plan.CanaryReason, Entries: []rollout.Entry{}, Ready: []string{}, Blocked: []string{}}
	for _, entry := range plan.Entries {
		if !wanted[entry.Repository] {
			continue
		}
		limited.Entries = append(limited.Entries, entry)
		switch entry.Status {
		case rollout.StatusReady:
			limited.Ready = append(limited.Ready, entry.Repository)
		case rollout.StatusBlocked:
			limited.Blocked = append(limited.Blocked, entry.Repository)
		}
	}
	return limited
}

// compatibilityFromTag reads the declared scope from the annotated tag message.
// A tag push has no workflow inputs, so the annotation is where the scope
// travels with the version — and unlike a release asset it is never pruned.
func compatibilityFromTag(ctx context.Context, client *github.Bound, repo, version string) (rollout.Compatibility, bool) {
	var ref struct {
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	found, err := client.GetOptional(ctx, fmt.Sprintf("repos/%s/git/ref/tags/%s", repo, version), &ref)
	if err != nil || !found || ref.Object.Type != "tag" {
		return rollout.Compatibility{}, false
	}
	var tag struct {
		Message string `json:"message"`
	}
	if err := client.Get(ctx, fmt.Sprintf("repos/%s/git/tags/%s", repo, ref.Object.SHA), &tag); err != nil {
		return rollout.Compatibility{}, false
	}
	compat := rollout.Compatibility{Version: version}
	declared := false
	for _, line := range strings.Split(tag.Message, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "affected-capabilities", "affected_capabilities":
			compat.AffectedCapabilities = fields(value)
			declared = true
		case "breaking":
			compat.Breaking = strings.EqualFold(strings.TrimSpace(value), "true")
			declared = true
		}
	}
	if !declared {
		return rollout.Compatibility{}, false
	}
	return compat, true
}

// fields splits a comma/space separated capability list.
func fields(value string) []string {
	out := []string{}
	for _, item := range strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == ';'
	}) {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// rolloutRollback reverses a bad infrastructure pin by opening pull requests that
// pin repositories back to a known-good version. It creates a new configuration
// change and never rewrites release tags: a version stays immutable, so the fix
// is a forward change of what repositories use.
func rolloutRollback(ctx context.Context, w io.Writer, format string, client *github.Bound, manifest *fleet.Manifest, releaseRepo, fromVersion, toVersion string, apply bool, only []string) error {
	if fromVersion == "" || toVersion == "" {
		return fmt.Errorf("rollback requires --from <bad version> --to <good version>")
	}
	fromCommit, err := resolveRefCommit(ctx, client, releaseRepo, fromVersion)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", fromVersion, err)
	}
	toCommit, err := resolveRefCommit(ctx, client, releaseRepo, toVersion)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", toVersion, err)
	}
	requested := map[string]bool{}
	for _, name := range only {
		requested[name] = true
	}

	managed, _ := resolveManaged(ctx, client, manifest)
	type outcome struct {
		Repository  string `json:"repository"`
		Status      string `json:"status"`
		PullRequest string `json:"pullRequest,omitempty"`
		Reason      string `json:"reason,omitempty"`
	}
	outcomes := []outcome{}
	for _, repo := range managed {
		if len(requested) > 0 && !requested[repo.Name] {
			continue
		}
		if repo.Classification != fleet.ClassificationManaged {
			outcomes = append(outcomes, outcome{Repository: repo.Name, Status: "skipped", Reason: repo.Classification})
			continue
		}
		raw, found, err := client.ReadFile(ctx, repo.Name, rollout.WorkflowPath, "")
		if err != nil || !found {
			outcomes = append(outcomes, outcome{Repository: repo.Name, Status: "skipped", Reason: "no release workflow"})
			continue
		}
		pin := rollout.ParsePin(raw)
		if pin.Ref != fromCommit && pin.Ref != fromVersion && pin.Version != fromVersion {
			outcomes = append(outcomes, outcome{Repository: repo.Name, Status: "skipped", Reason: "not pinned to " + fromVersion})
			continue
		}
		if !apply {
			outcomes = append(outcomes, outcome{Repository: repo.Name, Status: "would-roll-back", Reason: fromVersion + " -> " + toVersion})
			continue
		}
		pr, err := client.OpenChangePR(ctx, github.FileUpdate{
			Repo:      repo.Name,
			Path:      rollout.WorkflowPath,
			Branch:    "chore/releasegraph-rollback-" + toVersion,
			Message:   "chore(release-infra): roll back ReleaseGraph to " + toVersion,
			Title:     "chore(release-infra): roll back ReleaseGraph to " + toVersion,
			Body:      "Pins the release infrastructure back to `" + toVersion + "` (commit `" + toCommit + "`) from `" + fromVersion + "`.\n\nRelease tags are immutable, so the rollback is a new configuration change rather than a tag rewrite.",
			Transform: func(current string) (string, error) { return rollout.RepinToCommit(current, toVersion, toCommit) },
		})
		if err != nil {
			outcomes = append(outcomes, outcome{Repository: repo.Name, Status: "failed", Reason: err.Error()})
			continue
		}
		outcomes = append(outcomes, outcome{Repository: repo.Name, Status: "rolled-back", PullRequest: pr})
	}

	payload := map[string]any{"mode": mode(apply), "from": fromVersion, "to": toVersion, "outcomes": outcomes}
	if format == "json" {
		return write(w, "json", payload, nil)
	}
	for _, item := range outcomes {
		switch item.Status {
		case "rolled-back":
			fmt.Fprintf(w, "rolled back  %s -> %s %s\n", item.Repository, toVersion, item.PullRequest)
		case "would-roll-back":
			fmt.Fprintf(w, "would roll back  %s (%s)\n", item.Repository, item.Reason)
		default:
			fmt.Fprintf(w, "%-10s %s: %s\n", item.Status, item.Repository, item.Reason)
		}
	}
	if !apply {
		fmt.Fprintln(w, "\ndry-run only; pass --apply to open rollback pull requests")
	}
	return nil
}
