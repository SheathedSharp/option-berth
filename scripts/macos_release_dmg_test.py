import json
import os
from pathlib import Path
import plistlib
import shutil
import tempfile
import unittest
from unittest.mock import patch

import macos_release_dmg as dmg
from macos_release_signing import SigningConfiguration


class DiskImageTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.app = self.root / "OptionBerth.app"
        (self.app / "Contents/MacOS").mkdir(parents=True)
        with (self.app / "Contents/Info.plist").open("wb") as stream:
            plistlib.dump({"CFBundleShortVersionString": "1.2.3", "BerthBuildCommit": "a" * 40}, stream)
        (self.app / "Contents/MacOS/oberth").write_bytes(b"\xcf\xfa\xed\xfe" + b"synthetic, never executed")
        (self.app / "Contents/MacOS/oberth").chmod(0o755)
        for name in ("Licenses/option-berth-LICENSE.txt", "Licenses/target-runtime-notices.txt", "Fonts/LICENSE-Monaspace.txt"):
            path = self.app / "Contents/Resources" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("synthetic notice")
        self.destination = self.root / "result.dmg"
        self.calls = []
        self.volume = None
        self.developer = SigningConfiguration("developer-id", "A" * 40, "ABC1234567", "fixture-profile")

    def runner(self, *args, **kwargs):
        self.calls.append(args)
        if args[0] == "ditto":
            shutil.copytree(args[1], args[2], symlinks=True)
        elif args[:2] == ("hdiutil", "create"):
            self.volume = Path(args[args.index("-srcfolder") + 1])
            Path(args[-1]).write_bytes(b"synthetic disk image, never mounted")
        elif args[:2] == ("hdiutil", "attach"):
            self.assertIn("-readonly", args)
            self.assertIn("-nobrowse", args)
            mount = Path(args[args.index("-mountpoint") + 1])
            shutil.copytree(self.volume, mount, symlinks=True, dirs_exist_ok=True)
            return plistlib.dumps({"system-entities": [{"mount-point": str(mount)}]}).decode()
        elif args[:2] == ("codesign", "--display"):
            return "Authority=Developer ID Application: Fixture\nTeamIdentifier=ABC1234567\n"
        elif args[:3] == ("xcrun", "notarytool", "submit"):
            return '{"status":"Accepted"}'
        elif args[0] == "/usr/bin/env":
            self.assertIn("BERTH_NO_AUTOSTART=1", args)
            self.assertIn("/copied/OptionBerth.app/", args[-3])
            return json.dumps({"version": "v1.2.3", "commit": "a" * 7, "platform": "darwin/arm64"})
        return ""

    def build(self, runner=None, configuration=None):
        with patch.object(dmg.sys, "platform", "darwin"):
            return dmg.create_disk_image(self.app, self.destination, configuration, runner or self.runner)

    def test_readonly_drop_target_copy_identity_and_cleanup_precede_final_path(self):
        def observed(*args, **kwargs):
            self.assertFalse(self.destination.exists(), "final asset became visible before all gates")
            return self.runner(*args, **kwargs)
        self.assertEqual(self.build(observed), "adhoc")
        self.assertTrue(self.destination.is_file())
        self.assertEqual(self.calls[-1][:2], ("hdiutil", "detach"))
        self.assertFalse(any(call[0] == "xcrun" for call in self.calls))
        self.assertEqual(list(self.root.glob(".oberth-dmg-*")), [])
        self.assertFalse((self.app.parent / "Applications").exists())

    def test_failure_at_every_gate_leaves_no_final_asset(self):
        gates = [("ditto",), ("hdiutil", "create"), ("codesign", "--verify"),
                 ("hdiutil", "verify"), ("hdiutil", "attach"), ("/usr/bin/env",)]
        for gate in gates:
            self.calls = []
            def failure(*args, **kwargs):
                if args[:len(gate)] == gate: raise RuntimeError("fixture failed")
                return self.runner(*args, **kwargs)
            with self.subTest(gate=gate), self.assertRaises(RuntimeError):
                self.build(failure)
            self.assertFalse(self.destination.exists())
            if gate == ("/usr/bin/env",):
                self.assertEqual(self.calls[-1][:2], ("hdiutil", "detach"))

    def test_wrong_copy_identity_and_drop_link_are_rejected(self):
        for defect in ("engine", "link"):
            def broken(*args, **kwargs):
                value = self.runner(*args, **kwargs)
                if defect == "engine" and args[0] == "/usr/bin/env":
                    return '{"version":"v0.0.1","commit":"old","platform":"darwin/arm64"}'
                if defect == "link" and args[:2] == ("hdiutil", "attach"):
                    (Path(args[args.index("-mountpoint") + 1]) / "Applications").unlink()
                return value
            with self.subTest(defect=defect), self.assertRaises(RuntimeError): self.build(broken)
            self.assertFalse(self.destination.exists())

    def test_existing_file_and_concurrent_publication_are_never_overwritten(self):
        self.destination.write_bytes(b"existing")
        with self.assertRaises(ValueError): self.build()
        self.assertEqual(self.calls, [])
        self.destination.unlink()
        def race(*args, **kwargs):
            value = self.runner(*args, **kwargs)
            if args[:2] == ("hdiutil", "detach"): self.destination.write_bytes(b"another publisher")
            return value
        with self.assertRaises(FileExistsError): self.build(race)
        self.assertEqual(self.destination.read_bytes(), b"another publisher")

    def test_cleanup_failure_refuses_final_name(self):
        def busy(*args, **kwargs):
            if args[:2] == ("hdiutil", "detach"):
                self.calls.append(args)
                raise RuntimeError("busy owned mount")
            return self.runner(*args, **kwargs)
        with self.assertRaises(RuntimeError): self.build(busy)
        self.assertFalse(self.destination.exists())
        self.assertEqual(sum(call[:2] == ("hdiutil", "detach") for call in self.calls), 2)

    def test_developer_id_requires_outer_image_acceptance_and_gatekeeper(self):
        self.assertEqual(self.build(configuration=self.developer), "notarized")
        submissions = [a for a in self.calls if a[:3] == ("xcrun", "notarytool", "submit")]
        self.assertEqual(len(submissions), 1)
        self.assertTrue(submissions[0][3].endswith(".dmg"))
        self.assertTrue(any(a[:2] == ("spctl", "--assess") and "open" in a for a in self.calls))
        self.assertFalse(any("--deep" in a for a in self.calls if "--sign" in a))

    def test_rejected_outer_image_never_gets_notarized_filename(self):
        for response in ('{"status":"Invalid"}', '[]', 'not json'):
            self.calls = []
            def reject(*args, **kwargs):
                if args[:3] == ("xcrun", "notarytool", "submit"):
                    self.calls.append(args)
                    return response
                return self.runner(*args, **kwargs)
            with self.subTest(response=response), self.assertRaises(RuntimeError):
                self.build(reject, self.developer)
            self.assertFalse(self.destination.exists())
            self.assertFalse(any(a[:2] == ("xcrun", "stapler") for a in self.calls))

    def test_missing_engine_notice_external_symlink_and_wrong_platform_fail_closed(self):
        engine = self.app / "Contents/MacOS/oberth"
        engine.chmod(0o644)
        with self.assertRaises(ValueError): self.build()
        engine.chmod(0o755)
        (self.app / "external").symlink_to(self.root / "outside")
        with self.assertRaises(ValueError): self.build()
        (self.app / "external").unlink()
        (self.app / "Contents/Resources/Fonts/LICENSE-Monaspace.txt").unlink()
        with self.assertRaises(ValueError): self.build()
        with patch.object(dmg.sys, "platform", "linux"), self.assertRaises(RuntimeError):
            dmg.create_disk_image(self.app, self.destination, runner=self.runner)
        self.assertEqual(self.calls, [])


if __name__ == "__main__":
    unittest.main()
