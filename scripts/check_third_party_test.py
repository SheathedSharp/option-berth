"""Offline source-integrity regression tests: never execute archived code."""
import json
import io
import tarfile
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import collect_third_party as collector
import zipfile

from check_third_party import audit_archive, verify_manifest, verify_source_bindings
from collect_third_party import ROOT
from collect_third_party import module_h1, sha


class SourceIntegrityTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "engine").mkdir()
        (self.root / "third_party/sources/modules").mkdir(parents=True)
        self.archive = self.root / "third_party/sources/modules/example.zip"
        with zipfile.ZipFile(self.archive, "w") as archive:
            archive.writestr("example.org/lib@v1.0.0/go.mod", "module example.org/lib\n")
            archive.writestr("example.org/lib@v1.0.0/LICENSE", "Original license\n")
            archive.writestr("example.org/lib@v1.0.0/lib.go", "package lib\n")
        self.h1 = module_h1(self.archive)
        (self.root / "engine/go.sum").write_text("example.org/lib v1.0.0 " + self.h1 + "\n")
        (self.root / "engine/go.mod").write_text("module example.org/product\n")
        info = audit_archive(self.archive)
        info.update(path=self.archive.relative_to(self.root).as_posix(), kind="complete-go-module-snapshot-not-whole-upstream-repository", go_sum_h1=self.h1)
        self.upstream = self.archive.with_suffix(".tar.gz")
        with tarfile.open(self.upstream, "w:gz") as archive:
            for name,data in [("repo/lib.go",b"package lib\n"),("repo/LICENSE",b"Original license\n")]:
                member=tarfile.TarInfo(name); member.size=len(data); archive.addfile(member,io.BytesIO(data))
        upstream=audit_archive(self.upstream)
        upstream.update(path=self.upstream.relative_to(self.root).as_posix(),kind="full-upstream-source-archive")
        self.manifest = {"inputs": {"engine/go.mod": sha(self.root / "engine/go.mod"), "engine/go.sum": sha(self.root / "engine/go.sum")}, "components": [{"id": "example.org/lib", "version": "v1.0.0", "archives": [info,upstream], "gaps": []}], "license_documents": []}

    def errors(self):
        return verify_manifest(self.root, self.manifest, check_graph=False)

    def test_complete_pinned_source_passes(self):
        self.assertEqual([], self.errors())

    def test_archive_tampering_rejected(self):
        with self.archive.open("ab") as stream:
            stream.write(b"tamper")
        self.assertTrue(any("sha256" in error for error in self.errors()))

    def test_missing_source_rejected(self):
        self.archive.unlink()
        self.assertTrue(any("missing" in error for error in self.errors()))

    def test_go_sum_binding_not_only_manifest_hash(self):
        self.manifest["components"][0]["archives"][0]["go_sum_h1"] = "h1:fake"
        self.assertTrue(any("h1" in error for error in self.errors()))

    def test_changed_dependency_inputs_rejected(self):
        (self.root / "engine/go.mod").write_text("module changed\n")
        self.assertTrue(any("input" in error for error in self.errors()))

    def test_unregistered_archive_rejected(self):
        (self.archive.parent / "extra.zip").write_bytes(self.archive.read_bytes())
        self.assertTrue(any("unregistered" in error for error in self.errors()))

    def test_unsafe_archive_paths_rejected(self):
        for name in ("../escape.go", "/absolute.go", "repo/.git/config", "repo/../../escape.go", "repo\\..\\escape.go"):
            with self.subTest(name=name):
                with zipfile.ZipFile(self.archive, "w") as archive:
                    archive.writestr(name, "not executable")
                with self.assertRaises(ValueError):
                    audit_archive(self.archive)

    def test_lfs_placeholder_is_not_full_source(self):
        with zipfile.ZipFile(self.archive, "w") as archive:
            archive.writestr("repo/source.c", "version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 100\n")
        with self.assertRaisesRegex(ValueError, "LFS"):
            audit_archive(self.archive)

    def test_json_schema_test_data_is_actual_source(self):
        with zipfile.ZipFile(self.archive, "w") as archive:
            archive.writestr("suite/tests/schema.json", '{"type":"integer"}')
        self.assertEqual(1, audit_archive(self.archive)["source_files"])

    def test_source_only_license_is_rejected(self):
        with zipfile.ZipFile(self.archive, "w") as archive:
            archive.writestr("example.org/lib@v1.0.0/LICENSE", "License only, not source")
        self.manifest["components"][0]["archives"][0].update(audit_archive(self.archive))
        self.assertTrue(any("no source files" in error for error in self.errors()))

    def test_submodule_is_not_assumed_in_tarball(self):
        with zipfile.ZipFile(self.archive, "a") as archive:
            archive.writestr("example.org/lib@v1.0.0/.gitmodules", '[submodule "testdata/suite"]\n path = testdata/suite\n')
        self.manifest["components"][0]["archives"][0].update(audit_archive(self.archive))
        self.assertTrue(any("unresolved submodules" in error for error in self.errors()))

    def test_license_document_hash_is_bound(self):
        self.manifest["license_documents"] = [{"path":"missing-license.txt", "sha256":"fake", "bytes":10}]
        self.assertTrue(any("license document" in error for error in self.errors()))

    def test_upstream_unsafe_fixture_link_is_recorded_not_extracted(self):
        import io
        import tarfile
        path = self.archive.with_suffix(".tar.gz")
        with tarfile.open(path, "w:gz") as archive:
            source = tarfile.TarInfo("repo/source.go"); source.size = 10
            archive.addfile(source, io.BytesIO(b"package x\n"))
            link = tarfile.TarInfo("repo/testdata/link"); link.type = tarfile.SYMTYPE; link.linkname = "/etc/foo"
            archive.addfile(link)
        self.assertEqual([["repo/testdata/link", "/etc/foo"]], audit_archive(path)["unsafe_symlinks"])
        self.assertFalse((self.root / "repo").exists())

    def test_real_checked_in_inventory_uses_production_verifier(self):
        manifest = json.loads((ROOT / "third_party/sources.json").read_text())
        self.assertEqual([], verify_manifest(ROOT, manifest, check_graph=False))
        self.assertEqual([], verify_source_bindings(ROOT, manifest))

    def test_module_zip_is_not_silently_called_full_project(self):
        self.manifest["components"][0]["archives"].pop()
        self.upstream.unlink()
        self.assertTrue(any("full upstream" in error for error in self.errors()))

    def offline_fixture(self):
        commit = "a" * 40
        target = self.root / "third_party/sources/upstream"; target.mkdir()
        path = target / ("example--lib--" + commit + ".tar.gz")
        path.write_bytes(self.upstream.read_bytes())
        inventory = {"components":[{"archives":[{"repository":"https://github.com/example/lib","ref":"v1.0.0","commit":commit,"sha256":sha(path)}]}]}
        (self.root / "third_party/sources.json").write_text(json.dumps(inventory))
        return target,path

    def test_offline_collection_reuses_real_pinned_archive_without_network(self):
        target,path = self.offline_fixture()
        with patch.object(collector,"ROOT",self.root), patch.object(collector,"OFFLINE",True), patch.object(collector,"public_gh",side_effect=AssertionError("network forbidden")):
            result = collector.github_source_serial("example/lib","v1.0.0",target)
        self.assertEqual(sha(path),result["sha256"])

    def test_offline_missing_archive_is_not_downloaded(self):
        target,path = self.offline_fixture(); path.unlink()
        with patch.object(collector,"ROOT",self.root), patch.object(collector,"OFFLINE",True), patch.object(collector,"public_gh",side_effect=AssertionError("network forbidden")):
            with self.assertRaisesRegex(ValueError,"missing or changed"):
                collector.github_source_serial("example/lib","v1.0.0",target)
        self.assertFalse(path.exists())

    def test_offline_tamper_is_not_overwritten_or_downloaded(self):
        target,path = self.offline_fixture(); path.write_bytes(b"changed")
        with patch.object(collector,"ROOT",self.root), patch.object(collector,"OFFLINE",True), patch.object(collector,"public_gh",side_effect=AssertionError("network forbidden")):
            with self.assertRaisesRegex(ValueError,"missing or changed"):
                collector.github_source_serial("example/lib","v1.0.0",target)
        self.assertEqual(b"changed",path.read_bytes())


if __name__ == "__main__":
    unittest.main()
