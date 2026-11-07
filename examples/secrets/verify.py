#!/usr/bin/env python3
"""Verify fictional YAML secrets, with optional Linux local/SSH env acceptance."""

import argparse
import copy
import hashlib
import json
import os
import re
import shlex
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import uuid


def check(condition):
    if not condition:
        raise AssertionError("example check failed; contents withheld")


def private_file(path, content):
    path.write_text(content)
    path.chmod(0o600)


def verify_yaml(binary):
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


def verify_env(binary, args):
    """Exercise the same preparation contract with controller-side env bindings."""
    fixtures = Path(__file__).resolve().parent
    names = ("RICHT_EXAMPLE_PASSWORD", "RICHT_EXAMPLE_EMPTY", "RICHT_EXAMPLE_MULTILINE",
             "RICHT_EXAMPLE_MISSING", "RICHT_EXAMPLE_REQUIRED")
    clean_env = {key: value for key, value in os.environ.items() if key not in names}
    values = dict(clean_env, RICHT_EXAMPLE_PASSWORD="fictional-env-password-first",
                  RICHT_EXAMPLE_EMPTY="", RICHT_EXAMPLE_MULTILINE="fictional env first line\nfictional env second line\n")
    markers = ("fictional-env-password-first", "fictional-env-password-second",
               "fictional-default-password", "fictional env first line", "fictional env second line")
    root = "/var/tmp/richt-env-secret-" + uuid.uuid4().hex
    connection = {"type": "ssh" if args.host else "local"}
    if args.host:
        user, separator, host = args.host.rpartition("@")
        connection["host"] = host if separator else args.host
        if separator:
            connection["user"] = user
    if args.sudo:
        connection["sudo"] = True
    previous_umask = os.umask(0o077)
    temporary = None
    if args.work_root:
        args.work_root.mkdir(parents=True, exist_ok=True)
        work = Path(tempfile.mkdtemp(prefix="richt-env-secret-", dir=args.work_root)).resolve()
    else:
        temporary = tempfile.TemporaryDirectory(prefix="richt-env-secret-")
        work = Path(temporary.name)
    checks = []
    report = {"status": "running", "transport": connection["type"], "sudo": args.sudo,
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "checks": checks}
    commands = 0
    created = False

    def record(name, condition=True):
        check(condition)
        checks.append(name)
        print("PASS " + name, flush=True)

    def target(script, ok=True):
        command = ["sudo", "-n", "--", "sh", "-c", script] if args.sudo else ["sh", "-c", script]
        if args.host:
            command = ["ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                       "--", args.host, shlex.join(command)]
        result = subprocess.run(command, capture_output=True, text=True, timeout=90, env=clean_env)
        if ok:
            check(result.returncode == 0)
        return result

    def run(*arguments, env=None, ok=True):
        nonlocal commands
        result = subprocess.run([str(binary), *map(str, arguments)], capture_output=True, text=True,
                                timeout=90, env=clean_env if env is None else env)
        commands += 1
        output = result.stdout + result.stderr
        # Keep even a regression's leaked bytes out of retained reports and terminal output.
        check(not any(marker in output for marker in markers))
        private_file(work / f"command-{commands:03}.log", output)
        check((result.returncode == 0) if ok else (result.returncode != 0))
        return result

    def write_document(path, document):
        private_file(path, json.dumps(document, indent=2) + "\n")

    def tasks(plan):
        return json.loads(plan.read_text())["hosts"][0]["tasks"]

    def read_target(name):
        return target("cat -- " + shlex.quote(root + "/" + name)).stdout

    def private_target(name):
        # GNU stat is deliberate: this optional suite requires a Linux target.
        return target("stat -c %a -- " + shlex.quote(root + "/" + name)).stdout.strip() == "600"

    inventory = work / "env-inventory.yaml"
    template = work / "env-app.conf.gotmpl"
    yaml_source = work / "default.yaml"
    client = work / "source-config.yaml"
    sources = {
        "default": {"type": "env", "env": {"credential.password": names[0], "optional": names[1], "message": names[2]}},
        "yaml": {"type": "yaml", "file": "default.yaml"},
        "unused": {"type": "env", "env": {"unused.key": names[3]}},
    }
    # This process intentionally prints config bytes on both output streams. Richt must
    # suppress them on success and failure, including plain progress and saved errors.
    hook = ('cat "$1"; cat "$1" >&2; '
            'if test -e "$2"; then sleep 1; exit 29; fi; '
            'printf "reaction\\n" >> "$3"')
    document = {
        "tags": {"example": {"services": {"application": {"state": "present"}}}},
        "secret_sources": sources,
        "services": {"application": {"order": 10, "configs": [{
            "name": "credentials", "path": root + "/app.conf", "mode": "0600",
            "sensitive": False, "template": {"file": template.name}}], "processes": [{
                "name": "react", "type": "oneshot", "run_on": ["config.credentials"],
                "command": ["/bin/sh", "-c", hook, "richt-secret-reaction",
                            root + "/app.conf", root + "/fail", root + "/events"]}]}},
        "hosts": [{"name": "example", "tags": ["example"], "transport": connection}],
    }
    expected = ("environment=fictional-env-password-first\nyaml=fictional-default-password\n"
                "empty=[]\nmultiline:\nfictional env first line\nfictional env second line\n\n")
    try:
        record("env target is Linux", target("uname -s").stdout.strip() == "Linux")
        target("mkdir -m 700 -- " + shlex.quote(root))
        created = True
        write_document(inventory, document)
        # Neither template nor YAML source exists and all declared env variables are absent.
        run("inventory", "validate", "-i", inventory)
        run("inventory", "show", "-i", inventory)
        record("env inventory inspection requires no template, YAML file or variables")
        private_file(template, (fixtures / "env-app.conf.gotmpl").read_text())
        private_file(yaml_source, (fixtures / "default.yaml").read_text())

        missing = copy.deepcopy(document)
        missing["secret_sources"]["default"]["env"]["unrequested.key"] = names[4]
        write_document(inventory, missing)
        failed_plan = work / "missing.plan.json"
        result = run("diff", "-i", inventory, "--progress=plain", "-o", failed_plan, env=values, ok=False)
        record("missing unrequested variable invalidates the entire selected source",
               not failed_plan.exists() and "secret lookup failed" in result.stderr
               and names[4] not in result.stderr and "unrequested.key" not in result.stderr)
        write_document(inventory, document)
        saved = work / "env.plan.json"
        result = run("diff", "-i", inventory, "--progress=plain", "-o", saved, env=values)
        prepared = tasks(saved)
        change = next(task["file"] for task in prepared if "file" in task)
        record("env/YAML independent identical keys, verbatim empty/multiline values and unused source",
               change["content"] == expected and change["sensitive"] and len(prepared) == 2
               and "Content hidden: sensitive" in result.stdout)
        record("env prepared plan and lock are private",
               all(stat.S_IMODE(path.stat().st_mode) == 0o600 for path in (saved, Path(str(saved) + ".lock"))))
        for path in (inventory, template, yaml_source):
            path.unlink()
        target("touch -- " + shlex.quote(root + "/fail"))
        failed = run("apply", "-y", "--progress=plain", saved, ok=False)
        saved_errors = " ".join(task["progress"].get("error", "") for task in tasks(saved))
        record("env saved apply needs no env or sources and suppresses process failure output/progress",
               read_target("app.conf") == expected and private_target("app.conf")
               and "Started " in failed.stderr and "29" in failed.stderr
               and "29" in saved_errors and not any(marker in saved_errors for marker in markers))
        target("rm -- " + shlex.quote(root + "/fail"))
        resumed = run("apply", "-y", "--progress=plain", saved)
        record("env continuation skips completed config and retries only unfinished reaction",
               read_target("app.conf") == expected and read_target("events") == "reaction\n"
               and "previously completed" in resumed.stdout)

        private_file(template, (fixtures / "env-app.conf.gotmpl").read_text())
        private_file(yaml_source, (fixtures / "default.yaml").read_text())
        bound = copy.deepcopy(document)
        del bound["secret_sources"]
        write_document(inventory, bound)
        write_document(client, {"secret_sources": sources})
        noop = work / "noop-env.plan.json"
        run("diff", "-i", inventory, "--source-config", client, "-o", noop, env=values)
        record("explicit client env/YAML bindings produce a no-op diff", not tasks(noop))
        changed_env = dict(values, RICHT_EXAMPLE_PASSWORD="fictional-env-password-second")
        changed = work / "changed-env.plan.json"
        run("diff", "-i", inventory, "--source-config", client, "--progress=plain", "-o", changed, env=changed_env)
        record("fresh env snapshot schedules a real file change and reaction", len(tasks(changed)) == 2)
        for path in (inventory, template, yaml_source, client):
            path.unlink()
        run("apply", "-y", "--progress=plain", changed)
        expected_changed = expected.replace("fictional-env-password-first", "fictional-env-password-second")
        record("changed secret applies frozen bytes once with safe success output/progress",
               read_target("app.conf") == expected_changed and read_target("events") == "reaction\nreaction\n")
        # Reapplying the saved plan must not overwrite drift or rerun its reaction.
        target("printf 'external-change\\n' > " + shlex.quote(root + "/app.conf"))
        run("apply", "-y", "--progress=plain", changed)
        record("env successful continuation preserves drift and reaction count",
               read_target("app.conf") == "external-change\n" and read_target("events") == "reaction\nreaction\n")

        absent = copy.deepcopy(document)
        absent["tags"]["example"]["services"]["application"]["state"] = "absent"
        absent["services"]["application"]["configs"][0]["sensitive"] = True
        write_document(inventory, absent)
        removal = work / "absent-env.plan.json"
        run("diff", "-i", inventory, "--progress=plain", "-o", removal)
        record("absent service reads no missing env, YAML or template and schedules no reaction", len(tasks(removal)) == 1)
        inventory.unlink()
        run("apply", "-y", "--progress=plain", removal)
        record("absent env config is removed from frozen saved inputs",
               target("test ! -e " + shlex.quote(root + "/app.conf"), ok=False).returncode == 0
               and read_target("events") == "reaction\nreaction\n")
        report["status"] = "passed"
    except Exception:
        report["status"] = "failed"
        report["error"] = "check failed; captured contents withheld"
        raise
    finally:
        cleanup = target("rm -rf -- " + shlex.quote(root), ok=False) if created else None
        report["cleanup_succeeded"] = cleanup is None or cleanup.returncode == 0
        private_file(work / "acceptance.json", json.dumps(report, indent=2) + "\n")
        if args.work_root:
            print("Private env-suite evidence: " + str(work), flush=True)
        if temporary:
            temporary.cleanup()
        os.umask(previous_umask)
        check(report["cleanup_succeeded"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--env", action="store_true", help="Also run env/oneshot checks on Linux")
    parser.add_argument("--host", help="Disposable SSH host or user@host for the env suite")
    parser.add_argument("--sudo", action="store_true", help="Explicit sudo -n for env target operations")
    parser.add_argument("--work-root", type=Path, help="Retain private env-suite evidence under this directory")
    args = parser.parse_args()
    if (args.host or args.sudo or args.work_root) and not args.env:
        parser.error("--host, --sudo and --work-root require --env")
    if args.host and not re.fullmatch(r"(?:[A-Za-z0-9_][A-Za-z0-9_.-]*@)?[A-Za-z0-9][A-Za-z0-9_.:-]*", args.host):
        parser.error("--host must be a plain SSH host or user@host without options")
    binary = args.binary.resolve()
    verify_yaml(binary)
    if args.env:
        verify_env(binary, args)


if __name__ == "__main__":
    try:
        main()
    except (AssertionError, OSError, ValueError, KeyError, subprocess.SubprocessError):
        print("FAIL secret example; captured output and contents withheld", file=sys.stderr)
        sys.exit(1)
