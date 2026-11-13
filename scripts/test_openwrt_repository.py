from email.message import Message
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import urllib.error


SPEC = importlib.util.spec_from_file_location(
    "openwrt_repository", Path(__file__).with_name("openwrt-repository.py")
)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("Cannot load repository script")
repository = importlib.util.module_from_spec(SPEC)
with patch.dict(os.environ, {
    "PAGE_URL": "https://downloads.garinasset.com/updns",
    "OPENWRT_RELEASE": "25.12.6",
}):
    SPEC.loader.exec_module(repository)


class RepositoryTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.keys = tempfile.TemporaryDirectory()
        cls.key = Path(cls.keys.name) / "signing-key.pem"
        subprocess.run(
            ["openssl", "ecparam", "-name", "prime256v1", "-genkey",
             "-noout", "-out", str(cls.key)],
            check=True,
        )

    @classmethod
    def tearDownClass(cls):
        cls.keys.cleanup()

    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.inputs = self.root / "inputs"
        self.current = {"updns": "1.0.0-r61", "luci": "1.0.41-r1"}
        self.architectures = ["x86_64", "aarch64_cortex-a53"]
        self.web = {}
        self.generation = 0
        self.real_check_output = subprocess.check_output
        self.real_run = subprocess.run
        patches = [
            patch.object(repository, "versions", side_effect=lambda: dict(self.current)),
            patch.object(repository, "fetch", side_effect=self.fetch),
            patch.object(repository.subprocess, "check_output", side_effect=self.check_output),
            patch.object(repository.subprocess, "run", side_effect=self.run_command),
            patch.dict(os.environ, {
                "APK_TOOL": "fake-apk",
                "APK_KEY": str(self.key),
                "INPUT_DIR": str(self.inputs),
                "ARCHITECTURES": json.dumps(self.architectures),
                "GITHUB_OUTPUT": str(self.root / "github-output"),
            }),
        ]
        for item in patches:
            item.start()
            self.addCleanup(item.stop)
        self.build_inputs()

    def fetch(self, path, allow_missing=False):
        if path in self.web:
            return self.web[path]
        if allow_missing:
            return None
        error = urllib.error.HTTPError(path, 404, "Not found", Message(), None)
        self.addCleanup(error.close)
        raise error

    def check_output(self, args, **kwargs):
        if args[0] != "fake-apk":
            return self.real_check_output(args, **kwargs)
        if args[1] == "adbdump":
            return Path(args[-1]).read_bytes()
        if args[1] == "version":
            old, new = args[-2:]
            return "=" if old == new else "<"
        raise AssertionError(f"Unexpected APK command: {args}")

    def run_command(self, args, **kwargs):
        if args[0] != "fake-apk":
            kwargs["stderr"] = subprocess.PIPE
            return self.real_run(args, **kwargs)
        self.assertEqual(args[1], "mkndx")
        directory = Path(kwargs["cwd"])
        packages = args[args.index("--output") + 2:]
        self.assertEqual(len(packages), 3)
        for filename in packages:
            self.assertTrue((directory / filename).is_file())
        (directory / "packages.adb").write_bytes(json.dumps(packages).encode())
        return subprocess.CompletedProcess(args, 0)

    def build_inputs(self, sdk="25.12.6"):
        for arch in self.architectures:
            directory = self.inputs / arch
            directory.mkdir(parents=True, exist_ok=True)
            for name, group in repository.PACKAGES.items():
                if group == "luci" and arch != "x86_64":
                    continue
                version = self.current[group]
                (directory / f"{name}-{version}.apk").write_text(json.dumps({
                    "info": {
                        "name": name,
                        "version": version,
                        "arch": arch if group == "updns" else "noarch",
                    },
                    "sdk": sdk,
                }))

    def publish(self):
        site = self.root / f"site-{self.generation}"
        self.generation += 1
        with patch.dict(os.environ, {"OUTPUT_DIR": str(site)}):
            repository.publish()
        return site

    def seed_site(self, site):
        self.web.update({
            path.relative_to(site / "openwrt").as_posix(): path.read_bytes()
            for path in (site / "openwrt").rglob("*") if path.is_file()
        })

    def test_custom_domain_and_series(self):
        self.assertEqual(repository.BASE_URL, "https://downloads.garinasset.com/updns/openwrt")
        self.assertEqual(repository.RELEASE, "25.12")
        site = self.publish()
        self.assertTrue((site / "openwrt/signing.pem").is_file())
        self.assertFalse((site / "openwrt/25.12.6").exists())
        manifest = json.loads((site / "openwrt/25.12/manifest.json").read_bytes())
        self.assertEqual(manifest["sdk_release"], "25.12.6")
        for arch in self.architectures:
            directory = site / "openwrt/25.12" / arch
            self.assertTrue((directory / "packages.adb").is_file())
            for name, group in repository.PACKAGES.items():
                filename = f"{name}-{self.current[group]}.apk"
                package = directory / filename
                self.assertEqual(
                    manifest["packages"][arch][name]["sha256"],
                    hashlib.sha256(package.read_bytes()).hexdigest(),
                )
                if group == "luci":
                    self.assertEqual(
                        package.read_bytes(),
                        (self.inputs / "x86_64" / filename).read_bytes(),
                    )

    def test_same_series_replaces_superseded_packages(self):
        first = self.publish()
        self.seed_site(first)
        self.current["updns"] = "1.0.0-r62"
        self.build_inputs()
        rebuilt_luci = self.inputs / "x86_64/luci-app-updns-1.0.41-r1.apk"
        data = json.loads(rebuilt_luci.read_bytes())
        data["build_id"] = "unpublished-rebuild"
        rebuilt_luci.write_text(json.dumps(data))
        site = self.publish()
        for arch in self.architectures:
            directory = site / "openwrt/25.12" / arch
            self.assertFalse((directory / "updns-1.0.0-r61.apk").exists())
            self.assertTrue((directory / "updns-1.0.0-r62.apk").exists())
            filename = "luci-app-updns-1.0.41-r1.apk"
            self.assertEqual(
                (directory / filename).read_bytes(),
                (first / "openwrt/25.12" / arch / filename).read_bytes(),
            )

    def test_new_series_preserves_old_packages_and_indexes(self):
        first = self.publish()
        self.seed_site(first)
        self.build_inputs("26.12.1")
        with patch.object(repository, "RELEASE", "26.12"), \
                patch.object(repository, "SDK_RELEASE", "26.12.1"):
            site = self.publish()
        for original in (first / "openwrt/25.12").rglob("*"):
            if original.is_file() and original.name != "index.html":
                self.assertEqual(
                    original.read_bytes(),
                    (site / original.relative_to(first)).read_bytes(),
                )
        self.assertTrue((site / "openwrt/26.12/x86_64/packages.adb").is_file())
        registry = json.loads((site / "openwrt/releases.json").read_bytes())
        self.assertEqual(registry["releases"], ["25.12", "26.12"])

    def test_sdk_change_rebuilds_packages_at_same_versions(self):
        first = self.publish()
        self.seed_site(first)
        self.build_inputs("25.12.7")
        with patch.object(repository, "SDK_RELEASE", "25.12.7"):
            site = self.publish()
        for arch in self.architectures:
            for name, group in repository.PACKAGES.items():
                package = site / "openwrt/25.12" / arch / f"{name}-{self.current[group]}.apk"
                self.assertEqual(json.loads(package.read_bytes())["sdk"], "25.12.7")

    def test_prepare_detects_sdk_change_without_package_version_change(self):
        old = {"versions": dict(self.current), "sdk_release": "25.12.5"}
        with patch.object(repository, "previous", return_value=old):
            repository.prepare()
        self.assertEqual((self.root / "github-output").read_text(), "publish=true\n")

    def test_prepare_skips_unchanged_publication(self):
        old = {"versions": dict(self.current), "sdk_release": "25.12.6"}
        with patch.object(repository, "previous", return_value=old):
            repository.prepare()
        self.assertEqual((self.root / "github-output").read_text(), "publish=false\n")

    def test_prepare_publishes_new_series(self):
        repository.prepare()
        self.assertEqual((self.root / "github-output").read_text(), "publish=true\n")

    def test_indexes_show_file_hashes_without_checksum_manifests(self):
        site = self.publish()
        for directory in [site, *(path for path in site.rglob("*") if path.is_dir())]:
            listing = (directory / "index.html").read_text()
            self.assertIn("SHA-256 values are shown", listing)
            self.assertFalse((directory / "SHA256SUMS").exists())
            for path in directory.iterdir():
                if path.is_file() and path.name != "index.html":
                    digest = hashlib.sha256(path.read_bytes()).hexdigest()
                    self.assertIn(digest, listing)
            if directory != site:
                self.assertIn('href="../"', listing)

    def test_index_escapes_names_and_links(self):
        site = self.root / "escaped"
        site.mkdir()
        (site / 'test <&".apk').write_bytes(b"package")
        repository.generate_indexes(site)
        listing = (site / "index.html").read_text()
        self.assertIn("test &lt;&amp;&quot;.apk", listing)
        self.assertIn('href="test%20%3C%26%22.apk"', listing)

    def test_tampered_old_package_is_rejected(self):
        self.seed_site(self.publish())
        self.web["25.12/x86_64/updns-1.0.0-r61.apk"] = b"tampered"
        with self.assertRaisesRegex(ValueError, "checksum mismatch"):
            self.publish()

    def test_wrong_noarch_metadata_is_rejected(self):
        package = self.inputs / "x86_64/luci-app-updns-1.0.41-r1.apk"
        data = json.loads(package.read_bytes())
        data["info"]["arch"] = "x86_64"
        package.write_text(json.dumps(data))
        with self.assertRaisesRegex(ValueError, "Incorrect package metadata"):
            self.publish()

    def test_missing_shared_package_is_rejected(self):
        (self.inputs / "x86_64/luci-i18n-updns-zh-cn-1.0.41-r1.apk").unlink()
        with self.assertRaisesRegex(ValueError, "Expected one luci-i18n"):
            self.publish()

    def test_invalid_signature_is_rejected(self):
        self.seed_site(self.publish())
        self.web["releases.json.sig"] = b"invalid"
        with self.assertRaises(subprocess.CalledProcessError):
            self.publish()


class FetchTests(unittest.TestCase):
    def test_download_uses_custom_domain_repository_path(self):
        with patch.object(repository.urllib.request, "urlopen") as open_url:
            open_url.return_value.__enter__.return_value.read.return_value = b"public-key"
            self.assertEqual(repository.fetch("signing.pem"), b"public-key")
            open_url.assert_called_once_with(
                "https://downloads.garinasset.com/updns/openwrt/signing.pem", timeout=60
            )

    def test_required_download_does_not_ignore_404(self):
        error = urllib.error.HTTPError("https://example.test", 404, "Not found", Message(), None)
        self.addCleanup(error.close)
        with patch.object(repository.urllib.request, "urlopen", side_effect=error):
            with self.assertRaises(urllib.error.HTTPError):
                repository.fetch("missing")

    def test_optional_download_accepts_only_404(self):
        for status in (404, 403, 500):
            error = urllib.error.HTTPError("https://example.test", status, "Error", Message(), None)
            self.addCleanup(error.close)
            with patch.object(repository.urllib.request, "urlopen", side_effect=error):
                if status == 404:
                    self.assertIsNone(repository.fetch("missing", allow_missing=True))
                else:
                    with self.assertRaises(urllib.error.HTTPError):
                        repository.fetch("missing", allow_missing=True)


if __name__ == "__main__":
    unittest.main()
