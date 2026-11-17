"""Publication tests run against a release store, never against GitHub."""
import hashlib
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location("berth_publish", Path(__file__).with_name("publish_release.py"))
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class ReleaseStore:
    """A release store standing in for `gh release`, without network access.

    It holds what the remote side holds — the draft flag and a name-to-bytes map
    — and refuses the writes a real release refuses: a second release for the
    same tag, an upload over an existing asset name, the promotion of anything
    that is not a draft. A test can therefore tell "uploaded once" from
    "uploaded twice", and prove that no asset was overwritten or deleted.
    """

    def __init__(self, tag: str) -> None:
        self.tag = tag
        self.is_draft: bool | None = None
        self.assets: dict[str, bytes] = {}
        self.uploads: list[list[str]] = []
        self.promotions = 0
        # A racing publisher: (is_draft, assets) that appears between the read
        # and the create, so the create itself fails.
        self.race: tuple[bool, dict[str, bytes]] | None = None
        # Rewrites the downloaded copy, standing in for a remote set that is
        # internally consistent yet is not this build.
        self.corrupt_download = None

    @property
    def exists(self) -> bool:
        return self.is_draft is not None

    def seed(self, assets: dict[str, bytes], is_draft: bool = True) -> "ReleaseStore":
        self.is_draft = is_draft
        self.assets = dict(assets)
        return self

    def __call__(self, *args: str) -> subprocess.CompletedProcess:
        if len(args) < 3 or args[0] != "release" or args[2] != self.tag:
            raise AssertionError("unexpected gh invocation: " + " ".join(args))
        handler = {"view": self.view, "create": self.create, "upload": self.upload,
                   "download": self.download, "edit": self.edit}.get(args[1])
        if handler is None:
            raise AssertionError("unexpected gh release subcommand: " + args[1])
        return handler(list(args[3:]))

    def view(self, rest: list[str]) -> subprocess.CompletedProcess:
        if not self.exists:
            raise RuntimeError("release not found")
        payload = {"isDraft": self.is_draft,
                   "assets": [{"name": name} for name in self.assets]}
        return subprocess.CompletedProcess(["gh"], 0, stdout=json.dumps(payload), stderr="")

    def create(self, rest: list[str]) -> None:
        if self.race is not None:
            self.seed(self.race[1], self.race[0])
            self.race = None
            raise RuntimeError("release already exists: " + self.tag)
        if self.exists:
            raise RuntimeError("release already exists: " + self.tag)
        self.seed({})

    def upload(self, rest: list[str]) -> None:
        if not self.is_draft:
            raise AssertionError("upload needs a draft release")
        names = []
        for path in map(Path, rest):
            if path.name in self.assets:
                raise AssertionError("refusing to overwrite " + path.name)
            self.assets[path.name] = path.read_bytes()
            names.append(path.name)
        self.uploads.append(names)

    def download(self, rest: list[str]) -> None:
        if not self.exists:
            raise AssertionError("download needs an existing release")
        target = Path(rest[rest.index("--dir") + 1])
        for name, data in self.assets.items():
            (target / name).write_bytes(data)
        if self.corrupt_download is not None:
            self.corrupt_download(target)

    def edit(self, rest: list[str]) -> None:
        if rest != ["--draft=false"] or not self.is_draft:
            raise AssertionError("only a draft release is promoted")
        self.is_draft = False
        self.promotions += 1


class AssetVerificationTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.tag = "v1.2.3"
        for name in release.expected_assets(self.tag):
            if not name.endswith(".sha256"):
                (self.root / name).write_bytes(b"synthetic archive fixture")
                digest = hashlib.sha256(b"synthetic archive fixture").hexdigest()
                (self.root / (name + ".sha256")).write_text(digest + "  " + name + "\n")

    def test_complete_assets_verify_without_network(self):
        self.assertEqual(len(release.verify_assets(self.root, self.tag)), 20)
        text = "".join(path.read_text() for path in sorted(self.root.glob("*.sha256")))
        (self.root / "SHA256SUMS").write_text(text)
        self.assertEqual(len(release.verify_assets(self.root, self.tag, True)), 21)

    def test_assets_are_the_cli_archives_and_linux_packages(self):
        expected = release.expected_assets(self.tag)
        self.assertIn("option-berth-v1.2.3-windows-amd64.zip", expected)
        self.assertIn("option-berth-v1.2.3-darwin-arm64.tar.gz", expected)
        self.assertIn("option-berth_1.2.3_amd64.deb", expected)
        self.assertIn("option-berth_1.2.3_arm64.deb", expected)
        self.assertIn("option-berth-1.2.3-1.x86_64.rpm", expected)
        self.assertIn("option-berth-1.2.3-1.aarch64.rpm", expected)
        self.assertFalse(any(name.startswith("OptionBerth-") for name in expected),
                         "a desktop application is a separate product and must not ride in this release")
        extra = self.root / "OptionBerth-v1.2.3-macos-arm64-adhoc.dmg"
        extra.write_bytes(b"not part of this release")
        store = ReleaseStore(self.tag)
        with patch.object(release, "gh", store):
            with self.assertRaisesRegex(ValueError, "incomplete or unexpected"):
                release.publish(self.root, self.tag)
            self.assertEqual(store.uploads, [])

    def test_unexpected_asset_fails_before_network_or_release_writes(self):
        # A matching checksum does not make an unrelated file part of the
        # selected release contract. Never upload an ambiguous set.
        name = "OptionBerth-v1.2.3-macos-arm64-adhoc.dmg"
        data = b"synthetic extra asset"
        (self.root / name).write_bytes(data)
        (self.root / (name + ".sha256")).write_text(hashlib.sha256(data).hexdigest() + "  " + name + "\n")
        store = ReleaseStore(self.tag)
        with patch.object(release, "gh", store):
            with self.assertRaisesRegex(ValueError, "incomplete or unexpected"):
                release.publish(self.root, self.tag)
            self.assertEqual(store.uploads, [])
            self.assertFalse(store.exists)
            self.assertFalse((self.root / "SHA256SUMS").exists())

    def test_missing_extra_corrupt_and_path_injection_are_rejected(self):
        archive = next(path for path in self.root.iterdir() if path.name.endswith(".zip"))
        data = archive.read_bytes()
        archive.write_bytes(b"corrupted")
        with self.assertRaisesRegex(ValueError, "checksum"):
            release.verify_assets(self.root, self.tag)
        archive.write_bytes(data)
        extra = self.root / "private-note"
        extra.write_text("must not upload")
        with self.assertRaisesRegex(ValueError, "unexpected"):
            release.verify_assets(self.root, self.tag)
        extra.unlink()
        archive.unlink()
        with self.assertRaisesRegex(ValueError, "incomplete"):
            release.verify_assets(self.root, self.tag)
        for tag in ("../main", "v1.2", "v01.2.3", "v1.2.3;echo"):
            with self.assertRaises(ValueError):
                release.expected_assets(tag)


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.tag = "v1.2.3"
        for name in release.expected_assets(self.tag):
            if not name.endswith(".sha256"):
                (self.root / name).write_bytes(b"synthetic archive fixture")
                digest = hashlib.sha256(b"synthetic archive fixture").hexdigest()
                (self.root / (name + ".sha256")).write_text(digest + "  " + name + "\n")
        self.store = ReleaseStore(self.tag)
        gh = patch.object(release, "gh", self.store)
        gh.start()
        self.addCleanup(gh.stop)

    def expected_upload(self) -> dict[str, bytes]:
        """The exact bytes this invocation is expected to publish."""
        assets = {path.name: path.read_bytes()
                  for path in self.root.iterdir() if path.name != "SHA256SUMS"}
        aggregate = "".join(path.read_text() for path in sorted(self.root.glob("*.sha256")))
        return assets | {"SHA256SUMS": aggregate.encode()}

    def test_first_publication_uploads_every_asset_once_and_promotes(self):
        expected = self.expected_upload()
        release.publish(self.root, self.tag)
        self.assertEqual(self.store.uploads, [sorted(expected)])
        self.assertEqual(self.store.assets, expected)
        self.assertFalse(self.store.is_draft)
        self.assertEqual(self.store.promotions, 1)

    def test_publish_occurs_only_after_matching_roundtrip(self):
        release.publish(self.root, self.tag)
        self.assertEqual(self.store.promotions, 1)
        self.assertFalse(self.store.is_draft)

    def test_roundtrip_must_match_local_assets_not_just_remote_checksums(self):
        def different_build(target):
            archive = next(target.glob("*.zip"))
            data = b"different but internally consistent remote build"
            archive.write_bytes(data)
            (target / (archive.name + ".sha256")).write_text(
                hashlib.sha256(data).hexdigest() + "  " + archive.name + "\n")
            (target / "SHA256SUMS").write_text("".join(path.read_text() for path in sorted(target.glob("*.sha256"))))
        self.store.corrupt_download = different_build
        with self.assertRaisesRegex(ValueError, "local"):
            release.publish(self.root, self.tag)
        self.assertEqual(self.store.promotions, 0)
        self.assertTrue(self.store.is_draft, "a failed publication must leave the draft in place")
