import json
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

import package


class PackageTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="richt-package-test-")
        self.addCleanup(self.temporary.cleanup)
        self.work = Path(self.temporary.name)
        self.log = self.work / "builds.jsonl"
        self.go = self.work / "fake-go"
        self.go.write_text(f"#!{sys.executable}\n" + r'''
import json
import os
from pathlib import Path
import sys
target = os.environ["GOOS"] + "-" + os.environ["GOARCH"]
with open(os.environ["BUILD_LOG"], "a") as output:
    output.write(json.dumps({"target": target, "cgo": os.environ["CGO_ENABLED"], "args": sys.argv[1:]}) + "\n")
if target == os.environ.get("FAIL_TARGET"):
    sys.exit(23)
Path(sys.argv[sys.argv.index("-o") + 1]).write_bytes(("binary " + target).encode())
''')
        self.go.chmod(0o755)
        self.environment = dict(os.environ, GO=str(self.go), BUILD_LOG=str(self.log))

    def invoke(self, version="1.2.3", output=None, **environment):
        return subprocess.run(
            [sys.executable, str(package.ROOT / "scripts/release/package.py"), "--version", version,
             "--output-dir", str(output or self.work / "dist")],
            env=dict(self.environment, **environment), capture_output=True, text=True,
        )

    def test_four_archives_have_expected_content_and_portable_metadata(self):
        result = self.invoke("1.2.3-rc.1")
        self.assertEqual(result.returncode, 0, result.stderr)
        files = sorted((self.work / "dist").iterdir())
        self.assertEqual(len(files), 4)
        for target in package.TARGETS:
            stem = f"richt-1.2.3-rc.1-{target}"
            with tarfile.open(self.work / "dist" / f"{stem}.tar.gz") as archive:
                self.assertEqual(archive.getnames(), [stem, f"{stem}/richt", f"{stem}/README.md"])
                self.assertEqual(archive.extractfile(f"{stem}/richt").read(), f"binary {target}".encode())
                self.assertEqual(archive.extractfile(f"{stem}/README.md").read(), (package.ROOT / "README.md").read_bytes())
                self.assertEqual(archive.getmember(f"{stem}/richt").mode, 0o755)
                for member in archive.getmembers():
                    self.assertEqual((member.uid, member.gid, member.mtime), (0, 0, 0))
        builds = [json.loads(line) for line in self.log.read_text().splitlines()]
        self.assertEqual([build["target"] for build in builds], list(package.TARGETS))
        for build in builds:
            self.assertEqual(build["cgo"], "0")
            self.assertIn("-trimpath", build["args"])
            self.assertEqual(build["args"][build["args"].index("-ldflags") + 1],
                             "-X github.com/subuk/richt/cli.Version=1.2.3-rc.1")

    def test_identical_inputs_produce_identical_archives(self):
        first, second = self.work / "first", self.work / "second"
        self.assertEqual(self.invoke(output=first).returncode, 0)
        self.assertEqual(self.invoke(output=second).returncode, 0)
        for archive in first.iterdir():
            self.assertEqual(archive.read_bytes(), (second / archive.name).read_bytes())

    def test_rejects_invalid_versions_before_any_build_or_output(self):
        for version in ("dev", "v1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3+metadata",
                        "../1.2.3", "1.2.3;touch nope", "ci-0-abcdef0", "ci-12-abcdef", "1.2.3\n"):
            with self.subTest(version=version):
                result = self.invoke(version)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("version must be", result.stderr)
                self.assertNotIn("Traceback", result.stderr)
        self.assertFalse(self.log.exists())
        self.assertFalse((self.work / "dist").exists())

    def test_ci_version_is_embedded_and_named(self):
        result = self.invoke("ci-123-abcdef012345")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((self.work / "dist/richt-ci-123-abcdef012345-linux-amd64.tar.gz").exists())
        self.assertIn("cli.Version=ci-123-abcdef012345", self.log.read_text())

    def test_failed_build_does_not_publish_partial_archives(self):
        result = self.invoke(FAIL_TARGET="linux-arm64")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("23", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertFalse((self.work / "dist").exists())

    def test_missing_go_fails_cleanly(self):
        result = self.invoke(GO=str(self.work / "missing-go"))
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("missing-go", result.stderr)
        self.assertNotIn("Traceback", result.stderr)


if __name__ == "__main__":
    unittest.main()
