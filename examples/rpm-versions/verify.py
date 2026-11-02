#!/usr/bin/env python3
"""Exercise upstream RPM requests with published packages on a disposable host."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("work", type=Path)
    parser.add_argument("--source-revision", default="uncommitted checkout")
    parser.add_argument("--ordered-packages", nargs=3, metavar="PACKAGE",
                        help="Three additional published packages, initially absent, to verify list ordering")
    args = parser.parse_args()
    binary = args.binary.resolve()
    work = args.work.resolve()
    work.mkdir(parents=True, exist_ok=False)
    if os.geteuid() != 0:
        raise RuntimeError("run as root on an authorized disposable host")
    repo_path = Path("/etc/yum.repos.d/richt-rpm-versions.repo")
    if repo_path.exists() or subprocess.run(["rpm", "-q", "vmango"], capture_output=True).returncode == 0:
        raise RuntimeError("vmango or acceptance repository already exists")
    if args.ordered_packages:
        if len(set(args.ordered_packages + ["vmango"])) != 4:
            raise RuntimeError("ordered packages must be distinct and exclude vmango")
        for package in args.ordered_packages:
            if subprocess.run(["rpm", "-q", "--", package], capture_output=True).returncode == 0:
                raise RuntimeError("ordered acceptance package already exists: " + package)
    evidence = {
        "source_revision": args.source_revision,
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "checks": [],
    }
    count = 0

    def run(command, success=True):
        nonlocal count
        count += 1
        result = subprocess.run([str(value) for value in command], text=True, capture_output=True)
        (work / f"command-{count:03}.log").write_text(
            "$ " + " ".join(str(value) for value in command) + "\n"
            + result.stdout + result.stderr + f"\nexit={result.returncode}\n"
        )
        if (result.returncode == 0) != success:
            raise RuntimeError(f"unexpected exit {result.returncode}; see command-{count:03}.log")
        return result.stdout + result.stderr

    def passed(name):
        evidence["checks"].append(name)
        (work / "acceptance.json").write_text(json.dumps(evidence, indent=2) + "\n")
        print("PASS " + name, flush=True)

    inventory_path = work / "inventory.json"
    inventory = {
        "tags": {"example": {"services": {"repository": {"state": "present"}, "package": {"state": "present"}}}},
        "services": {
            "repository": {"order": 10, "configs": [{"name": "repository",
                "path": str(repo_path), "mode": "0644", "template": {"inline":
                    "[richt-rpm-versions]\nname=Richt upstream RPM acceptance\n"
                    "baseurl=https://download.copr.fedorainfracloud.org/results/subuk/vmango/rhel-10-$basearch/\n"
                    "enabled=1\ngpgcheck=1\n"
                    "gpgkey=https://download.copr.fedorainfracloud.org/results/subuk/vmango/pubkey.gpg\n"
                    "repo_gpgcheck=0\n"}}]},
            "package": {"order": 20, "binaries": [{"name": "app", "type": "rpm", "package": "vmango", "version": "0.16.0"}]},
        },
        "hosts": [{"name": "local", "tags": ["example"], "transport": {"type": "local"}}],
    }

    if args.ordered_packages:
        binaries = inventory["services"]["package"]["binaries"]
        binaries[0]["name"] = "z-last"
        for package, name in zip(args.ordered_packages, (None, "a-first", None)):
            item = {"type": "rpm", "package": package}
            if name is not None:
                item["name"] = name
            binaries.append(item)

    def write(version):
        package = inventory["services"]["package"]["binaries"][0]
        if version is None:
            package.pop("version", None)
        else:
            package["version"] = version
        inventory_path.write_text(json.dumps(inventory, indent=2) + "\n")

    def prepare(label, version):
        write(version)
        plan = work / f"{label}.json"
        output = run([binary, "diff", "-i", inventory_path, "-o", plan])
        return plan, json.loads(plan.read_text()), output

    def upstream():
        return run(["rpm", "-q", "vmango", "--qf", "%{VERSION}"]).strip()

    evidence["platform"] = run(["sh", "-c", "cat /etc/os-release; uname -m; getenforce; systemctl --version | head -1; rpm --version; dnf --version | head -1"])
    plan, prepared, shown = prepare("install", "0.16.0")
    assert not repo_path.exists(), "diff wrote repository configuration"
    package_tasks = [task for task in prepared["hosts"][0]["tasks"] if task.get("rpm")]
    assert package_tasks[0]["rpm"]["version"] == "0.16.0"
    expected_packages = ["vmango"] + (args.ordered_packages or [])
    assert [task["rpm"]["package"] for task in package_tasks] == expected_packages
    if args.ordered_packages:
        passed("mixed named and unnamed binaries retain declaration order in saved install tasks")
    assert "at apply" in shown and "epoch, release, and architecture" in shown
    passed("short request saved and displayed before repository exists")
    run([binary, "apply", "-y", plan])
    assert upstream() == "0.16.0"
    if args.ordered_packages:
        run(["rpm", "-q", "--"] + args.ordered_packages)
        assert all(task["progress"]["status"] == "success" for task in json.loads(plan.read_text())["hosts"][0]["tasks"])
        passed("all ordered published package requests execute successfully")
    passed("upstream install resolves published RPM through DNF at apply")
    _, prepared, _ = prepare("noop", "0.16.0")
    assert not prepared["hosts"][0]["tasks"]
    passed("matching installed upstream version is no-op")
    for label, version in [("upgrade", "0.17.0"), ("downgrade", "0.16.0"), ("exact", "0:0.15.0-1.el10.x86_64")]:
        plan, _, _ = prepare(label, version)
        run([binary, "apply", "-y", plan])
        assert upstream() == ("0.15.0" if label == "exact" else version)
        passed(label + " published RPM request")
    _, prepared, _ = prepare("unversioned", None)
    assert not prepared["hosts"][0]["tasks"] and upstream() == "0.15.0"
    passed("omitted version preserves installed RPM")
    plan, _, _ = prepare("unavailable", "9999.0")
    failure = run([binary, "apply", "-y", plan], success=False)
    assert "failed" in failure.lower() or "error" in failure.lower()
    assert upstream() == "0.15.0"
    assert json.loads(plan.read_text())["hosts"][0]["tasks"][0]["progress"]["status"] == "error"
    passed("unavailable upstream version fails without changing installed RPM")
    plan, _, _ = prepare("saved", "0.16.0")
    inventory_path.unlink()
    run([binary, "apply", "-y", plan])
    assert upstream() == "0.16.0"
    saved = json.loads(plan.read_text())["hosts"][0]["tasks"][0]
    assert saved["rpm"]["version"] == "0.16.0" and saved["progress"]["status"] == "success"
    run([binary, "apply", "-y", plan])
    passed("saved short request survives removed inventory and successful continuation")
    inventory["tags"]["example"]["services"] = {"repository": {"state": "absent"}, "package": {"state": "absent"}}
    plan, prepared, _ = prepare("remove", "0.16.0")
    package_tasks = [task for task in prepared["hosts"][0]["tasks"] if task.get("rpm")]
    assert [task["rpm"]["package"] for task in package_tasks] == expected_packages[::-1]
    run([binary, "apply", "-y", plan])
    assert not repo_path.exists()
    run(["rpm", "-q", "vmango"], success=False)
    passed("cleanup removes declared package and repository")
    if args.ordered_packages:
        for package in args.ordered_packages:
            run(["rpm", "-q", "--", package], success=False)
        passed("absence reverses the binary list and removes every added acceptance package")


if __name__ == "__main__":
    main()
