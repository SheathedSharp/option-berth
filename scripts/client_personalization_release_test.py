"""Check release verification wiring without publishing or launching processes."""
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("personalization_release", Path(__file__).with_name("release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class PersonalizationReleaseTests(unittest.TestCase):
    def commands(self):
        calls = []
        def record(root, *args, **kwargs):
            calls.append((args, kwargs))
            return "/fixture/build" if "--show-bin-path" in args else ""
        with patch.object(release, "run", record), patch.object(release, "check_versions"), patch.object(release.sys, "platform", "darwin"):
            release.verify(Path(__file__).resolve().parent.parent)
        return calls

    def test_draft_compile_contains_configuration_dependencies(self):
        calls = self.commands()
        command = next(args for args, _ in calls if "client/macos/Tests/DraftSheetInteractionTests.swift" in args)
        self.assertIn("client/macos/Sources/ClientConfiguration.swift", command)
        self.assertIn("client/macos/Sources/ClientConfigurationMonitor.swift", command)

    def test_configuration_regressions_are_release_gates(self):
        calls = self.commands()
        for name in ("ClientConfigurationTests", "ClientPreferencesTests", "ClientConfigurationWatchTests"):
            with self.subTest(name=name):
                compile_command = next((args for args, _ in calls if f"client/macos/Tests/{name}.swift" in args), None)
                self.assertIsNotNone(compile_command)
                binary = compile_command[compile_command.index("-o") + 1]
                self.assertTrue(any(args == (binary,) for args, _ in calls))

    def test_native_preferences_gate_isolated_and_required(self):
        calls = self.commands()
        self.assertTrue(any("--product" in args and "PersonalizationChecks" in args for args, _ in calls))
        executed = [(args, options) for args, options in calls if args and args[0].endswith("/PersonalizationChecks")]
        self.assertEqual(len(executed), 1)
        environment = executed[0][1]["environment"]
        self.assertEqual(environment["HOME"], environment["CFFIXED_USER_HOME"])
        self.assertTrue(Path(environment["BERTH_HOME"]).is_relative_to(Path(environment["HOME"])))
        self.assertEqual(environment["BERTH_PERSONALIZATION_TEST"], "1")


if __name__ == "__main__":
    unittest.main()
