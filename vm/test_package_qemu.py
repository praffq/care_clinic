import hashlib
import json
import os
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest import mock

from package_qemu import bundle_files, main, package, release_pin


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / "input"
        self.root.mkdir()
        for name in (
            "VERSION", "qemu-system-x86_64.exe", "libslirp-0.dll", "dependency.dll",
            "COPYING", "COPYING.LIB", "README.rst", "share/bios-256k.bin",
            "share/firmware.bin", "lib/module.dll", "$PLUGINSDIR/installer.dll",
            "qemu-system-aarch64.exe",
        ):
            p = self.root / name
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text("10.1.0" if name == "VERSION" else name)
        self.pin = {"version": "10.1.0", "url": "https://example.test/installer.exe", "sha256": "a" * 64}

    def test_layout_dependencies_and_provenance(self):
        output = Path(self.temp.name) / "bundle.zip"
        checksum = package(self.root, output, self.pin)
        self.assertEqual(checksum, hashlib.sha256(output.read_bytes()).hexdigest())
        with zipfile.ZipFile(output) as archive:
            names = archive.namelist()
            self.assertIn("qemu-system-x86_64.exe", names)
            self.assertIn("lib/module.dll", names)
            self.assertIn("dependency.dll", names)
            self.assertIn("share/firmware.bin", names)
            self.assertIn("COPYING", names)
            self.assertNotIn("$PLUGINSDIR/installer.dll", names)
            self.assertNotIn("qemu-system-aarch64.exe", names)
            provenance = json.loads(archive.read("care-qemu-provenance.json"))
            self.assertEqual(provenance["installer_sha256"], self.pin["sha256"])
            for name, expected in provenance["files_sha256"].items():
                self.assertEqual(hashlib.sha256(archive.read(name)).hexdigest(), expected)

    def test_reproducible_despite_source_timestamps(self):
        one = Path(self.temp.name) / "one.zip"
        two = Path(self.temp.name) / "two.zip"
        first = package(self.root, one, self.pin)
        for p in bundle_files(self.root, "10.1.0"):
            os.utime(p, (1000000000, 1000000000))
        self.assertEqual(first, package(self.root, two, self.pin))
        self.assertEqual(one.read_bytes(), two.read_bytes())

    def test_missing_runtime_or_license_fails(self):
        for name in ("libslirp-0.dll", "share/bios-256k.bin", "COPYING"):
            with self.subTest(name=name):
                p = self.root / name
                data = p.read_bytes()
                p.unlink()
                with self.assertRaisesRegex(ValueError, "missing"):
                    bundle_files(self.root, "10.1.0")
                p.write_bytes(data)

    def test_wrong_version_fails(self):
        with self.assertRaisesRegex(ValueError, "VERSION"):
            bundle_files(self.root, "11.0.0")

    def test_download_pin_matches_application_version(self):
        pin = release_pin()
        self.assertEqual(pin["version"], "10.1.0")
        self.assertEqual(len(pin["sha256"]), 64)
        self.assertNotEqual(pin["sha256"], "0" * 64)

    def test_corrupt_installer_is_rejected_before_extraction(self):
        installer = Path(self.temp.name) / "bad.exe"
        installer.write_bytes(b"not the pinned installer")
        output = Path(self.temp.name) / "out"
        with mock.patch("sys.argv", ["package_qemu.py", "--installer", str(installer), "--output", str(output)]), \
                mock.patch("package_qemu.shutil.which", return_value="7z"), \
                mock.patch("package_qemu.subprocess.run") as extract:
            with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
                main()
        extract.assert_not_called()
        self.assertEqual(list(output.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
