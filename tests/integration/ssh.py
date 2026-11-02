#!/usr/bin/env python3
"""Exercise Richt against two explicitly supplied disposable SSH targets.

Both aliases must log in as non-root users with passwordless sudo and have the
repository from examples/rpm-systemd/build-repository.sh at its default path.
The controller's PATH, SSH agent, and SSH client configuration are preserved.
This suite replaces the isolated richt-demo fixture on both targets and retains
its plans, command output, and acceptance.json in the chosen work directory.
"""

import argparse
import hashlib
import json
import os
import re
import shlex
import subprocess
import tempfile
import time
import uuid
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--host-a", required=True, help="First disposable SSH alias")
    parser.add_argument("--host-b", required=True, help="Second disposable SSH alias")
    parser.add_argument("--auth-failure-host", help="Optional SSH alias with deliberately rejected authentication")
    parser.add_argument("--unreachable-host", help="Optional unreachable SSH alias (defaults to 127.0.0.1 port 1)")
    parser.add_argument("--work-root", type=Path, help="Directory for retained artifacts")
    args = parser.parse_args()
    for alias in filter(None, (args.host_a, args.host_b, args.auth_failure_host, args.unreachable_host)):
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", alias):
            parser.error("target arguments must be SSH aliases, without user@ or options")
    if args.host_a == args.host_b:
        parser.error("two distinct disposable target aliases are required")
    binary = str(args.binary.resolve())
    work = args.work_root.resolve() if args.work_root else Path(tempfile.mkdtemp(prefix="richt-ssh-"))
    work.mkdir(parents=True, exist_ok=True)
    root = "/var/tmp/richt-ssh-" + uuid.uuid4().hex[:12]
    aliases = {"a-target": args.host_a, "b-target": args.host_b}
    checks = []
    secret_markers = []
    commands = 0
    report = {"targets": aliases, "remote_root": root, "checks": checks, "status": "running"}

    def run(argv, ok=True):
        nonlocal commands
        result = subprocess.run([str(x) for x in argv], text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, timeout=180)
        if any(marker in result.stdout for marker in secret_markers):
            raise AssertionError("secret marker escaped through command output")
        commands += 1
        (work / f"command-{commands:03}.log").write_text(
            "$ " + shlex.join([str(x) for x in argv]) + "\n" + result.stdout)
        if ok and result.returncode:
            raise AssertionError(f"command failed ({result.returncode}): {argv}\n{result.stdout}")
        return result

    def remote(alias, script, ok=True, sudo=True):
        command = ["sudo", "-n", "sh", "-c", script] if sudo else ["sh", "-c", script]
        return run(["ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                    "--", alias, shlex.join(command)], ok=ok)

    def check(name, condition):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print(f"PASS {name}", flush=True)

    def diff(name, source):
        inventory = work / (name + "-inventory.yaml")
        inventory.write_text(source if isinstance(source, str) else json.dumps(source, indent=2))
        target = work / (name + "-plan.json")
        result = run([binary, "diff", "-i", inventory, "-o", target])
        (work / (name + ".diff")).write_text(result.stdout)
        return inventory, target, json.loads(target.read_text())

    def apply(name, target, parallelism=2, ok=True):
        result = run([binary, "apply", "-y", "--parallelism", str(parallelism), target], ok=ok)
        (work / (name + ".apply")).write_text(result.stdout)
        return result

    def file_inventory(configs, hosts=None):
        return {"tags": {"files": {"services": {"files": {}}}},
                "services": {"files": {"order": 10, "configs": configs}}, "hosts": hosts or [
            {"name": name, "transport": {"type": "ssh", "host": alias, "sudo": True},
             "tags": ["files"]}
            for name, alias in aliases.items()]}

    def config(path, content):
        return {"path": path, "owner": "root", "group": "root", "mode": "0644",
                "template": {"inline": content}}

    def events(alias):
        return remote(alias, "cat /run/richt-demo/events").stdout.splitlines()

    def wait_reload(alias, count):
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if events(alias).count("reload") == count:
                return True
            time.sleep(0.1)
        return False

    try:
        print(f"Artifacts: {work}", flush=True)
        for alias in aliases.values():
            check(alias + " non-root login", remote(alias, "id -u", sudo=False).stdout.strip() != "0")
            remote(alias, "test -d /var/lib/richt-demo-fixture/repo/repodata")
            remote(alias, "set -e\nsystemctl stop richt-demo.service || :\n"
                   "systemctl disable richt-demo.service || :\n"
                   "dnf -y remove richt-demo\n"
                   "rm -f /etc/yum.repos.d/richt-demo.repo /etc/richt-demo.conf "
                   "/etc/systemd/system/richt-demo.service.d/20-reload.conf /run/richt-demo/events\n"
                   "systemctl daemon-reload\nmkdir -p " + shlex.quote(root))

        # Reuse the shipped package/config/process inputs and repository builder.
        fixture = Path(__file__).resolve().parents[2] / "examples/rpm-systemd/inventory.yaml"
        services = fixture.read_text().split("\nhosts:", 1)[0]
        hosts = "\nhosts:\n" + "".join(
            "  - name: " + name + "\n    transport: {type: ssh, host: " + json.dumps(alias) +
            ", sudo: true}\n    tags: [example]\n" for name, alias in aliases.items())
        _, target, prepared = diff("first", services + hosts)
        check("both SSH hosts prepare package requests", all(
            any(task["operation"] == "rpm" for task in host["tasks"]) for host in prepared["hosts"]))
        apply("first", target)
        for alias in aliases.values():
            actual = remote(alias, "rpm -q --qf '%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}' richt-demo; "
                            "printf '\\n'; systemctl is-active richt-demo.service; "
                            "systemctl is-enabled richt-demo.service; cat /etc/richt-demo.conf").stdout
            check(alias + " exact package, active enabled process and config", actual.splitlines() == [
                "0:1.0-1.noarch", "active", "enabled", "message=hello"])
        _, _, noop = diff("noop", services + hosts)
        check("two-host repeat diff has no tasks", all(not host["tasks"] for host in noop["hosts"]))

        before = {alias: events(alias).count("reload") for alias in aliases.values()}
        _, target, reload_plan = diff("reload", services.replace("message=hello", "message=reloaded") + hosts)
        check("each changed config schedules one reload", all(
            [task["process"].get("reaction") for task in host["tasks"] if task["operation"] == "systemd"] == ["reload"]
            for host in reload_plan["hosts"]))
        apply("reload", target)
        for alias in aliases.values():
            check(alias + " config reload executes", wait_reload(alias, before[alias] + 1))

        inventory, target, frozen = diff("frozen", file_inventory({"frozen": config(root + "/frozen", "frozen input\n")}))
        check("saved files preserve owner and group names", all(
            task["file"].get("owner") == "root" and task["file"].get("group") == "root" and
            "uid" not in task["file"] and "gid" not in task["file"]
            for host in frozen["hosts"] for task in host["tasks"]))
        inventory.unlink()
        apply("frozen-without-inventory", target, parallelism=1)
        check("saved plan executes without inventory", all(
            remote(alias, "cat " + shlex.quote(root + "/frozen")).stdout == "frozen input\n" for alias in aliases.values()))

        # Only these uniquely named account records are created and removed.
        # Their files stay in the test root as inspectable acceptance artifacts.
        identity = "richt_ssh_" + root.rsplit("-", 1)[1]
        quoted_identity = shlex.quote(identity)
        remote(args.host_a, "if getent passwd " + quoted_identity + " >/dev/null || getent group " +
               quoted_identity + " >/dev/null; then echo 'test identity already exists'; exit 1; fi")
        try:
            remote(args.host_a, "set -e\ngroupadd -- " + quoted_identity +
                   "\nuseradd -M -N -g " + quoted_identity + " -s /sbin/nologin -- " + quoted_identity)
            old_ids = remote(args.host_a, "printf '%s:%s\\n' \"$(id -u " + quoted_identity +
                             ")\" \"$(getent group " + quoted_identity + " | cut -d: -f3)\"").stdout.strip()
            host_a = [{"name": "a-target", "transport": {"type": "ssh", "host": args.host_a, "sudo": True},
                       "tags": ["files"]}]
            named = config(root + "/named-ids", "names resolve at apply\n")
            named.update(owner=identity, group=identity)
            _, target, named_plan = diff("named-ids", file_inventory({"named": named}, hosts=host_a))
            named_file = named_plan["hosts"][0]["tasks"][0]["file"]
            check("changing-ID plan contains names without numeric ownership", named_file.get("owner") == identity and
                  named_file.get("group") == identity and "uid" not in named_file and "gid" not in named_file)
            new_ids = remote(args.host_a, "set -eu\nnew_uid=60000\n"
                             "while getent passwd \"$new_uid\" >/dev/null; do\n"
                             "  test \"$new_uid\" -lt 65000\n  new_uid=$((new_uid + 1))\ndone\n"
                             "new_gid=60000\nwhile getent group \"$new_gid\" >/dev/null; do\n"
                             "  test \"$new_gid\" -lt 65000\n  new_gid=$((new_gid + 1))\ndone\n"
                             "usermod -u \"$new_uid\" -- " + quoted_identity +
                             "\ngroupmod -g \"$new_gid\" -- " + quoted_identity +
                             "\nprintf '%s:%s\\n' \"$new_uid\" \"$new_gid\"").stdout.strip()
            report["named_identity"] = {"name": identity, "prepared_ids": old_ids, "apply_ids": new_ids}
            check("disposable user and group IDs changed after preparation", all(
                before != after for before, after in zip(old_ids.split(":"), new_ids.split(":"))))
            apply("named-ids", target, parallelism=1)
            check("saved owner and group names resolve current target IDs", remote(
                args.host_a, "stat -c '%u:%g' -- " + shlex.quote(root + "/named-ids")).stdout.strip() == new_ids)

            owner_write = root + "/owner-only-write"
            owner_attributes = root + "/owner-only-attributes"
            remote(args.host_a, "set -e\nprintf 'old content\\n' > " + shlex.quote(owner_write) +
                   "\nprintf 'unchanged content\\n' > " + shlex.quote(owner_attributes) +
                   "\nchown root:root -- " + shlex.quote(owner_write) + " " + shlex.quote(owner_attributes) +
                   "\nchmod 0644 -- " + shlex.quote(owner_write) + " " + shlex.quote(owner_attributes))
            _, target, owner_plan = diff("owner-only", file_inventory({
                "write": {"path": owner_write, "owner": identity, "template": {"inline": "new content\n"}},
                "attributes": {"path": owner_attributes, "owner": identity, "mode": "0600",
                               "template": {"inline": "unchanged content\n"}}}, hosts=host_a))
            check("owner-only plan covers a write and an attribute-only change", sorted(
                bool(task["file"].get("write")) for task in owner_plan["hosts"][0]["tasks"]) == [False, True] and
                all("group" not in task["file"] and "gid" not in task["file"]
                    for task in owner_plan["hosts"][0]["tasks"]))
            apply("owner-only", target, parallelism=1)
            root_gid = remote(args.host_a, "getent group root | cut -d: -f3").stdout.strip()
            expected = new_ids.split(":")[0] + ":" + root_gid
            actual_ids = remote(args.host_a, "stat -c '%u:%g' -- " + shlex.quote(owner_write) + " " +
                                shlex.quote(owner_attributes)).stdout.splitlines()
            check("owner-only writes and attributes preserve an existing different group", actual_ids == [expected, expected])
        finally:
            # userdel refuses active users; never kill target processes or remove
            # their files. Account removal precedes optional primary-group removal.
            remote(args.host_a, "set -e\nif getent passwd " + quoted_identity + " >/dev/null; then userdel -- " +
                   quoted_identity + "; fi\nif getent group " + quoted_identity + " >/dev/null; then groupdel -- " +
                   quoted_identity + "; fi\n! getent passwd " + quoted_identity + " >/dev/null\n! getent group " +
                   quoted_identity + " >/dev/null")
            check("disposable named user and group removed", True)

        for alias in aliases.values():
            remote(alias, "mkdir -p " + shlex.quote(root + "/parent") +
                   "; printf old > " + shlex.quote(root + "/actual") +
                   "; ln -s actual " + shlex.quote(root + "/link") +
                   "; ln -s parent " + shlex.quote(root + "/parent-link"))
        _, target, _ = diff("symlinks", file_inventory({
            "target": config(root + "/link", "followed target\n"),
            "parent": config(root + "/parent-link/child", "followed parent\n")}))
        apply("symlinks", target)
        check("target and parent symlinks follow ordinary semantics", all(
            remote(alias, "cat " + shlex.quote(root + "/actual") + " " +
                   shlex.quote(root + "/parent/child")).stdout == "followed target\nfollowed parent\n"
            for alias in aliases.values()))

        mixed = {"tags": {
            "local": {"services": {"localfiles": {}}},
            "remote": {"services": {"remotefiles": {}}}}, "services": {
            "localfiles": {"order": 10, "configs": {"mixed": {"path": str(work / "mixed-local"), "template": {"inline": "mixed\n"}}}},
            "remotefiles": {"order": 10, "configs": {"mixed": config(root + "/mixed", "mixed\n")}}}, "hosts": [
                {"name": "local", "transport": {"type": "local"}, "tags": ["local"]},
                {"name": "remote", "transport": {"type": "ssh", "host": args.host_a, "sudo": True},
                 "tags": ["remote"]}]}
        _, target, _ = diff("mixed", mixed)
        apply("mixed", target)
        check("mixed local and SSH plan executes", (work / "mixed-local").read_text() == "mixed\n" and
              remote(args.host_a, "cat " + shlex.quote(root + "/mixed")).stdout == "mixed\n")

        progress = file_inventory({
            "a-first": config(root + "/progress-first", "first\n"),
            "b-fail": config(root + "/progress-fail", "second\n"),
            "c-blocked": config(root + "/progress-blocked", "third\n")})
        _, target, _ = diff("progress", progress)
        remote(args.host_a, "mkdir " + shlex.quote(root + "/progress-fail"))
        failure = apply("progress-failure", target, parallelism=1, ok=False)
        saved = json.loads(target.read_text())
        check("failed host preserves successes and queued host completes", failure.returncode != 0 and
              [task["progress"]["status"] for task in saved["hosts"][0]["tasks"]] == ["success", "error", "pending"] and
              all(task["progress"]["status"] == "success" for task in saved["hosts"][1]["tasks"]))
        remote(args.host_a, "rmdir " + shlex.quote(root + "/progress-fail") +
               "; printf 'external change\\n' > " + shlex.quote(root + "/progress-first"))
        continued = apply("progress-resume", target, parallelism=1)
        check("explicit continuation skips successful tasks", "previously completed" in continued.stdout and
              remote(args.host_a, "cat " + shlex.quote(root + "/progress-first")).stdout == "external change\n" and
              all(task["progress"]["status"] == "success" for host in json.loads(target.read_text())["hosts"] for task in host["tasks"]))

        # The only secret inputs here are synthetic runtime canaries. Compare
        # remote bytes by digest so neither raw values nor commands contain them.
        secret_markers.extend("RICHT_TEST_" + uuid.uuid4().hex for _ in range(3))
        default_value = secret_markers[0] + "\n" + secret_markers[1] + "\n"
        alternate_value = secret_markers[2]
        source_default = work / "secrets-default.yaml"
        source_alternate = work / "secrets-alternate.yaml"
        for source_path, value in [(source_default, default_value), (source_alternate, alternate_value)]:
            with os.fdopen(os.open(source_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "w") as stream:
                os.fchmod(stream.fileno(), 0o600)
                json.dump({"database": {"password": value}}, stream)
        secret_files = file_inventory({
            "a-first": dict(config(root + "/secret-first", '{{secret "database.password"}}'), mode="0600"),
            "b-fail": dict(config(root + "/secret-second", '{{secret "database.password" "alternate"}}'), mode="0600")})
        secret_files["secret_sources"] = {
            "default": {"type": "yaml", "file": source_default.name},
            "alternate": {"type": "yaml", "file": source_alternate.name}}
        _, target, prepared = diff("secrets", secret_files)
        check("SSH plans retain independent sources and automatic sensitivity", all(
            [task["file"]["content"] for task in host["tasks"]] == [default_value, alternate_value] and
            all(task["file"].get("sensitive") for task in host["tasks"])
            for host in prepared["hosts"]))
        remote(args.host_a, "mkdir " + shlex.quote(root + "/secret-second"))
        failed = apply("secrets-failure", target, parallelism=1, ok=False)
        saved_secrets = json.loads(target.read_text())
        check("SSH secret failure preserves completed host and task progress", failed.returncode != 0 and
              [task["progress"]["status"] for task in saved_secrets["hosts"][0]["tasks"]] == ["success", "error"] and
              all(task["progress"]["status"] == "success" for task in saved_secrets["hosts"][1]["tasks"]))
        source_default.unlink()
        source_alternate.unlink()
        remote(args.host_a, "rmdir " + shlex.quote(root + "/secret-second"))
        resumed = apply("secrets-resume-without-sources", target)
        check("SSH secret continuation skips successes without source files", "previously completed" in resumed.stdout)
        expected_digests = [hashlib.sha256(value.encode()).hexdigest() for value in [default_value, alternate_value]]
        check("SSH sudo writes frozen multiline secret bytes", all(
            [line.split()[0] for line in remote(alias, "sha256sum -- " + shlex.quote(root + "/secret-first") +
             " " + shlex.quote(root + "/secret-second")).stdout.splitlines()] == expected_digests
            for alias in aliases.values()))
        check("SSH secret files retain requested restrictive modes", all(
            remote(alias, "stat -c '%a' -- " + shlex.quote(root + "/secret-first") + " " +
                   shlex.quote(root + "/secret-second")).stdout.splitlines() == ["600", "600"]
            for alias in aliases.values()))

        # Prepare reachable hosts, then change only saved connection data. This
        # simulates a target becoming unreachable after preparation, while
        # preserving exactly the prepared operations and their order.
        failures = [("unreachable", args.unreachable_host)]
        if args.auth_failure_host:
            failures.append(("authentication", args.auth_failure_host))
        for label, failure_alias in failures:
            _, target, failed_connection = diff(label, file_inventory({"connection": config(root + "/" + label, "reachable peer\n")}))
            if failure_alias:
                failed_connection["hosts"][0]["connection"]["host"] = failure_alias
            else:
                failed_connection["hosts"][0]["connection"].update(host="127.0.0.1", port=1, user="richt-unreachable")
            target.write_text(json.dumps(failed_connection, indent=2) + "\n")
            unavailable = apply(label, target, parallelism=1, ok=False)
            finished = json.loads(target.read_text())
            check(label + " failure is independent of queued peer", unavailable.returncode != 0 and
                  finished["hosts"][0]["tasks"][0]["progress"]["status"] == "error" and
                  finished["hosts"][1]["tasks"][0]["progress"]["status"] == "success")
            check(label + " queued peer operation really executed", remote(args.host_b, "cat " + shlex.quote(root + "/" + label)).stdout == "reachable peer\n")

        invalid = run([binary, "apply", "-y", "--parallelism", "0", target], ok=False)
        check("nonpositive CLI parallelism rejected", invalid.returncode != 0 and "parallelism must be positive" in invalid.stdout)
        report["status"] = "passed"
    except Exception as error:
        report["status"] = "failed"
        report["error"] = str(error)
        raise
    finally:
        (work / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print(f"Acceptance report: {work / 'acceptance.json'}", flush=True)


if __name__ == "__main__":
    main()
