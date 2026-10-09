import unittest
from unittest import mock

from release_infra import cli, release


class PrepareTagTest(unittest.TestCase):
    def prepare(self, *, apply=False, remote=None, head="source", base="source", mode="manual", version="1.5.19", dirty="", configured_base=None):
        policy = {"versioning": {"mode": mode, "version": "1.5.19"}}
        if configured_base:
            policy["repository"] = {"git": {"productionOperations": {"base": configured_base}}}
        with mock.patch.object(release, "load_policy", return_value=policy), \
                mock.patch.object(release, "_run", side_effect=[dirty, head, f"{base}\tHEAD"]) as run, \
                mock.patch.object(release, "_remote_tag_commit", return_value=remote), \
                mock.patch.object(release, "_ensure_tag") as ensure:
            result = release.prepare_tag("policy", version, apply=apply)
            return result, run.call_args_list, ensure.call_args_list

    def test_plan_does_not_mutate(self):
        result, calls, mutations = self.prepare()
        self.assertEqual(result["action"], "create-tag")
        self.assertFalse(result["apply"])
        self.assertEqual(mutations, [])
        self.assertEqual(calls, [mock.call(["git", "status", "--porcelain", "--untracked-files=no"], capture=True),
                                mock.call(["git", "rev-parse", "HEAD"], capture=True),
                                mock.call(["git", "ls-remote", "origin", "HEAD"], capture=True)])

    def test_apply_uses_existing_tag_conflict_primitive(self):
        result, _, mutations = self.prepare(apply=True)
        self.assertTrue(result["apply"])
        self.assertEqual(mutations, [mock.call("v1.5.19", "source")])

    def test_existing_tag_at_same_commit_is_noop(self):
        result, _, mutations = self.prepare(remote="source", apply=True)
        self.assertEqual(result["action"], "none")
        self.assertEqual(mutations, [])

    def test_uncommitted_policy_is_rejected(self):
        with self.assertRaisesRegex(release.ReleaseError, "clean tracked working tree"):
            self.prepare(apply=True, dirty=" M .release-policy.yml")

    def test_configured_production_base_is_resolved_remotely(self):
        _, calls, _ = self.prepare(configured_base="stable")
        self.assertEqual(calls[-1], mock.call(["git", "ls-remote", "origin", "refs/heads/stable"], capture=True))

    def test_existing_tag_conflict_is_never_applied(self):
        with self.assertRaisesRegex(release.ReleaseError, "points to old"):
            self.prepare(apply=True, remote="old")

    def test_stale_or_stacked_source_is_rejected(self):
        with self.assertRaisesRegex(release.ReleaseError, "production-base HEAD"):
            self.prepare(apply=True, base="new")

    def test_other_version_and_provider_are_rejected(self):
        with self.assertRaisesRegex(release.ReleaseError, "reviewed policy"):
            self.prepare(version="1.5.20")
        with self.assertRaisesRegex(release.ReleaseError, "manual version provider"):
            self.prepare(mode="release-please")

    def test_cli_defaults_to_plan(self):
        with mock.patch.object(cli, "prepare_tag", return_value={}) as prepare, mock.patch("builtins.print"):
            self.assertEqual(cli.main(["prepare-tag", "--version", "v1.5.19"]), 0)
        prepare.assert_called_once_with(".release-policy.yml", "v1.5.19", apply=False)

    def test_cli_apply_is_explicit(self):
        with mock.patch.object(cli, "prepare_tag", return_value={}) as prepare, mock.patch("builtins.print"):
            self.assertEqual(cli.main(["prepare-tag", "--version", "1.5.19", "--apply"]), 0)
        prepare.assert_called_once_with(".release-policy.yml", "1.5.19", apply=True)
