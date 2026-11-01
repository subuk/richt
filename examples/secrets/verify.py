#!/usr/bin/env python3
"""Verify fictional secrets in a private, automatically cleaned local workspace."""

import argparse
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile


def check(condition):
    if not condition:
        raise AssertionError("example check failed; contents withheld")


def private_file(path, content):
    path.write_text(content)
    path.chmod(0o600)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    args = parser.parse_args()
    binary = args.binary.resolve()
    fixtures = Path(__file__).resolve().parent
    markers = (
        "fictional-default-password", "fictional-production-password",
        "fictional first line", "fictional second line",
    )
    expected = (
        "default=fictional-default-password\n"
        "production=fictional-production-password\n"
        'quoted="fictional-production-password"\n'
        "multiline:\nfictional first line\nfictional second line\n\n"
    )

    def run(*arguments):
        result = subprocess.run(
            [str(binary), *map(str, arguments)], capture_output=True, text=True,
        )
        output = result.stdout + result.stderr
        check(not any(marker in output for marker in markers))
        check(result.returncode == 0)
        return output

    def private(path):
        check(stat.S_IMODE(path.stat().st_mode) == 0o600)

    previous_umask = os.umask(0o077)
    try:
        with tempfile.TemporaryDirectory(prefix="richt-secret-example-") as name:
            work = Path(name)
            check(stat.S_IMODE(work.stat().st_mode) == 0o700)
            target = work / "app.conf"
            inventory = work / "inventory.yaml"
            source = (fixtures / "inventory.yaml").read_text().replace(
                "/var/tmp/richt-secret-example/app.conf", json.dumps(str(target)),
            )
            for filename in ("default.yaml", "production.yaml", "app.conf.gotmpl"):
                private_file(work / filename, (fixtures / filename).read_text())
            private_file(inventory, source)
            run("inventory", "validate", "-i", inventory)
            run("inventory", "show", "-i", inventory)
            plan = work / "prepared.plan.json"
            output = run("diff", "-i", inventory, "-o", plan)
            check("Content hidden: sensitive" in output and not target.exists())
            tasks = json.loads(plan.read_text())["hosts"][0]["tasks"]
            check(len(tasks) == 1)
            change = tasks[0]["file"]
            check(change["sensitive"] and change["content"] == expected)
            check(all(marker in change["patch"] for marker in markers))
            private(plan)
            private(Path(str(plan) + ".lock"))
            print("PASS independent sources, automatic sensitivity, and private plan")

            for filename in ("default.yaml", "production.yaml", "app.conf.gotmpl", "inventory.yaml"):
                (work / filename).unlink()
            run("apply", "-y", plan)
            check(target.read_text() == expected)
            private(target)
            private(plan)
            print("PASS frozen saved inputs after deleting sources, template, and inventory")

            # Re-create inputs only for a fresh no-op diff, then remove them again.
            for filename in ("default.yaml", "production.yaml", "app.conf.gotmpl"):
                private_file(work / filename, (fixtures / filename).read_text())
            private_file(inventory, source)
            noop = work / "noop.plan.json"
            run("diff", "-i", inventory, "-o", noop)
            check(not json.loads(noop.read_text())["hosts"][0]["tasks"])
            for filename in ("default.yaml", "production.yaml", "app.conf.gotmpl", "inventory.yaml"):
                (work / filename).unlink()
            private_file(target, "fictional-external-change\n")
            run("apply", "-y", plan)
            check(target.read_text() == "fictional-external-change\n")
            print("PASS no-op diff and continuation skipping successful tasks")

            # Old secret bytes need explicit protection when removal skips rendering.
            private_file(target, expected)
            absent = source.replace("state: present", "state: absent").replace(
                "sensitive: false", "sensitive: true",
            )
            private_file(inventory, absent)
            removal = work / "remove.plan.json"
            output = run("diff", "-i", inventory, "-o", removal)
            check("Content hidden: sensitive" in output)
            inventory.unlink()
            run("apply", "-y", removal)
            check(not target.exists())
            private(removal)
            print("PASS explicit-sensitive removal with missing template and sources")
    finally:
        os.umask(previous_umask)
    print("Secret example passed; private temporary files removed")


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, ValueError, KeyError, subprocess.SubprocessError):
        print("FAIL secret example; captured output and contents withheld", file=sys.stderr)
        sys.exit(1)
