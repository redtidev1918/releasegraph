import re
import unittest
from pathlib import Path


class WorkflowTest(unittest.TestCase):
    def test_fleet_audit_cannot_trigger_its_own_dashboard_commit(self):
        workflow = Path(".github/workflows/fleet-audit.yml").read_text()
        event_block = workflow.split("permissions:", 1)[0]
        self.assertNotIn("\n  push:", event_block)

    def test_release_steps_use_same_job_numeric_plan_gate(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        self.assertNotIn("needs.plan.outputs.run_release", workflow)
        self.assertGreaterEqual(workflow.count("steps.plan.outputs.run_release == '1'"), 10)
        self.assertIn("if: always() && needs.build.result == 'success'", workflow)

    def test_third_party_actions_use_full_pinned_shas(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        for action in re.findall(r"uses:\s*([^\s#]+)@([0-9a-f]+)", workflow):
            self.assertEqual(len(action[1]), 40, action)

    def test_central_release_attaches_metadata(self):
        workflow = Path(".github/workflows/infra-release.yml").read_text()
        self.assertIn("release_infra.cli publish", workflow)
        self.assertLess(workflow.index("release_infra.cli audit"), workflow.index("git tag -f v1"))
        for target in ("linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"):
            self.assertIn(target, workflow)

    def test_readonly_plan_workflow_cannot_dispatch(self):
        workflow = Path(".github/workflows/reusable-readonly-plan.yml").read_text()
        self.assertNotIn("gh workflow run", workflow)
        self.assertNotIn("repository-dispatch", workflow)
        self.assertNotIn("GITHUB_TOKEN:\n        required:", workflow)
        self.assertIn("path: .releasegraph-engine", workflow)
        self.assertIn("./releasegraph fleet", workflow)
        self.assertIn("contents: read", workflow)
        self.assertIn('plan --graph "$GRAPH_PATH" --live', workflow)

    def test_docs_workflow_uses_pinned_actions(self):
        workflow = Path(".github/workflows/docs.yml").read_text()
        for action in re.findall(r"uses:\s*([^\s#]+)@([0-9a-f]+)", workflow):
            self.assertEqual(len(action[1]), 40, action)
        self.assertIn("pages: write", workflow)

    def test_release_please_only_manages_version(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        self.assertIn("skip-github-release: true", workflow)

    def test_artifact_transfer_has_bounded_retries(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        self.assertEqual(workflow.count("actions/upload-artifact@"), 4)
        self.assertEqual(workflow.count("actions/download-artifact@"), 4)
        self.assertIn("sleep 60", workflow)

    def test_finalize_configures_git_identity_for_annotated_release_tags(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        finalize = workflow.split("  finalize:", 1)[1]
        self.assertIn('git config user.name "github-actions[bot]"', finalize)
        self.assertIn(
            'git config user.email "41898282+github-actions[bot]@users.noreply.github.com"',
            finalize,
        )

    def test_finalize_can_reconcile_release_pull_request_labels(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        finalize = workflow.split("  finalize:", 1)[1].split("  release_summary:", 1)[0]
        permissions = finalize.split("steps:", 1)[0]
        self.assertIn("pull-requests: write", permissions)

    def test_reusable_workflow_exposes_release_please_component_paths(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        self.assertIn("paths_released:", workflow)
        self.assertIn("jobs.release_please.outputs.paths_released", workflow)
        self.assertIn("steps.rp.outputs.paths_released", workflow)

    def test_every_forwarded_output_exists_on_its_job(self):
        # A workflow_call output whose value points at a job output that does not
        # exist resolves to the empty string for the CALLER, with no error
        # anywhere in this repository. So the reference is checked structurally.
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        call = workflow.split("    outputs:", 1)[1].split("    inputs:", 1)[0]
        forwarded = re.findall(r"value: \$\{\{ jobs\.([\w-]+)\.outputs\.(\w+) \}\}", call)
        self.assertGreaterEqual(len(forwarded), 6, "caller outputs disappeared")
        for job, name in forwarded:
            with self.subTest(output=name, job=job):
                section = workflow.split(f"\n  {job}:\n", 1)
                self.assertEqual(len(section), 2, f"job {job} does not exist")
                body = section[1].split("\n    steps:", 1)[0]
                self.assertIn(f"      {name}: ", body, f"job {job} has no output {name}")

    def test_callers_can_see_the_plan_verdict(self):
        # 14 repositories call this workflow. Until these were exposed, a caller
        # could learn `paths_released` and nothing else -- not even whether the
        # run decided the repository was healthy.
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        call = workflow.split("    outputs:", 1)[1].split("    inputs:", 1)[0]
        for exposed in ("release_health", "version", "tag", "tag_drift", "run_release"):
            with self.subTest(output=exposed):
                self.assertIn(f"\n      {exposed}:\n", call)
        self.assertIn("jobs.build-plan.outputs.release_health", call)

    def test_finalize_configures_node_registry_before_npm_publication(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        finalize = workflow.split("  finalize:", 1)[1]
        publication = finalize.index("Required registry publication")
        setup = finalize.index("actions/setup-node@")
        self.assertLess(setup, publication)
        self.assertIn("registry-url: https://registry.npmjs.org/", finalize)
        # The toolchain step must be selected by the plan's structured channel
        # flag, never by searching the publish command for a substring: a policy
        # that publishes with `npx --yes npm@11 publish` never contains
        # "npm publish", so the step silently skipped and the publication ran
        # without a registry token.
        node_step = finalize[setup:publication]
        self.assertIn("steps.plan.outputs.npm_publish_enabled == '1'", node_step)
        self.assertNotIn("required_publish", node_step)

    def test_required_registry_verification_retries_registry_propagation(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        finalize = workflow.split("  finalize:", 1)[1]
        verification = finalize.split("Required registry verification", 1)[1].split("      - name:", 1)[0]
        self.assertIn("for attempt in 1 2 3 4 5 6", verification)
        self.assertIn("sleep $((attempt * 10))", verification)

    def test_npm_publish_receives_configured_token(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        finalize = workflow.split("  finalize:", 1)[1]
        publication = finalize.split("Required registry publication", 1)[1].split("      - name:", 1)[0]
        self.assertIn("NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }}", publication)

    def test_healthy_public_repair_skips_mutation_and_runs_audit(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        finalize = workflow.split("  finalize:", 1)[1]
        self.assertIn("steps.plan.outputs.release_health != 'healthy'", finalize)
        self.assertIn('release_infra.cli audit --version', finalize)


if __name__ == "__main__":
    unittest.main()


class BranchContractWorkflowTest(unittest.TestCase):
    """Structure invariants of the reusable production-operation branch contract gate."""

    def test_branch_contract_gate_is_reusable_read_only_and_hard_failing(self):
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        self.assertIn("workflow_call:", workflow)
        self.assertIn("contents: read", workflow)
        # Ancestry decisions need full git topology evidence.
        self.assertIn("fetch-depth: 0", workflow)
        self.assertIn("branch-contract check", workflow)
        self.assertIn("path: .releasegraph-engine", workflow)
        # No escape hatches: the gate must be able to fail the PR.
        self.assertNotIn("continue-on-error", workflow)

    def test_branch_contract_gate_never_mutates_the_production_branch(self):
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        for forbidden in (
            "git rebase",
            "git push --force",
            "git push -f",
            "git switch --create",
            "git commit",
            "branch-contract new",
        ):
            self.assertNotIn(forbidden, workflow)

    def test_branch_contract_gate_pins_all_actions(self):
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        actions = re.findall(r"uses:\s*([^\s#]+)@([^\s#]+)", workflow)
        self.assertTrue(actions)
        for action, ref in actions:
            if action == "redtidev1918/releasegraph/.github/workflows/reusable-branch-contract.yml":
                continue
            self.assertEqual(len(ref), 40, (action, ref))

    @staticmethod
    def _engine_checkout_block() -> str:
        """Locate the engine checkout step — the actions/checkout step whose
        with: block contains path: .releasegraph-engine — by step boundaries,
        so the consumer-repository checkout earlier in the same workflow can
        never be mistaken for it."""
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        lines = workflow.splitlines()
        anchor = next(i for i, line in enumerate(lines) if "path: .releasegraph-engine" in line)
        start = max(i for i in range(anchor) if lines[i].startswith("      - "))
        end = next((i for i in range(anchor + 1, len(lines)) if lines[i].startswith("      - ")), len(lines))
        return "\n".join(lines[start:end])

    def test_branch_contract_engine_checkout_is_pinned_to_the_workflow_commit(self):
        """The reusable workflow definition and the ReleaseGraph engine it
        executes MUST come from the same immutable commit: repository and ref
        of the engine checkout derive from the reusable workflow's own job
        context (job.workflow_repository / job.workflow_sha), never from a
        hardcoded repository/ref pair."""
        block = self._engine_checkout_block()
        self.assertIn("repository: ${{ job.workflow_repository }}", block)
        self.assertIn("ref: ${{ job.workflow_sha }}", block)
        self.assertNotIn("repository: redtidev1918/releasegraph", block)
        for mutable in ("ref: v1", "ref: main", "ref: master", "ref: latest"):
            self.assertNotIn(mutable, block)

    def test_branch_contract_engine_sha_tracks_caller_pin_not_mutable_channel(self):
        """Semantic regression for the provenance incident this guards against:
        a consumer pins the reusable workflow @SHA-A; later the v1 channel
        moves to SHA-B. If the engine checkout resolved a mutable channel, the
        caller's immutable pin would silently execute SHA-B. The contract is
        that the engine ref IS the caller's pin (job.workflow_sha), so
        workflow version == engine version by construction."""
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        self.assertIn("ref: ${{ job.workflow_sha }}", workflow)
        self.assertNotIn("ref: v1", workflow)
        self.assertNotIn("ref: main", workflow)
        self.assertNotIn("ref: master", workflow)

    def test_branch_contract_runtime_asserts_engine_provenance(self):
        """Static invariants are enforced at CI authoring time; the workflow
        also asserts at runtime that the checked-out engine HEAD equals the
        workflow commit, and fails closed (empty SHA or mismatch -> exit 1)
        instead of falling back to a mutable ref."""
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        self.assertIn("name: Assert engine provenance", workflow)
        self.assertIn("WORKFLOW_SHA: ${{ job.workflow_sha }}", workflow)
        self.assertIn("git -C .releasegraph-engine rev-parse HEAD", workflow)
        self.assertIn('[ -z "$WORKFLOW_SHA" ]', workflow)
        self.assertIn("exit 1", workflow)

    def test_branch_contract_gate_evaluates_the_pr_head_commit(self):
        """The gate must evaluate the actual PR head commit
        (github.event.pull_request.head.sha), never the merge commit — whose
        ancestry against the base is trivially clean — and never the head
        branch name, which does not resolve inside a merge-ref checkout."""
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        self.assertIn("EVENT_HEAD_SHA: ${{ github.event.pull_request.head.sha }}", workflow)
        self.assertIn('--head-sha "$HEAD_SHA"', workflow)
        # The env file is written from a grouped redirect (SC2129) around the
        # whole resolve step, so assert both the export and its destination
        # instead of a single-line redirect that no longer exists.
        self.assertIn('echo "HEAD_SHA=$EVENT_HEAD_SHA"', workflow)
        self.assertIn('} >> "$GITHUB_ENV"', workflow)

    def test_branch_contract_gate_attaches_diff_proof(self):
        workflow = Path(".github/workflows/reusable-branch-contract.yml").read_text()
        self.assertIn("branch-contract.txt", workflow)
        self.assertIn("actions/upload-artifact@", workflow)
        self.assertIn("if: always()", workflow)


class ProviderReconciliationWorkflowTest(unittest.TestCase):
    """Ordering invariants of the version provider reconciliation layer."""

    def test_ghcr_build_receives_full_release_identity(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        ghcr = workflow.split("Publish GHCR image", 1)[1].split("Required registry verification", 1)[0]
        for arg in ("APP_VERSION=${{ steps.plan.outputs.version }}",
                    "GIT_SHA=${{ github.sha }}",
                    "BUILD_DATE=${{ steps.identity.outputs.build_date }}"):
            self.assertIn(arg, ghcr, f"GHCR build-arg missing: {arg}")
        self.assertIn("org.opencontainers.image.created=", ghcr)
        identity = workflow.index("Stamp release identity")
        self.assertLess(identity, workflow.index("Publish GHCR image"),
                        "the stamped build date must exist before the image build")

    def test_provider_reconcile_runs_before_release_please(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        reconcile = workflow.index("Provider pre-reconcile")
        action = workflow.index(".release-please-action/dist/index.js")
        self.assertLess(reconcile, action, "provider pre-reconcile must precede release-please")
        self.assertIn("provider reconcile --repo", workflow)
        self.assertIn("--apply", workflow)

    def test_release_please_retries_only_transient_github_api_failures(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        release_please = workflow.split("  build-plan:", 1)[0]
        self.assertIn("repository: googleapis/release-please-action", release_please)
        self.assertIn("ref: 45996ed1f6d02564a971a2fa1b5860e934307cf7", release_please)
        self.assertIn("node .release-please-action/dist/index.js", release_please)
        self.assertIn("for attempt in 1 2 3 4", release_please)
        self.assertIn("Something went wrong while executing your query", release_please)
        self.assertIn("API rate limit exceeded", release_please)
        self.assertIn("grep -Eqi", release_please)
        self.assertIn("non-transient or exhausted error", release_please)

    def test_provider_ack_runs_only_after_release_is_published_and_audited(self):
        workflow = Path(".github/workflows/reusable-release.yml").read_text()
        ack = workflow.index("Provider acknowledgement")
        audit = workflow.index("release_infra.cli audit")
        publish = workflow.index("Publish, set Latest, audit, then prune Release objects")
        self.assertLess(publish, ack, "ACK must follow the publish/audit transaction")
        self.assertLess(audit, ack, "ACK must follow the release audit")
        ack_block = workflow[ack:]
        self.assertIn("provider reconcile", ack_block)
        self.assertIn("--apply", ack_block)
        # A non-dry run on a real branch only: never ACK from a pull request.
        self.assertIn("github.event_name != 'pull_request'", ack_block)

    def test_provider_watchdog_never_rewrites_release_history(self):
        workflow = Path(".github/workflows/provider-watchdog.yml").read_text()
        self.assertIn("provider inspect", workflow)
        self.assertIn("provider reconcile", workflow)
        self.assertIn("--apply", workflow)
        for forbidden in ("gh release delete", "gh release edit", "git push --force", "cleanup-tag", "git tag -f"):
            self.assertNotIn(forbidden, workflow)
        # The scheduled run is the one allowed to mutate, and only labels.
        self.assertIn("contents: read", workflow)
        self.assertIn("issues: write", workflow)
        self.assertIn("actions/create-github-app-token@", workflow)
        self.assertIn("RELEASEGRAPH_FLEET_TOKEN: ${{ steps.fleet-token.outputs.token }}", workflow)
        self.assertNotIn("PROFILE_REPO_TOKEN", workflow)

    def test_fleet_rollout_uses_ephemeral_app_token_and_plans_first(self):
        workflow = Path(".github/workflows/fleet-rollout.yml").read_text()
        self.assertIn("actions/create-github-app-token@", workflow)
        self.assertIn("RELEASEGRAPH_FLEET_TOKEN: ${{ steps.fleet-token.outputs.token }}", workflow)
        self.assertIn("permission-contents: write", workflow)
        self.assertIn("permission-pull-requests: write", workflow)
        self.assertIn("permission-workflows: write", workflow)
        self.assertLess(workflow.index("rollout plan"), workflow.index("rollout apply"))
        self.assertIn("if: ${{ inputs.apply }}", workflow)


class PRLifecycleWorkflowTest(unittest.TestCase):
    """The daily enforcement of the pull-request lifecycle contract.

    Open pull requests are a merge queue, not a backlog, so this is the one
    schedule that acts fleet-wide. The invariants below are the ones that make
    that safe: a bounded blast radius, least-privilege writes, and no history
    rewriting or branch deletion anywhere in the job.
    """

    WORKFLOW = Path(".github/workflows/pr-lifecycle.yml")

    def setUp(self):
        self.workflow = self.WORKFLOW.read_text()

    def test_it_is_scheduled_daily_and_applies_the_contract(self):
        self.assertIn('cron: "41 4 * * *"', self.workflow)
        self.assertIn("pr-lifecycle audit", self.workflow)
        self.assertIn("pr-lifecycle apply --limit", self.workflow)
        # A manual run consults; only the schedule (or an explicit input) acts.
        self.assertIn("github.event_name == 'schedule' || inputs.apply", self.workflow)
        self.assertIn("default: false", self.workflow)

    def test_the_blast_radius_of_one_pass_is_bounded(self):
        # Archiving and closing are batched by the run, not by how many pull
        # requests happen to be stale on the day the schedule first fires.
        self.assertIn("LIMIT: ${{ inputs.limit || '1' }}", self.workflow)
        self.assertIn("--limit \"$LIMIT\"", self.workflow)
        # The contract may close pull requests, so its first passes are a
        # canary: at most one pull request leaves the queue per repository,
        # which a human can verify against the archive issue before the limit
        # is raised.
        self.assertIn('default: "1"', self.workflow)

    def test_the_app_token_is_the_narrowest_one_that_can_work(self):
        for granted in (
            "permission-metadata: read",
            "permission-contents: read",
            "permission-pull-requests: write",
            "permission-issues: write",
            "permission-actions: read",
            # A commit's check runs are the Checks API, not the Actions API.
            # Without this read every pull request would look untested and green
            # work would age into PARKED.
            "permission-checks: read",
        ):
            with self.subTest(permission=granted):
                self.assertIn(granted, self.workflow)
        for forbidden in (
            "permission-contents: write",
            "permission-administration",
            "permission-secrets",
            "permission-workflows",
            "permission-actions: write",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, self.workflow)
        self.assertIn("contents: read", self.workflow)

    def test_it_never_merges_deletes_a_branch_or_rewrites_history(self):
        for forbidden in (
            "gh pr merge",
            "git merge",
            "git rebase",
            "git push --force",
            "git push -f",
            "git tag -f",
            "git branch -D",
            "git branch -d",
            "deleteBranch: true",
            "gh api -X DELETE",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, self.workflow)

    def test_every_action_is_pinned_to_a_full_commit_sha(self):
        actions = re.findall(r"uses:\s*([^\s#]+)@([^\s#]+)", self.workflow)
        self.assertTrue(actions)
        for action, ref in actions:
            with self.subTest(action=action):
                self.assertEqual(len(ref), 40, (action, ref))

    def test_the_dashboard_is_fed_by_a_read_only_token(self):
        # The column in STATUS.md is rendered by release_infra from the Go
        # sidecar, so the audit workflow must produce the sidecar -- and must be
        # unable to mutate anything while doing it.
        dashboard = Path(".github/workflows/fleet-audit.yml").read_text()
        self.assertIn("pr-lifecycle audit", dashboard)
        self.assertIn("--report pr-lifecycle.json", dashboard)
        self.assertIn("permission-checks: read", dashboard)
        self.assertNotIn("permission-pull-requests: write", dashboard)
        self.assertNotIn("permission-issues: write", dashboard)
        self.assertNotIn("pr-lifecycle apply", dashboard)
