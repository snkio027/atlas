import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("prepare_ci", Path(__file__).with_name("prepare-ci.py"))
ci = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ci)


class PreparationContract(unittest.TestCase):
    def test_fast_prepares_task_only(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(ci.sys, "argv", ["prepare-ci.py", "--fast", directory]), patch.object(ci, "prepare_extra_tool") as prepare, patch.object(ci.subprocess, "check_output") as external:
            ci.main()
            self.assertEqual([x.args[0]["name"] for x in prepare.call_args_list], ["task"])
            external.assert_not_called()

    def test_full_preserves_runtime_and_historical_tool_preparation(self):
        frozen = json.dumps([{"name": name} for name in ["helm", "kubectl", "yq"]]).encode()
        def result(command, **kwargs):
            if command[0] == "go":
                return "/fixture/prepared"
            self.assertEqual(command[:3], ["git", "show", command[2]])
            self.assertTrue(command[2].startswith("697ebf04e9362abcce9a4b8db005ee747493417d:"))
            return frozen
        with tempfile.TemporaryDirectory() as directory, patch.object(ci.sys, "argv", ["prepare-ci.py", directory]), patch.object(ci, "prepare_extra_tool") as prepare, patch.object(ci.subprocess, "check_output", side_effect=result) as external, patch.object(ci.shutil, "copyfile") as copy, patch.object(ci.os, "chmod"):
            ci.main()
            commands = [x.args[0] for x in external.call_args_list]
            self.assertEqual(commands[0][:4], ["go", "run", "./cmd/atlas-release", "--prepare-tools"])
            self.assertGreater(copy.call_count, 0)
            self.assertEqual([x.args[0]["name"] for x in prepare.call_args_list], ["task", "yq", "lua", "helm", "kubectl", "yq"])

    def test_unknown_mode_refuses_before_download(self):
        with patch.object(ci.sys, "argv", ["prepare-ci.py", "--unknown", "/tmp/unused"]), patch.object(ci, "prepare_extra_tool") as prepare:
            with self.assertRaises(SystemExit):
                ci.main()
            prepare.assert_not_called()


if __name__ == "__main__":
    unittest.main()
