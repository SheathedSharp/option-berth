import importlib.util
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("macos_release_signing", Path(__file__).with_name("macos_release_signing.py"))
signing = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = signing
SPEC.loader.exec_module(signing)


class MacSigningTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.app = Path(self.temporary.name) / "Fixture.app"
        (self.app / "Contents/MacOS").mkdir(parents=True)
        (self.app / "Contents/Info.plist").write_text("fixture")
        (self.app / "Contents/MacOS/Fixture").write_bytes(b"\xcf\xfa\xed\xfe" + b"synthetic code")
        self.configuration = signing.SigningConfiguration("developer-id", "A" * 40, "ABC1234567", "fixture-profile")
        self.calls = []

    def runner(self, *args, **kwargs):
        self.calls.append(args)
        if args[:2] == ("codesign", "--display"):
            return "Authority=Developer ID Application: Fixture\nTeamIdentifier=ABC1234567\n"
        if args[:3] == ("xcrun", "notarytool", "submit"):
            return json.dumps({"status": "Accepted"})
        return ""

    def test_adhoc_never_touches_notary_or_keychain(self):
        with patch.object(signing.sys, "platform", "darwin"):
            self.assertEqual(signing.sign_application(self.app, signing.SigningConfiguration(), self.runner), "adhoc")
        self.assertFalse(any(args[0] == "xcrun" for args in self.calls))
        self.assertTrue(all("--deep" not in args for args in self.calls if "--sign" in args))

    def test_developer_id_gates_finish_before_trusted_name(self):
        with patch.object(signing.sys, "platform", "darwin"):
            self.assertEqual(signing.sign_application(self.app, self.configuration, self.runner), "notarized")
        self.assertEqual(self.calls[-1][:2], ("spctl", "--assess"))
        self.assertLess(next(i for i,a in enumerate(self.calls) if a[:3] == ("xcrun","notarytool","submit")),
                        next(i for i,a in enumerate(self.calls) if a[:3] == ("xcrun","stapler","staple")))
        for args in self.calls:
            if "--sign" in args:
                self.assertIn("--timestamp", args)
                self.assertIn("runtime", args)

    def test_missing_configuration_is_rejected_before_commands(self):
        for config in [signing.SigningConfiguration("developer-id"),
                       signing.SigningConfiguration("adhoc", identity="unexpected"),
                       signing.SigningConfiguration("developer-id", "A"*40, "wrong", "profile"),
                       signing.SigningConfiguration("developer-id", "A"*40, "ABC1234567", "../profile")]:
            with self.assertRaises(ValueError):
                signing.sign_application(self.app, config, self.runner)
        self.assertEqual(self.calls, [])

    def test_wrong_team_never_uploads(self):
        def wrong(*args, **kwargs):
            if args[:2] == ("codesign", "--display"):
                return "Authority=Developer ID Application: Other\nTeamIdentifier=WRONG00000"
            return self.runner(*args, **kwargs)
        with patch.object(signing.sys, "platform", "darwin"), self.assertRaisesRegex(RuntimeError, "team"):
            signing.sign_application(self.app, self.configuration, wrong)
        self.assertFalse(any(args[:2] == ("xcrun", "notarytool") for args in self.calls))

    def test_rejected_or_malformed_notary_result_never_staples(self):
        for output in ['{"status":"Invalid"}', 'not-json', '[]']:
            self.calls = []
            def rejected(*args, **kwargs):
                if args[:3] == ("xcrun", "notarytool", "submit"):
                    self.calls.append(args)
                    return output
                return self.runner(*args, **kwargs)
            with patch.object(signing.sys, "platform", "darwin"), self.assertRaises(RuntimeError):
                signing.sign_application(self.app, self.configuration, rejected)
            self.assertFalse(any(args[:2] == ("xcrun", "stapler") for args in self.calls))

    def test_gate_failure_never_returns_trusted_state(self):
        for gate in [("xcrun", "stapler", "staple"), ("xcrun", "stapler", "validate"), ("spctl", "--assess")]:
            def failed(*args, **kwargs):
                if args[:len(gate)] == gate: raise RuntimeError("fixture gate failure")
                return self.runner(*args, **kwargs)
            with patch.object(signing.sys, "platform", "darwin"), self.assertRaises(RuntimeError):
                signing.sign_application(self.app, self.configuration, failed)

    def test_external_symlink_rejected(self):
        (self.app / "Contents/external").symlink_to(Path(self.temporary.name) / "outside")
        with patch.object(signing.sys, "platform", "darwin"), self.assertRaisesRegex(ValueError, "symlink"):
            signing.sign_application(self.app, signing.SigningConfiguration(), self.runner)
        self.assertEqual(self.calls, [])

    def test_non_macos_refused(self):
        with patch.object(signing.sys, "platform", "linux"), self.assertRaises(RuntimeError):
            signing.sign_application(self.app, signing.SigningConfiguration(), self.runner)
