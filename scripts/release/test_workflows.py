"""Exercise workflow gates and publication with local command fakes, without GitHub."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import textwrap
import unittest
from unittest import mock


ROOT = Path(__file__).resolve().parents[2]


def workflow_script(filename, step_name):
    """Read a named literal run block; keep the tested code in the workflow itself."""
    text = (ROOT / ".github" / "workflows" / filename).read_text()
    step = re.search(rf"^      - name: {re.escape(step_name)}\n", text, re.MULTILINE)
    if step is None:
        raise AssertionError(f"Missing workflow step: {step_name}")
    body = re.split(r"^      - ", text[step.end():], maxsplit=1, flags=re.MULTILINE)[0]
    block = re.search(r"^        run: \|\n(?P<script>(?:^          .*\n|^\n)*)", body, re.MULTILINE)
    if block is None:
        raise AssertionError(f"Missing literal run block: {step_name}")
    return textwrap.dedent(block.group("script"))


def run_script(script, directory, **environment):
    env = dict(os.environ, **environment)
    return subprocess.run(
        ["bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script],
        cwd=directory,
        env=env,
        text=True,
        capture_output=True,
        timeout=15,
    )


def install_checksum_fallback(directory):
    """Ubuntu provides sha256sum; keep local tests usable on a clean macOS host."""
    if shutil.which("sha256sum") is None:
        command = directory / "sha256sum"
        command.write_text(
            "#!/usr/bin/env python3\n"
            "import hashlib, pathlib, sys\n"
            "for name in sys.argv[1:]:\n"
            "    print(hashlib.sha256(pathlib.Path(name).read_bytes()).hexdigest() + '  ' + name)\n"
        )
        command.chmod(0o755)


class WorkflowGateTests(unittest.TestCase):
    def test_required_check_rejects_any_unsuccessful_dependency(self):
        script = workflow_script("ci.yml", "Require every dependency to succeed")
        for result in ("success", "failure", "cancelled", "skipped"):
            with self.subTest(result=result):
                completed = run_script(
                    script,
                    ROOT,
                    RESULTS=json.dumps({"build": {"result": "success"}, "other": {"result": result}}),
                )
                self.assertEqual(completed.returncode == 0, result == "success", completed.stderr)
        self.assertNotEqual(run_script(script, ROOT, RESULTS="{}").returncode, 0)

    def test_build_requires_exact_sha(self):
        script = workflow_script("build.yml", "Require an exact source commit")
        for source in ("a" * 40, "main", "refs/pull/12/head", "abc1234", "A" * 40, "a" * 40 + "\n"):
            with self.subTest(source=source):
                completed = run_script(script, ROOT, SOURCE_SHA=source)
                self.assertEqual(completed.returncode == 0, source == "a" * 40)

    def test_release_request_validation(self):
        script = workflow_script("release.yml", "Validate release request")
        for version in ("0.1.0", "1.2.3-rc.1", "1.2.3-beta-2", "1.2.3-0", "v1.2.3", "01.2.3", "1.2", "1.2.3-01", "1.2.3+meta", "1.2.3\n", "$(touch injected)"):
            with self.subTest(version=version):
                completed = run_script(script, ROOT, VERSION=version, SOURCE_REF="main", WORKFLOW_REF="refs/heads/main")
                self.assertEqual(completed.returncode == 0, version in ("0.1.0", "1.2.3-rc.1", "1.2.3-beta-2", "1.2.3-0"), completed.stderr)
        for overrides in ({"WORKFLOW_REF": "refs/heads/other"}, {"SOURCE_REF": "--help"}, {"SOURCE_REF": "main\nother"}, {"SOURCE_REF": ""}):
            env = dict(VERSION="1.2.3", SOURCE_REF="main", WORKFLOW_REF="refs/heads/main")
            env.update(overrides)
            self.assertNotEqual(run_script(script, ROOT, **env).returncode, 0)

    def test_no_input_expression_is_interpolated_into_shell_code(self):
        steps = {
            "ci.yml": ["Require every dependency to succeed"],
            "build.yml": ["Require an exact source commit", "Run the full validation suite", "Smoke-test the native Linux archive"],
            "release.yml": ["Validate release request", "Resolve source and check existing tag", "Publish the complete release"],
        }
        for filename, names in steps.items():
            for name in names:
                with self.subTest(workflow=filename, step=name):
                    self.assertNotIn("${{", workflow_script(filename, name))


GH_FAKE = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

args = sys.argv[1:]
log = Path(os.environ['GH_TEST_LOG'])
with log.open('a') as stream:
    stream.write(json.dumps(args) + '\n')
scenario = os.environ['SCENARIO']
if args[:2] == ['release', 'create']:
    if scenario == 'create_failure':
        sys.exit(1)
elif args[0] == 'api':
    if '--method' in args:
        if scenario == 'publish_failure':
            sys.exit(1)
    elif any('/git/matching-refs/' in arg for arg in args):
        if scenario == 'tag_api_failure':
            sys.exit(1)
        if scenario in ('matching_tag', 'wrong_tag', 'moved_tag'):
            print('refs/tags/v' + os.environ['VERSION'])
    elif any('/commits/' in arg for arg in args):
        previous = [json.loads(line) for line in log.read_text().splitlines()]
        count = sum(any('/commits/' in arg for arg in call) for call in previous)
        wrong = scenario == 'wrong_tag' or (scenario == 'moved_tag' and count > 1)
        print('b' * 40 if wrong else os.environ['SOURCE_SHA'])
    elif '--paginate' in args:
        if scenario == 'releases_api_failure':
            sys.exit(1)
        if scenario == 'existing_release':
            print('42')
    elif any('/releases/tags/' in arg for arg in args):
        print('123')
    else:
        raise SystemExit('Unexpected API call: ' + repr(args))
else:
    raise SystemExit('Unexpected gh call: ' + repr(args))
'''


class PublicationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.dist = self.root / "dist"
        self.dist.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        fake = self.bin / "gh"
        fake.write_text(GH_FAKE)
        fake.chmod(0o755)
        install_checksum_fallback(self.bin)
        self.log = self.root / "gh.jsonl"
        self.script = workflow_script("release.yml", "Publish the complete release")

    def publish(self, scenario="success", version="1.2.3", change=None):
        for path in self.dist.iterdir():
            path.unlink()
        if self.log.exists():
            self.log.unlink()
        self.assets = [f"richt-{version}-{osname}-{arch}.tar.gz" for osname in ("linux", "darwin") for arch in ("amd64", "arm64")]
        for asset in self.assets:
            (self.dist / asset).write_bytes(asset.encode())
        if change:
            change()
        return run_script(
            self.script,
            self.dist,
            PATH=f"{self.bin}{os.pathsep}{os.environ['PATH']}",
            GH_TEST_LOG=str(self.log),
            SCENARIO=scenario,
            GH_REPO="test/richt",
            VERSION=version,
            SOURCE_SHA="a" * 40,
        )

    def calls(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_complete_assets_are_checksummed_then_published_from_draft(self):
        completed = self.publish("matching_tag")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        expected = [f"{hashlib.sha256(asset.encode()).hexdigest()}  {asset}" for asset in self.assets]
        self.assertEqual((self.dist / "SHA256SUMS").read_text().splitlines(), expected)
        calls = self.calls()
        create = next(call for call in calls if call[:2] == ["release", "create"])
        self.assertIn("--draft", create)
        self.assertIn("--target", create)
        self.assertEqual(create[create.index("--target") + 1], "a" * 40)
        self.assertEqual(create[-5:], self.assets + ["SHA256SUMS"])
        self.assertIn("draft=false", calls[-1])
        self.assertIn("make_latest=legacy", calls[-1])
        self.assertEqual(sum(any('/commits/' in arg for arg in call) for call in calls), 2)

    def test_prerelease_never_becomes_latest(self):
        completed = self.publish(version="1.2.3-rc.1")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertIn("--prerelease", next(call for call in self.calls() if call[:2] == ["release", "create"]))
        self.assertIn("make_latest=false", self.calls()[-1])

    def test_stdlib_checksum_fallback_without_coreutils(self):
        with mock.patch.object(shutil, "which", return_value=None):
            install_checksum_fallback(self.bin)
        self.assertTrue((self.bin / "sha256sum").is_file())
        completed = self.publish()
        self.assertEqual(completed.returncode, 0, completed.stderr)
        expected = [f"{hashlib.sha256(asset.encode()).hexdigest()}  {asset}" for asset in self.assets]
        self.assertEqual((self.dist / "SHA256SUMS").read_text().splitlines(), expected)

    def test_invalid_asset_set_fails_before_any_api_call(self):
        changes = (
            lambda: (self.dist / self.assets[0]).unlink(),
            lambda: (self.dist / "extra").write_text("unexpected"),
            lambda: (self.dist / ".hidden").write_text("unexpected"),
            lambda: (self.dist / self.assets[0]).write_bytes(b""),
            self.replace_archive_with_symlink,
        )
        for change in changes:
            with self.subTest(change=change):
                completed = self.publish(change=change)
                self.assertNotEqual(completed.returncode, 0)
                self.assertEqual(self.calls(), [])

    def replace_archive_with_symlink(self):
        first = self.dist / self.assets[0]
        first.unlink()
        first.symlink_to(self.assets[1])

    def test_tag_mismatch_existing_release_and_api_errors_never_create_release(self):
        for scenario in ("wrong_tag", "existing_release", "tag_api_failure", "releases_api_failure"):
            with self.subTest(scenario=scenario):
                completed = self.publish(scenario)
                self.assertNotEqual(completed.returncode, 0)
                self.assertFalse(any(call[:2] == ["release", "create"] for call in self.calls()))
                final_call = self.calls()[-1]
                if scenario == "wrong_tag":
                    self.assertTrue(any('/commits/' in arg for arg in final_call))
                elif scenario == "tag_api_failure":
                    self.assertTrue(any('/git/matching-refs/' in arg for arg in final_call))
                else:
                    self.assertIn("--paginate", final_call)

    def test_failed_create_or_changed_tag_never_publishes(self):
        for scenario in ("create_failure", "moved_tag"):
            with self.subTest(scenario=scenario):
                completed = self.publish(scenario)
                self.assertNotEqual(completed.returncode, 0)
                self.assertFalse(any("--method" in call for call in self.calls()))
                self.assertTrue(any(call[:2] == ["release", "create"] for call in self.calls()))
                if scenario == "moved_tag":
                    self.assertEqual(sum(any('/commits/' in arg for arg in call) for call in self.calls()), 2)

    def test_failed_publication_is_reported(self):
        self.assertNotEqual(self.publish("publish_failure").returncode, 0)


if __name__ == "__main__":
    unittest.main()
