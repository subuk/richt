#!/usr/bin/env python3
"""Smoke-test a native release archive without using checkout files or hosts."""

import argparse
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile
import tempfile

from package import ROOT, validate_version


def require(condition, message):
    if not condition:
        raise ValueError(message)


def native_target():
    systems = {"Linux": "linux", "Darwin": "darwin"}
    machines = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}
    try:
        return f"{systems[platform.system()]}-{machines[platform.machine()]}"
    except KeyError:
        raise ValueError("smoke test requires Linux or macOS on amd64 or arm64") from None


def extract_archive(archive_path, destination, stem):
    allowed = {stem, f"{stem}/richt", f"{stem}/README.md", f"{stem}/LICENSE"}
    with tarfile.open(archive_path, "r:gz") as archive:
        members = archive.getmembers()
        names = [member.name.rstrip("/") for member in members]
        require(len(set(names)) == len(names), "archive contains duplicate entries")
        require(set(names) <= allowed, "archive contains unexpected paths")
        require({f"{stem}/richt", f"{stem}/README.md"} <= set(names), "archive is missing richt or README.md")
        for member, name in zip(members, names):
            require((name == stem and member.isdir()) or (name != stem and member.isfile()),
                    "archive contains a link or unsupported entry")
        (destination / stem).mkdir()
        for member, name in zip(members, names):
            if name == stem:
                continue
            content = archive.extractfile(member)
            require(content is not None, "archive file cannot be read")
            with content, (destination / name).open("wb") as output:
                shutil.copyfileobj(content, output)
            os.chmod(destination / name, member.mode & 0o777)
        require((destination / stem / "richt").stat().st_mode & 0o111, "archive binary is not executable")
    return destination / stem / "richt"


def run(binary, work, *arguments):
    result = subprocess.run([str(binary), *map(str, arguments)], cwd=work,
                            stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=60)
    require(result.returncode == 0,
            f"{' '.join(map(str, arguments))} failed ({result.returncode}): {result.stderr.strip()}")
    return result.stdout


def statuses(saved):
    document = json.loads(saved.read_text())
    tasks = [task for host in document["hosts"] for task in host["tasks"]]
    require(bool(tasks), "saved plan has no file tasks")
    return [task["progress"]["status"] for task in tasks]


def exercise(binary, work, version):
    require(run(binary, work, "--version").strip() == f"richt {version}", "binary reports the wrong version")
    help_text = run(binary, work, "--help")
    require(all(word in help_text for word in ("Usage:", "inventory", "diff", "apply")), "incomplete command help")
    target, template = work / "managed" / "greeting.conf", work / "greeting.tmpl"
    inventory, saved = work / "inventory.yaml", work / "plan.json"
    template.write_text("release smoke {{ .greeting }}\n")
    inventory.write_text(json.dumps({
        "tags": {"smoke": {"services": {"greeting": {}}, "vars": {"greeting": "passed"}}},
        "services": {"greeting": {"order": 10, "configs": {"main": {
            "path": str(target), "mode": "0600", "template": {"file": str(template)}
        }}}},
        "hosts": [{"name": "local", "tags": ["smoke"], "transport": {"type": "local"}}],
    }))
    run(binary, work, "inventory", "validate", "-i", inventory)
    resolved = json.loads(run(binary, work, "inventory", "show", "-i", inventory))
    require([host["name"] for host in resolved["hosts"]] == ["local"], "inventory did not resolve the local host")
    run(binary, work, "diff", "-i", inventory, "-o", saved)
    require(not target.exists(), "diff changed the managed file")
    require(all(status == "pending" for status in statuses(saved)), "new plan has unexpected progress")
    require(saved.stat().st_mode & 0o777 == 0o600, "saved plan permissions are not private")
    # Apply must use prepared bytes after all preparation inputs disappear.
    inventory.unlink()
    template.unlink()
    run(binary, work, "apply", "-y", "--parallelism", "1", saved)
    require(target.read_text() == "release smoke passed\n", "saved apply did not write prepared bytes")
    require(target.stat().st_mode & 0o777 == 0o600, "target mode was not applied")
    require(all(status == "success" for status in statuses(saved)), "successful apply did not persist progress")
    # A continuation must skip successful tasks even if their target has drifted.
    target.write_text("changed after apply\n")
    continuation = run(binary, work, "apply", "-y", saved)
    require("previously completed" in continuation, "continuation did not report skipped tasks")
    require(target.read_text() == "changed after apply\n", "continuation replayed a successful task")


def smoke(archive, version):
    validate_version(version)
    stem = f"richt-{version}-{native_target()}"
    require(archive.name == f"{stem}.tar.gz", f"expected native archive {stem}.tar.gz")
    # TMPDIR may point inside the checkout. Select a temporary root outside it.
    temporary_root = next((path for path in (Path(tempfile.gettempdir()), Path("/tmp"))
                           if not path.resolve().is_relative_to(ROOT)), None)
    require(temporary_root is not None, "no temporary directory outside the checkout is available")
    with tempfile.TemporaryDirectory(prefix="richt-smoke-", dir=temporary_root) as temporary:
        work = Path(temporary).resolve()
        binary = extract_archive(archive, work, stem)
        exercise(binary, work, version)
    print(f"Smoke passed: {archive.name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", type=Path, required=True)
    parser.add_argument("--version", required=True)
    args = parser.parse_args()
    try:
        smoke(args.archive.resolve(), args.version)
    except (ValueError, OSError, KeyError, tarfile.TarError, subprocess.SubprocessError) as error:
        print(f"smoke: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
