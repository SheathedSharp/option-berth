"""Verify command release gates without running tools or publishing."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("command_release", Path(__file__).with_name("release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)

class CommandReleaseTests(unittest.TestCase):
    def test_command_policy_and_native_menu_are_required_and_isolated(self):
        calls = []
        def record(root, *args, **kwargs):
            calls.append((args, kwargs))
            return "/fixture/build" if "--show-bin-path" in args else ""
        with patch.object(release, "run", record), patch.object(release, "check_versions"), patch.object(release.sys, "platform", "darwin"):
            release.verify(Path(__file__).resolve().parent.parent)
        compile_command = next(args for args, _ in calls if "client/macos/Tests/WorkspaceCommandTests.swift" in args)
        self.assertIn("client/macos/Sources/WorkspaceCommands.swift", compile_command)
        binary = compile_command[compile_command.index("-o") + 1]
        self.assertTrue(any(args == (binary,) for args, _ in calls))
        self.assertTrue(any("--product" in args and "CommandChecks" in args for args, _ in calls))
        native = [(args, options) for args, options in calls if args and args[0].endswith("/CommandChecks")]
        self.assertEqual(len(native), 1)
        env = native[0][1]["environment"]
        self.assertEqual(env["HOME"], env["CFFIXED_USER_HOME"])
        self.assertTrue(Path(env["BERTH_HOME"]).is_relative_to(env["HOME"]))
        self.assertEqual(env["BERTH_COMMAND_TEST"], "1")

if __name__ == "__main__":
    unittest.main()
