#!/usr/bin/env python3
"""Unit tests for release_infra/actions.py (post-release action engine)."""
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from release_infra import actions  # noqa: E402


def policy_fixture(post_release=None):
    return {
        "kind": "hybrid",
        "versioning": {"mode": "manual", "version": "2.7.0"},
        "tag": {"template": "v{version}"},
        "release": {"postRelease": post_release or [
            {"id": "refresh-docs", "type": "github-workflow", "required": True,
             "workflow": "update-download-page.yml", "inputs": {"tag": "{{tag}}", "deploy-docs": "false"}},
            {"id": "deploy-docs", "type": "github-workflow", "required": False,
             "workflow": "docs.yml", "inputs": {"tag": "{{tag}}"}},
        ]},
    }


class MarkerTests(unittest.TestCase):
    def test_parse_render_roundtrip(self):
        state = {"a": {"status": "success", "attempts": 1, "last_error": ""}}
        body = "notes\n\n" + actions.render_marker(state)
        self.assertEqual(actions.parse_marker(body), state)

    def test_with_marker_replaces(self):
        old = {"a": {"status": "failed", "attempts": 1, "last_error": "x"}}
        new = {"a": {"status": "success", "attempts": 2, "last_error": ""}}
        body = "hello\n" + actions.render_marker(old)
        out = actions.with_marker(body, new)
        self.assertNotIn("failed", out)
        self.assertIn('"success"', out)
        self.assertEqual(actions.parse_marker(out), new)

    def test_with_marker_appends_when_missing(self):
        out = actions.with_marker("plain body", {"a": {"status": "success", "attempts": 1, "last_error": ""}})
        self.assertTrue(out.startswith("plain body"))
        self.assertIn("releasegraph-post-release", out)

    def test_parse_marker_ignores_garbage(self):
        self.assertEqual(actions.parse_marker("<!-- releasegraph-post-release not-json -->"), {})


class ActionLoadTests(unittest.TestCase):
    def test_validation_rejects_unknown_type_and_missing_workflow(self):
        with self.assertRaises(actions.PostReleaseError):
            actions.load_actions({"release": {"postRelease": [{"id": "a", "type": "shell-command"}]}})
        with self.assertRaises(actions.PostReleaseError):
            actions.load_actions({"release": {"postRelease": [{"id": "a", "type": "github-workflow"}]}})
        with self.assertRaises(actions.PostReleaseError):
            actions.load_actions({"release": {"postRelease": [{"id": "a"}, {"id": "a"}]}})

    def test_load_normalizes(self):
        got = actions.load_actions(policy_fixture())
        self.assertEqual([a["id"] for a in got], ["refresh-docs", "deploy-docs"])
        self.assertTrue(got[0]["required"])
        self.assertFalse(got[1]["required"])

    def test_resolve_inputs_substitutes_context(self):
        ctx = {"tag": "v2.7.0", "version": "2.7.0", "repo": "owner/project",
               "release_id": "42", "release_url": "https://github.com/owner/project/releases/tag/v2.7.0",
               "head_sha": "abc", "default_branch": "main"}
        out = actions.resolve_inputs({"inputs": {"tag": "{{tag}}", "url": "{{release_url}}", "plain": "x"}}, ctx)
        self.assertEqual(out, {"tag": "v2.7.0", "url": "https://github.com/owner/project/releases/tag/v2.7.0", "plain": "x"})


class EngineTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.policy = self.root / ".release-policy.yml"
        self.policy.write_text(json.dumps(policy_fixture()), encoding="utf-8")
        self.env = mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": "owner/project"}, clear=False)
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.tmp.cleanup()

    def _release(self, body="", draft=False, published="2026-09-15T00:00:00Z"):
        return {"id": 1, "tag_name": "v2.7.0", "body": body, "draft": draft,
                "prerelease": False, "published_at": published}

    def test_run_all_actions_success(self):
        with mock.patch.object(actions, "_release_payload", return_value=self._release()), \
             mock.patch.object(actions, "_dispatch_and_wait") as dispatch, \
             mock.patch.object(actions, "_update_release_body") as update, \
             mock.patch.object(actions, "_gh", return_value="main"):
            code = actions.run(str(self.policy))
        self.assertEqual(code, 0)
        self.assertEqual(dispatch.call_count, 2)
        body = update.call_args[0][2]
        state = actions.parse_marker(body)
        self.assertEqual(state["refresh-docs"]["status"], "success")
        self.assertEqual(state["deploy-docs"]["status"], "success")

    def test_run_skips_satisfied_actions_resume(self):
        body = actions.render_marker({"refresh-docs": {"status": "success", "attempts": 1, "last_error": ""}})
        with mock.patch.object(actions, "_release_payload", return_value=self._release(body=body)), \
             mock.patch.object(actions, "_dispatch_and_wait") as dispatch, \
             mock.patch.object(actions, "_update_release_body"), \
             mock.patch.object(actions, "_gh", return_value="main"):
            code = actions.run(str(self.policy))
        self.assertEqual(code, 0)
        # Only the still-missing action dispatches.
        self.assertEqual(dispatch.call_count, 1)
        self.assertEqual(dispatch.call_args[0][1], "docs.yml")

    def test_required_failure_fails_and_records(self):
        with mock.patch.object(actions, "_release_payload", return_value=self._release()), \
             mock.patch.object(actions, "_dispatch_and_wait",
                               side_effect=actions.PostReleaseError("run concluded failure")), \
             mock.patch.object(actions, "_update_release_body") as update, \
             mock.patch.object(actions, "_gh", return_value="main"):
            code = actions.run(str(self.policy))
        self.assertEqual(code, 1)
        state = actions.parse_marker(update.call_args[0][2])
        self.assertEqual(state["refresh-docs"]["status"], "failed")
        self.assertIn("concluded failure", state["refresh-docs"]["last_error"])
        self.assertEqual(state["refresh-docs"]["attempts"], 1)

    def test_optional_failure_does_not_fail(self):
        policy = policy_fixture(post_release=[
            {"id": "only-optional", "type": "github-workflow", "workflow": "docs.yml"}])
        self.policy.write_text(json.dumps(policy), encoding="utf-8")
        with mock.patch.object(actions, "_release_payload", return_value=self._release()), \
             mock.patch.object(actions, "_dispatch_and_wait",
                               side_effect=actions.PostReleaseError("boom")), \
             mock.patch.object(actions, "_update_release_body"), \
             mock.patch.object(actions, "_gh", return_value="main"):
            code = actions.run(str(self.policy))
        self.assertEqual(code, 0)

    def test_retry_increments_attempts(self):
        body = actions.render_marker({"refresh-docs": {"status": "failed", "attempts": 2,
                                                        "last_error": "old error"}})
        with mock.patch.object(actions, "_release_payload", return_value=self._release(body=body)), \
             mock.patch.object(actions, "_dispatch_and_wait"), \
             mock.patch.object(actions, "_update_release_body") as update, \
             mock.patch.object(actions, "_gh", return_value="main"):
            actions.run(str(self.policy))
        state = actions.parse_marker(update.call_args[0][2])
        self.assertEqual(state["refresh-docs"]["attempts"], 3)
        self.assertEqual(state["refresh-docs"]["status"], "success")

    def test_draft_release_is_noop(self):
        with mock.patch.object(actions, "_release_payload", return_value=self._release(draft=True)), \
             mock.patch.object(actions, "_dispatch_and_wait") as dispatch, \
             mock.patch.object(actions, "_update_release_body") as update:
            code = actions.run(str(self.policy))
        self.assertEqual(code, 0)
        dispatch.assert_not_called()
        update.assert_not_called()


class DispatchCorrelationTests(unittest.TestCase):
    def test_dispatch_and_poll_to_success(self):
        calls = []

        def fake_subprocess(*args, **kwargs):
            argv = args[0]
            if "dispatches" in " ".join(argv):
                return unittest.mock.Mock(returncode=0, stdout="", stderr="")
            raise AssertionError(f"unexpected subprocess call: {argv}")

        def fake_gh(args, *, capture=True, retries=True):
            flat = " ".join(args)
            if "default_branch" in flat:
                return "main"
            if "workflows/update-download-page.yml/runs" in flat:
                return json.dumps([{"id": 77, "head_sha": "abc123",
                                    "created_at": "2026-09-15T00:01:00Z",
                                    "status": "completed", "conclusion": "success"}])
            if "actions/runs/77" in flat:
                return json.dumps({"status": "completed", "conclusion": "success"})
            raise AssertionError(f"unexpected gh api call: {flat}")

        subprocess_calls = []

        def recording_subprocess(*args, **kwargs):
            subprocess_calls.append(args[0])
            return fake_subprocess(*args, **kwargs)

        with mock.patch("release_infra.actions._release_payload",
                        return_value={"id": 1, "tag_name": "v2.7.0", "body": "", "draft": False,
                                      "prerelease": False, "published_at": "2026-09-15T00:00:00Z"}), \
             mock.patch("release_infra.actions._gh", side_effect=fake_gh), \
             mock.patch("release_infra.actions.subprocess.run", side_effect=recording_subprocess), \
             mock.patch("release_infra.actions._update_release_body"):
            actions._dispatch_and_wait("owner/project", "update-download-page.yml", "main",
                                       {"tag": "v2.7.0"}, "abc123", max_wait=60)
        self.assertTrue(any("dispatches" in " ".join(c) for c in subprocess_calls))


if __name__ == "__main__":
    unittest.main()