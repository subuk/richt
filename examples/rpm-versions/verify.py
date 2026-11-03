#!/usr/bin/env python3
"""Exercise upstream RPM requests with published packages on a disposable host.

Run locally as root, or pass --host for SSH with sudo -n. The controller binary
must match the controller OS; package operations run on the selected target.
Requires DNF4/YUM4 and access to the published Vmango COPR repository on RHEL 10
x86_64. Vmango and the acceptance repository must initially be absent.
--disable-excludes adds repository-ID/all exclusion coverage for both managers;
main-scope argv is covered by Go tests without editing global DNF configuration.
Private plans and command logs remain in the newly created work directory.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("work", type=Path)
    parser.add_argument("--source-revision", default="uncommitted checkout")
    parser.add_argument("--host", help="SSH alias or user@host with noninteractive sudo -n")
    parser.add_argument("--disable-excludes", action="store_true",
                        help="Exercise DNF/YUM exclusion overrides using the acceptance repository")
    parser.add_argument("--ordered-packages", nargs=3, metavar="PACKAGE",
                        help="Three additional published packages, initially absent, to verify list ordering")
    args = parser.parse_args()
    binary = args.binary.resolve()
    work = args.work.resolve()
    work.mkdir(mode=0o700, parents=True, exist_ok=False)
    if not args.host and os.geteuid() != 0:
        raise RuntimeError("run as root on an authorized disposable host")
    repo_path = Path("/etc/yum.repos.d/richt-rpm-versions.repo")

    def target_command(command):
        if args.host:
            return ["ssh", "-T", "-o", "BatchMode=yes", "--", args.host,
                    shlex.join(["sudo", "-n", "--"] + [str(value) for value in command])]
        return command

    def target_probe(command, expected=(0,)):
        result = subprocess.run(target_command(command), text=True, capture_output=True, timeout=60)
        if result.returncode not in expected:
            raise RuntimeError("target probe failed with exit " + str(result.returncode)
                               + ": " + shlex.join(str(value) for value in command)
                               + "\n" + result.stderr)
        return result

    def repo_exists():
        return target_probe(["test", "-e", str(repo_path)], expected=(0, 1)).returncode == 0

    if target_probe(["id", "-u"]).stdout.strip() != "0":
        raise RuntimeError("target requires root privileges (SSH uses sudo -n)")
    installed = set(target_probe(["rpm", "-qa", "--qf", "%{NAME}\n"]).stdout.splitlines())
    if repo_exists() or "vmango" in installed:
        raise RuntimeError("vmango or acceptance repository already exists")
    if args.ordered_packages:
        if len(set(args.ordered_packages + ["vmango"])) != 4:
            raise RuntimeError("ordered packages must be distinct and exclude vmango")
        for package in args.ordered_packages:
            if package in installed:
                raise RuntimeError("ordered acceptance package already exists: " + package)
    evidence = {
        "source_revision": args.source_revision,
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "checks": [],
        "disable_excludes": args.disable_excludes,
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

    def target(command, success=True):
        return run(target_command(command), success)

    def assert_package_absent(package):
        names = target(["rpm", "-qa", "--qf", "%{NAME}\n"]).splitlines()
        assert package not in names, "acceptance package remains installed: " + package

    def assert_exact_nevra(version):
        installed = target(["rpm", "-q", "vmango", "--qf",
                            "%{NAME}-%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}"]).strip()
        assert installed == "vmango-" + version, "installed NEVRA does not match exact request: " + installed

    transport = {"type": "local"}
    if args.host:
        transport = {"type": "ssh", "host": args.host, "sudo": True}
        if "@" in args.host:
            transport["user"], transport["host"] = args.host.rsplit("@", 1)
    evidence["transport"] = transport

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
        "hosts": [{"name": "target", "tags": ["example"], "transport": transport}],
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

    def prepare(label, version, package_only=False):
        write(version)
        plan = work / f"{label}.json"
        command = [binary, "diff", "-i", inventory_path, "-o", plan]
        if package_only:
            command += ["--service", "package"]
        output = run(command)
        return plan, json.loads(plan.read_text()), output

    def upstream():
        return target(["rpm", "-q", "vmango", "--qf", "%{VERSION}"]).strip()

    evidence["platform"] = target(["sh", "-c", "cat /etc/os-release; uname -m; getenforce; systemctl --version | head -1; rpm --version; dnf --version | head -1; yum --version | head -1"])
    plan, prepared, shown = prepare("install", "0.16.0")
    assert not repo_exists(), "diff wrote repository configuration"
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
        target(["rpm", "-q", "--"] + args.ordered_packages)
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
        if label == "exact":
            assert_exact_nevra(version)
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
    if args.disable_excludes:
        binaries = inventory["services"]["package"]["binaries"]
        package = binaries[0]
        inventory["services"]["package"]["binaries"] = [package]
        assignments = inventory["tags"]["example"]["services"]
        repository = inventory["services"]["repository"]["configs"][0]
        repository["template"]["inline"] += "excludepkgs=vmango*\n"
        plan, prepared, _ = prepare("configure-excludes", "0.16.0")
        assert not any(task.get("rpm") for task in prepared["hosts"][0]["tasks"])
        run([binary, "apply", "-y", plan])
        excluded_config = target(["cat", str(repo_path)])
        assert "excludepkgs=vmango*" in excluded_config

        def assert_repository_unchanged():
            assert target(["cat", str(repo_path)]) == excluded_config, "override changed repository configuration"

        def remove_excluded(label, scope):
            package["disable_excludes"] = scope
            assignments["package"]["state"] = "absent"
            plan, prepared, shown = prepare(label, None, package_only=True)
            tasks = prepared["hosts"][0]["tasks"]
            assert len(tasks) == 1 and tasks[0]["rpm"]["state"] == "absent"
            assert tasks[0]["rpm"]["disable_excludes"] == scope
            assert "--disableexcludes=" + scope in shown
            run([binary, "apply", "-y", plan])
            assert_package_absent("vmango")
            assert_repository_unchanged()
            assignments["package"]["state"] = "present"

        remove_excluded("excluded-initial-remove", "richt-rpm-versions")
        for manager in ("dnf", "yum"):
            package["manager"] = manager
            for form, version, scope in (("unversioned", None, "richt-rpm-versions"),
                                         ("upstream", "0.16.0", "all"),
                                         ("exact", "0:0.15.0-1.el10.x86_64", "richt-rpm-versions")):
                label = "excluded-" + manager + "-" + form
                package.pop("disable_excludes", None)
                plan, prepared, shown = prepare(label + "-blocked", version, package_only=True)
                task = prepared["hosts"][0]["tasks"][0]
                assert "disable_excludes" not in task["rpm"] and "--disableexcludes" not in shown
                run([binary, "apply", "-y", plan], success=False)
                assert json.loads(plan.read_text())["hosts"][0]["tasks"][0]["progress"]["status"] == "error"
                assert_package_absent("vmango")
                assert_repository_unchanged()
                passed(manager + " " + form + " request respects configured exclusions when override is omitted")

                package["disable_excludes"] = scope
                plan, prepared, shown = prepare(label + "-override", version, package_only=True)
                task = prepared["hosts"][0]["tasks"][0]
                assert task["rpm"]["disable_excludes"] == scope and "--disableexcludes=" + scope in shown
                inventory_path.unlink()
                run([binary, "apply", "-y", plan])
                installed = upstream()
                assert version is None or installed == ("0.15.0" if form == "exact" else version)
                if form == "exact":
                    assert_exact_nevra(version)
                run([binary, "apply", "-y", plan])
                assert json.loads(plan.read_text())["hosts"][0]["tasks"][0]["progress"]["status"] == "success"
                assert_repository_unchanged()
                package["disable_excludes"] = "all" if scope != "all" else "richt-rpm-versions"
                _, prepared, _ = prepare(label + "-override-only-noop", version, package_only=True)
                assert not prepared["hosts"][0]["tasks"]
                passed(manager + " " + form + " saved override installs excluded published RPM and override-only change is no-op")
                remove_excluded(label + "-remove", package["disable_excludes"])
                passed(manager + " " + form + " excluded removal preserves repository configuration")
        package["disable_excludes"] = "all"
        plan, _, _ = prepare("excluded-final-install", "0.16.0", package_only=True)
        run([binary, "apply", "-y", plan])
        assert upstream() == "0.16.0"
        assert_repository_unchanged()
        inventory["services"]["package"]["binaries"] = binaries
        evidence["disable_excludes_scope_limit"] = "Repository ID and all exercised against repo exclusions; main argv is covered by Go tests without modifying global DNF configuration."
        passed("exclusion overrides leave acceptance repository configuration unchanged")
    inventory["tags"]["example"]["services"] = {"repository": {"state": "absent"}, "package": {"state": "absent"}}
    plan, prepared, _ = prepare("remove", "0.16.0")
    package_tasks = [task for task in prepared["hosts"][0]["tasks"] if task.get("rpm")]
    assert [task["rpm"]["package"] for task in package_tasks] == expected_packages[::-1]
    run([binary, "apply", "-y", plan])
    assert not repo_exists()
    assert_package_absent("vmango")
    passed("cleanup removes declared package and repository")
    if args.ordered_packages:
        for package in args.ordered_packages:
            assert_package_absent(package)
        passed("absence reverses the binary list and removes every added acceptance package")


if __name__ == "__main__":
    main()
