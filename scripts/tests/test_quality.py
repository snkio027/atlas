import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SOURCE = Path(__file__).resolve().parents[1] / "quality.py"
spec = importlib.util.spec_from_file_location("quality", SOURCE)
q = importlib.util.module_from_spec(spec)
spec.loader.exec_module(q)


class QualityContract(unittest.TestCase):
    def setUp(self):
        self.p = q.policy()
        self.c = {"event": "pull_request", "head": "a" * 40, "base": "b" * 40, "test": "c" * 40, "tree": "d" * 40, "dirty": False, "inputs_sha256": "e" * 64}

    def report(self, mode="full"):
        return {"schema": 1, "mode": mode, "result": "success", **q.binding(self.p, self.c), "run_id": "123", "attempt": "1", "actual_environment": {k: self.p["environment"][k] for k in ["system", "machine", "go"]}, "checks": [{"id": name, "result": "success", "seconds": 1.0} for name in self.p[mode]]}

    def validate(self, report):
        q.validate_report(report, "full", self.p, self.c, "123", "1")

    def test_full_keeps_every_previous_quality_check_and_oci(self):
        self.assertEqual(self.p["full"], ["source", "workflow", "race", "render", "ot1", "experiments", "data", "packaging", "web-oci"])
        self.validate(self.report())

    def test_missing_duplicate_failed_and_cancelled_coverage_is_rejected(self):
        for variant in ["missing", "duplicate", "failure", "cancelled", "skipped", "not_run"]:
            report = self.report()
            if variant == "missing":
                report["checks"].pop()
            elif variant == "duplicate":
                report["checks"].append(report["checks"][0])
            else:
                report["checks"][0]["result"] = variant
            with self.subTest(variant=variant), self.assertRaises(ValueError):
                self.validate(report)

    def test_candidate_environment_policy_and_attempt_cannot_be_reused(self):
        changes = [("candidate", dict(self.c, test="f" * 40)), ("environment", {}), ("actual_environment", {"system": "Linux"}), ("policy_sha256", "wrong"), ("verifier_sha256", "wrong"), ("attempt", "2"), ("run_id", "other"), ("result", "failure")]
        for key, value in changes:
            report = self.report(); report[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                self.validate(report)

    def test_draft_and_non_success_dependencies_do_not_admit(self):
        q.phase_gate("pull_request", "false", "success", "success", "success", "success")
        q.phase_gate("push", "null", "success", "success", "skipped", "success")
        q.phase_gate("merge_group", "null", "success", "success", "skipped", "success")
        for state in ["failure", "cancelled", "skipped", "neutral", "", None]:
            for field in [2, 3, 4, 5]:
                args = ["pull_request", "false", "success", "success", "success", "success"]; args[field] = state
                with self.subTest(field=field, state=state), self.assertRaises(ValueError):
                    q.phase_gate(*args)
        for draft in ["true", "", None]:
            with self.assertRaises(ValueError):
                q.phase_gate("pull_request", draft, "success", "success", "success", "success")
        with self.assertRaises(ValueError):
            q.phase_gate("unknown", "false", "success", "success", "success", "success")

    def test_title_is_validated_as_data(self):
        for title in ["fix: preserve receipts", "ci(workflow): validate candidates", "feat(api)!: change configuration"]:
            q.validate_title(title)
        for title in [None, "", "Update workflow", "fix: one\ntwo"]:
            with self.assertRaises(ValueError):
                q.validate_title(title)

    def test_duplicate_json_fields_rejected(self):
        with self.assertRaises(ValueError):
            q.strict_json('{"full_required": true, "full_required": false}')

    def test_missing_history_produces_explicit_full_without_executing_checks(self):
        with patch.object(q, "git", side_effect=subprocess.CalledProcessError(1, "git")), patch.object(q, "run_command") as execute:
            plan = q.make_plan(self.p, self.c)
            self.assertIs(plan["full_required"], True)
            self.assertEqual(plan["checks"], q.FULL)
            self.assertIn("unavailable", plan["reason"])
            q.validate_plan(plan, self.p, self.c)
            execute.assert_not_called()
            for key, value in [("checks", []), ("full_required", False), ("full_required", "true"), ("full_required", 1), ("schema", 2), ("candidate", dict(self.c, base="f" * 40))]:
                bad = copy.deepcopy(plan); bad[key] = value
                with self.subTest(key=key, value=value), self.assertRaises(ValueError):
                    q.validate_plan(bad, self.p, self.c)
            bad = dict(plan, invented=True)
            with self.assertRaises(ValueError):
                q.validate_plan(bad, self.p, self.c)

    def test_plan_covers_delete_rename_and_untracked_work(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", *args], cwd=root, stderr=subprocess.DEVNULL)
            git("init", "-q"); (root / "old").write_text("same"); (root / "deleted").write_text("gone")
            git("add", "."); git("commit", "-qm", "base"); base = git("rev-parse", "HEAD").decode().strip()
            (root / "old").rename(root / "renamed"); (root / "deleted").unlink(); git("add", "-A"); git("commit", "-qm", "change")
            head = git("rev-parse", "HEAD").decode().strip(); (root / "new-untracked").write_text("work")
            with patch.object(q, "ROOT", root):
                plan = q.make_plan(self.p, dict(self.c, base=base, head=head, dirty=True))
                self.assertEqual(plan["comparison"]["paths"], ["deleted", "new-untracked", "old", "renamed"])
                before = q.input_digest(); (root / "new-untracked").write_text("edited"); self.assertNotEqual(before, q.input_digest())
                before = q.input_digest(); (root / "renamed").chmod(0o755); self.assertNotEqual(before, q.input_digest())

    def test_missing_plan_cannot_start_full(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.dict(os.environ, {"CI_REPORT_DIR": directory, "CI_PLAN_PATH": str(Path(directory) / "absent"), "CI_PR_TITLE": "test: fixture"}), patch.object(q, "candidate", return_value=self.c), patch.object(q, "environment", return_value={k: self.p["environment"][k] for k in ["system", "machine", "go"]}), patch.object(q, "run_command") as execute:
                with self.assertRaises(FileNotFoundError):
                    q.execute("full")
                execute.assert_not_called()
                report = json.loads((Path(directory) / "full.json").read_text())
                self.assertEqual(report["result"], "failure")
                self.assertEqual(report["checks"], [])

    def test_failed_command_retains_failure_and_does_not_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            with patch.dict(os.environ, {"CI_REPORT_DIR": directory}), patch.object(q, "candidate", return_value=dict(self.c, event="local")), patch.object(q, "environment", return_value={k: self.p["environment"][k] for k in ["system", "machine", "go"]}), patch.object(q, "run_command", side_effect=ValueError("fixture failure")) as execute:
                with self.assertRaises(ValueError):
                    q.execute("fast")
                self.assertEqual(execute.call_count, 1)
                report = json.loads((Path(directory) / "fast.json").read_text())
                self.assertEqual(report["result"], "failure")
                self.assertEqual(report["checks"][0]["result"], "failure")


    def test_non_json_numbers_are_rejected(self):
        for raw in ['NaN', 'Infinity', '-Infinity', '{"duration": NaN}']:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                q.strict_json(raw)

    def test_current_pr_rejects_stale_head_base_draft_target_and_metadata(self):
        live = {"title": "ci: fixture", "state": "open", "draft": False,
                "head": {"sha": self.c["head"]}, "base": {"sha": self.c["base"], "ref": "main"}}
        with patch.dict(os.environ, {"CI_PR_TITLE": live["title"]}):
            q.validate_current_pr(live, self.c)
            variants = [{"state": "closed"}, {"draft": True}, {"title": "ci: changed"},
                        {"head": {"sha": "f" * 40}}, {"base": {"sha": "f" * 40, "ref": "main"}},
                        {"base": {"sha": self.c["base"], "ref": "other"}}]
            for changes in variants:
                with self.subTest(changes=changes), self.assertRaises(ValueError):
                    q.validate_current_pr(dict(live, **changes), self.c)

    def test_pull_request_candidate_must_be_actual_declared_merge(self):
        def git(*args):
            if args == ("rev-parse", "HEAD"):
                return (self.c["test"] + "\n").encode()
            if args == ("rev-list", "--parents", "-n", "1", "HEAD"):
                return (self.c["test"] + " " + self.c["base"] + " " + self.c["head"]).encode()
            if args == ("status", "--porcelain"):
                return b""
            if args == ("rev-parse", "HEAD^{tree}"):
                return self.c["tree"].encode()
            raise AssertionError(args)
        env = {"CI_EVENT": "pull_request", "CI_HEAD_SHA": self.c["head"], "CI_BASE_SHA": self.c["base"], "CI_TEST_SHA": self.c["test"], "CI_BASE_REF": "main"}
        with patch.dict(os.environ, env), patch.object(q, "git", side_effect=git), patch.object(q, "input_digest", return_value=self.c["inputs_sha256"]):
            self.assertEqual(q.candidate(), self.c)
            with patch.dict(os.environ, {"CI_HEAD_SHA": "f" * 40}), self.assertRaises(ValueError):
                q.candidate()

    def test_gate_requires_complete_artifact_layout_and_live_read(self):
        env = {"CI_PR_DRAFT": "false", "FAST_RESULT": "success", "VALIDATE_RESULT": "success", "PLAN_RESULT": "success", "EXECUTION_RESULT": "success", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_REPOSITORY": "fixture/atlas", "CI_PR_NUMBER": "42", "CI_PR_TITLE": "ci: fixture"}
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, env), patch.object(q, "candidate", return_value=self.c), patch.object(q, "git", side_effect=subprocess.CalledProcessError(1, "git")):
            root = Path(directory)
            plan = q.make_plan(self.p, self.c)
            for mode in ["fast", "full"]:
                report = self.report(mode)
                if mode == "full":
                    report["plan_sha256"] = q.digest(q.encode(plan))
                    q.save(root / mode / "plan.json", plan)
                q.save(root / mode / (mode + ".json"), report)
            live = {"title": "ci: fixture", "state": "open", "draft": False, "head": {"sha": self.c["head"]}, "base": {"sha": self.c["base"], "ref": "main"}}
            with patch.object(q.subprocess, "check_output", return_value=q.encode(live)):
                q.gate(root)
            with patch.object(q.subprocess, "check_output", side_effect=subprocess.CalledProcessError(1, "gh")), self.assertRaises(subprocess.CalledProcessError):
                q.gate(root)
            (root / "full/full.json").unlink()
            with self.assertRaises(FileNotFoundError):
                q.gate(root)


    def test_actionlint_rejects_runner_context_before_job_assignment(self):
        fixture = "name: fixture\non: push\njobs:\n  check:\n    runs-on: macos-26\n    env:\n      OUTPUT: ${{ runner.temp }}/output\n    steps:\n      - run: echo fixture\n"
        with tempfile.NamedTemporaryFile(mode="w", suffix=".yml") as stream:
            stream.write(fixture); stream.flush()
            result = subprocess.run([os.environ.get("ACTIONLINT", "actionlint"), "-shellcheck=", "-pyflakes=", stream.name], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('context "runner" is not allowed here', result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
