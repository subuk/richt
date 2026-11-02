import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

import package
import smoke


class SmokeTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="richt-native-test-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.work = Path(cls.temporary.name)
        cls.binary = cls.work / "richt"
        environment = dict(os.environ, CGO_ENABLED="0")
        environment.pop("GOOS", None)
        environment.pop("GOARCH", None)
        subprocess.run([os.environ.get("GO", "go"), "build", "-trimpath", "-ldflags",
                        "-X github.com/subuk/richt/cli.Version=1.2.3", "-o", str(cls.binary), "."],
                       cwd=package.ROOT, env=environment, check=True)

    def setUp(self):
        self.case = tempfile.TemporaryDirectory(prefix="richt-smoke-test-")
        self.addCleanup(self.case.cleanup)
        self.directory = Path(self.case.name)
        self.stem = f"richt-1.2.3-{smoke.native_target()}"
        self.archive = self.directory / f"{self.stem}.tar.gz"

    def invoke(self, version="1.2.3"):
        return subprocess.run([sys.executable, str(package.ROOT / "scripts/release/smoke.py"),
                               "--archive", str(self.archive), "--version", version],
                              cwd=self.directory, capture_output=True, text=True)

    def test_real_native_archive_preparation_apply_and_continuation(self):
        package.write_archive(self.archive, self.stem, self.binary)
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Smoke passed:", result.stdout)
        self.assertEqual(list(self.directory.iterdir()), [self.archive])

    def test_wrong_embedded_version_fails(self):
        self.stem = f"richt-1.2.4-{smoke.native_target()}"
        self.archive = self.directory / f"{self.stem}.tar.gz"
        package.write_archive(self.archive, self.stem, self.binary)
        result = self.invoke("1.2.4")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("wrong version", result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def malformed_archive(self, special):
        with tarfile.open(self.archive, "w:gz") as archive:
            for name in ("richt", "README.md"):
                member = tarfile.TarInfo(f"{self.stem}/{name}")
                member.mode, member.size = 0o755, 1
                archive.addfile(member, io.BytesIO(b"x"))
            archive.addfile(special)

    def test_path_traversal_is_rejected_before_extraction(self):
        member = tarfile.TarInfo("../escaped")
        self.malformed_archive(member)
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("unexpected paths", result.stderr)
        self.assertFalse((self.directory.parent / "escaped").exists())

    def test_links_and_duplicate_entries_are_rejected(self):
        member = tarfile.TarInfo(f"{self.stem}/LICENSE")
        member.type, member.linkname = tarfile.SYMTYPE, "/etc/passwd"
        self.malformed_archive(member)
        self.assertIn("link or unsupported entry", self.invoke().stderr)
        self.malformed_archive(tarfile.TarInfo(f"{self.stem}/README.md"))
        self.assertIn("duplicate entries", self.invoke().stderr)

    def test_missing_binary_and_corrupt_archives_fail_cleanly(self):
        with tarfile.open(self.archive, "w:gz"):
            pass
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing richt", result.stderr)
        self.archive.write_bytes(b"not an archive")
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
