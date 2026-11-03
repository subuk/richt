#!/usr/bin/env python3
"""Isolated local-account acceptance on an authorized Linux host, local or SSH/sudo.

Requires shadow-utils and GNU getent. Unique users, groups, and files are removed;
private inventories, plans, logs and acceptance.json remain in --output.
"""

import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--ssh-host")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    if not args.ssh_host and os.geteuid() != 0:
        parser.error("local acceptance requires root")
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True, mode=0o700)
    output.chmod(0o700)
    binary = str(Path(args.binary).resolve())
    name = "rct_" + uuid.uuid4().hex[:12]
    extra = name + "_extra"
    root = "/var/tmp/" + name
    checks = []
    sequence = 0
    transport = {"type": "local"}
    if args.ssh_host:
        user, sep, host = args.ssh_host.rpartition("@")
        transport = {"type": "ssh", "host": host if sep else args.ssh_host, "sudo": True}
        if sep:
            transport["user"] = user

    def command(argv, ok=True):
        nonlocal sequence
        result = subprocess.run(argv, capture_output=True, text=True, timeout=120, umask=0o077)
        sequence += 1
        (output / f"{sequence:03d}.log").write_text(shlex.join(argv) + "\n" + result.stdout + result.stderr)
        if ok != (result.returncode == 0):
            raise AssertionError(f"exit {result.returncode}: {shlex.join(argv)}\n{result.stdout}{result.stderr}")
        return result

    def target(script, ok=True):
        argv = ["sh", "-c", "set -eu\n" + script]
        if args.ssh_host:
            argv = ["ssh", "-T", "-o", "BatchMode=yes", args.ssh_host,
                    shlex.join(["sudo", "-n", "--"] + argv)]
        return command(argv, ok)

    def check(label, condition=True):
        if not condition:
            raise AssertionError(label)
        checks.append(label)
        print("PASS " + label, flush=True)

    def inventory(label, configs, state="present"):
        data = {"tags": {"test": {"services": {"test": {"state": state}}}},
                "services": {"test": {"order": 10, "configs": configs}},
                "hosts": [{"name": "target", "tags": ["test"], "transport": transport}]}
        if any(c.get("name") == "account" for c in configs):
            data["services"]["test"]["processes"] = [{
                "type": "oneshot", "run_on": ["config.account"],
                "command": ["/bin/sh", "-c", f"printf 'account\\n' >> {root}/events"]}]
        source = output / (label + ".yaml")
        source.write_text(json.dumps(data, indent=2) + "\n")
        return source

    def prepare(label, configs, state="present", ok=True):
        source = inventory(label, configs, state)
        saved = output / (label + ".json")
        command([binary, "diff", "-i", str(source), "-o", str(saved)], ok)
        return saved

    def apply(saved, ok=True):
        command([binary, "apply", "-y", str(saved)], ok)

    def tasks(saved):
        return json.loads(saved.read_text())["hosts"][0]["tasks"]

    def replay(saved, label):
        data = json.loads(saved.read_text())
        for task in data["hosts"][0]["tasks"]:
            if task["operation"] == "account":
                task["progress"] = {"status": "running"}
        pending = output / (label + ".json")
        pending.write_text(json.dumps(data) + "\n")
        pending.chmod(0o600)
        return pending

    group = {"type": "group", "group": name, "system": True}
    supplementary = {"type": "group", "group": extra, "system": True}
    user = {"type": "user", "name": "account", "user": name, "group": name,
            "system": True, "home": root + "/home", "shell": "/usr/sbin/nologin",
            "comment": "Richt acceptance", "supplementary_groups": [extra]}
    directory = {"type": "directory", "path": root + "/data", "owner": name, "group": name, "mode": "0750"}
    template = {"path": root + "/data/config", "owner": name, "group": name,
                "mode": "0640", "template": {"inline": "prepared account content\n"}}
    plain = {"type": "file", "path": root + "/data/empty", "owner": name, "group": name}
    configs = [group, supplementary, user, directory, template, plain]
    fixtures_owned = False
    try:
        target(f"if getent passwd {name}; then exit 1; fi; if getent group {name}; then exit 1; fi; if getent group {extra}; then exit 1; fi; test ! -e {root}")
        policy_before = target("sha256sum /etc/login.defs").stdout
        target(f"mkdir -m 0700 {root}")
        fixtures_owned = True
        source = inventory("inspect", configs)
        command([binary, "inventory", "validate", "-i", str(source)])
        command([binary, "inventory", "show", "-i", str(source)])
        target(f"if getent passwd {name}; then exit 1; fi; if getent group {name}; then exit 1; fi")
        check("inventory inspection is side-effect free")
        prepare("wrong-order", [user, group, supplementary], ok=False)
        check("a new user cannot precede required groups")
        saved = prepare("create", configs)
        target(f"if getent passwd {name}; then exit 1; fi; if getent group {name}; then exit 1; fi; test ! -e {root}/data")
        check("preparation accepts planned owners without creating resources")
        check("account tasks precede owned filesystem resources", [t["operation"] for t in tasks(saved)][:3] == ["account"] * 3)
        (output / "create.yaml").unlink()
        apply(saved)
        target(f"test \"$(stat -c %U:%G {root}/data/config)\" = {name}:{name}; "
               f"test \"$(stat -c %U:%G {root}/data/empty)\" = {name}:{name}; "
               f"test \"$(stat -c %a {root}/data)\" = 750; test ! -e {root}/home; "
               f"getent -s files passwd {name}; getent -s files group {extra}")
        check("saved apply creates accounts and resolves new file owners without inventory")
        check("home path does not create a home directory")
        check("second diff is empty", not tasks(prepare("noop", configs)))
        apply(replay(saved, "interrupted-create"))
        check("interrupted account creations accept their matching results")
        check("account creation triggers one reaction and continuation skips its success",
              target(f"wc -l < {root}/events").stdout.strip() == "1")
        uid = int(target(f"id -u {name}").stdout.strip())
        prepare("uid-change", [dict(user, uid=uid + 1)], ok=False)
        prepare("uid-conflict", [dict(user, user=name + "_other", uid=uid)], ok=False)
        check("UID mutation and another account's UID are rejected")
        omitted = dict(user)
        omitted.pop("supplementary_groups")
        target(f"usermod -c 'outside value' {name}")
        omitted.pop("comment")
        check("omitted comment and memberships preserve current values", not tasks(prepare("omitted", [omitted])))
        updated = dict(user, comment="updated value", supplementary_groups=[])
        changed = prepare("update", [updated])
        apply(changed)
        target(f"test \"$(getent -s files group {extra} | cut -d: -f4)\" = ''; "
               f"test \"$(getent -s files passwd {name} | cut -d: -f5)\" = 'updated value'")
        apply(replay(changed, "interrupted-update"))
        check("explicit empty memberships clear local groups and updates can continue")
        check("changed account triggers one reaction; noop and retries do not repeat it",
              target(f"wc -l < {root}/events").stdout.strip() == "2")
        target(f"mkdir {root}/home; printf retained > {root}/home/marker")
        deletion = prepare("user-delete", [updated], "absent")
        apply(deletion)
        target(f"if getent -s files passwd {name}; then exit 1; fi; getent -s files group {name}; test -f {root}/home/marker")
        apply(replay(deletion, "interrupted-delete"))
        check("user deletion preserves same-name group and home data and can continue")
        # Recreate the user and prepare full reverse removal.
        apply(prepare("recreate", [updated]))
        absent = prepare("absent", configs, "absent")
        account_tasks = [t["account"] for t in tasks(absent) if t["operation"] == "account"]
        check("absent order deletes users before groups", [t["kind"] for t in account_tasks] == ["user", "group", "group"])
        apply(absent)
        target(f"if getent passwd {name}; then exit 1; fi; if getent group {name}; then exit 1; fi; if getent group {extra}; then exit 1; fi; test ! -e {root}/data; test -f {root}/home/marker")
        check("full absence removes only declared accounts and filesystem resources")
        check("repeated absent diff is empty after owners disappear", not tasks(prepare("absent-noop", configs, "absent")))
        explicit_id = uid + 200000
        target(f"if getent passwd {explicit_id}; then exit 1; fi; "
               f"if getent group {explicit_id}; then exit 1; fi; "
               f"if getent passwd {explicit_id + 1}; then exit 1; fi")
        fixed_group = dict(group, gid=explicit_id)
        fixed_user = dict(updated, uid=explicit_id)
        apply(prepare("explicit-ids", [fixed_group, fixed_user]))
        check("explicit UID and GID are applied", target(f"id -u {name}; id -g {name}").stdout.split() == [str(explicit_id)] * 2)
        drift_plan = prepare("identity-drift", [dict(fixed_user, comment="must not apply")])
        target(f"usermod --uid {explicit_id + 1} {name}")
        apply(drift_plan, ok=False)
        target(f"test \"$(getent -s files passwd {name} | cut -d: -f5)\" = 'updated value'")
        check("saved update rejects a changed numeric identity before changing attributes")
        target(f"usermod --uid {explicit_id} {name}")
        apply(drift_plan)
        check("explicit continuation succeeds after the expected identity is restored")
        apply(prepare("explicit-absent", [fixed_group, fixed_user], "absent"))
        check("user deletion leaves host login.defs unchanged", target("sha256sum /etc/login.defs").stdout == policy_before)
        report = {"checks": checks, "count": len(checks), "transport": "ssh-sudo" if args.ssh_host else "local"}
        (output / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
    finally:
        if fixtures_owned:
            target(f"if getent -s files passwd {name} >/dev/null; then userdel {name}; fi; "
                   f"if getent -s files group {name} >/dev/null; then groupdel {name}; fi; "
                   f"if getent -s files group {extra} >/dev/null; then groupdel {extra}; fi; "
                   f"rm -rf --one-file-system -- {root}")


if __name__ == "__main__":
    main()
