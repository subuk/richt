#!/usr/bin/env python3
"""Verify oneshot processes on an authorized disposable Linux target.

Run locally as root, or use --host with a non-root SSH login and sudo -n.
Requires Python 3, GNU coreutils, systemd and a writable vm.swappiness sysctl.
Unique files/unit are removed and the original sysctl value is restored.
--package optionally verifies a binary trigger using an initially absent
published RPM from the target's existing DNF4 repositories; no RPM is built.
Controller inventories, executable plans and logs are private and retained when
--work-root is supplied.
"""

import argparse
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--host", help="SSH alias or user@host; login must have sudo -n")
    parser.add_argument("--work-root", type=Path, help="Retain private controller artifacts here")
    parser.add_argument("--package", help="Initially absent published RPM to install/remove for a binary trigger")
    args = parser.parse_args()
    if not args.host and os.geteuid() != 0:
        parser.error("local execution requires root on a disposable Linux host")
    binary = str(args.binary.resolve())
    work = args.work_root.resolve() if args.work_root else Path(tempfile.mkdtemp(prefix="richt-oneshot-"))
    work.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(work, 0o700)
    suffix = uuid.uuid4().hex
    root = "/var/tmp/richt-oneshot-" + suffix
    unit = "richt-oneshot-" + suffix + ".service"
    events = root + "/events"
    canary = "RICHT_ONESHOT_OUTPUT_" + suffix
    literal = "literal $HOME $(false); * | >"
    transport = {"type": "local"}
    if args.host:
        transport = {"type": "ssh", "host": args.host, "sudo": True}
        if "@" in args.host:
            transport["user"], transport["host"] = args.host.rsplit("@", 1)
    checks = []
    sequence = 0
    original_swappiness = None
    package_requested = False
    fixtures_created = False

    def target(script, ok=True, timeout=45):
        command = ["sh", "-c", script]
        if args.host:
            command = ["ssh", "-T", "-o", "BatchMode=yes", "--", args.host,
                       shlex.join(["sudo", "-n", "--"] + command)]
        result = subprocess.run(command, text=True, capture_output=True, timeout=timeout)
        if ok and result.returncode:
            raise AssertionError("target setup/check failed: " + result.stderr)
        return result

    def shell(script):
        return target(script).stdout

    def run(argv, ok=True, timeout=120):
        nonlocal sequence
        result = subprocess.run([binary] + [str(x) for x in argv], text=True,
                                capture_output=True, timeout=timeout)
        output = result.stdout + result.stderr
        sequence += 1
        (work / f"command-{sequence:03}.log").write_text(output)
        if canary in output:
            raise AssertionError("command stdout/stderr escaped through CLI diagnostics")
        if (result.returncode == 0) != ok:
            raise AssertionError(f"unexpected CLI exit {result.returncode}: {argv}\n{output}")
        return output

    def check(name, condition=True):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def config(name, content="prepared\n", mode="0640", sensitive=False):
        return {"name": name, "path": root + "/" + name, "owner": "root", "group": "root",
                "mode": mode, "sensitive": sensitive, "template": {"inline": content}}

    def process(label, refs=None, name=True, behavior="success", timeout="5s"):
        result = {"type": "oneshot", "command": ["/bin/sh", root + "/checker", label,
                  behavior, events, literal], "run_on": refs or ["config.main"], "timeout": timeout}
        if name:
            result["name"] = label
        return result

    def inventory(configs, processes, state="present", binaries=None, later=None):
        services = {"test": {"order": 10, "configs": configs, "processes": processes}}
        assignments = {"test": {"state": state}}
        if binaries:
            services["test"]["binaries"] = binaries
        if later:
            services["later"] = {"order": 20, "configs": [config("later")]}
            assignments["later"] = {}
        return {"tags": {"test": {"services": assignments}}, "services": services,
                "hosts": [{"name": "target", "tags": ["test"], "transport": transport}]}

    def write_inventory(name, data):
        inv = work / (name + ".yaml")
        inv.write_text(json.dumps(data, indent=2) + "\n")
        return inv

    def prepare(name, data):
        inv = write_inventory(name, data)
        saved = work / (name + ".json")
        output = run(["diff", "-i", inv, "-o", saved])
        return inv, saved, output

    def tasks(saved):
        return json.loads(saved.read_text())["hosts"][0]["tasks"]

    def oneshots(saved):
        return [task for task in tasks(saved) if task["operation"] == "oneshot"]

    def apply(saved, ok=True):
        output = run(["apply", "-y", saved], ok)
        if canary in saved.read_text():
            raise AssertionError("command stdout/stderr persisted in saved progress")
        return output

    def event_lines():
        return shell("if test -e " + shlex.quote(events) + "; then cat -- " + shlex.quote(events) + "; fi").splitlines()

    def reset_events():
        shell(": > " + shlex.quote(events))

    def absent(path):
        return target("test ! -e " + shlex.quote(path), ok=False).returncode == 0

    try:
        if args.host:
            identity = subprocess.check_output(["ssh", "-T", "-o", "BatchMode=yes", "--", args.host, "id -u"], text=True).strip()
            check("SSH login is non-root with explicit noninteractive sudo", identity != "0")
        facts = shell("id -u; uname -m; cat /etc/os-release; systemctl --version; "
                      "getenforce; /usr/sbin/sysctl -n vm.swappiness")
        (work / "platform.log").write_text(facts)
        check("target privileges are root", facts.splitlines()[0] == "0")
        original_swappiness = shell("/usr/sbin/sysctl -n vm.swappiness").strip()
        check("unique unit path and fixture directory are initially absent",
              absent(root) and absent("/etc/systemd/system/" + unit))
        shell("mkdir -m 0700 -- " + shlex.quote(root))
        fixtures_created = True
        checker = r'''set -eu
[ "$4" = 'literal $HOME $(false); * | >' ]
printf '%s' CANARY
printf '%s' CANARY >&2
printf '%s\n' "$1" >> "$3"
case "$2" in
  failure) exit 42;;
  guard) test -e GUARD;;
  timeout) exec /usr/bin/sleep 30;;
esac
'''.replace("CANARY", shlex.quote(canary)).replace("GUARD", shlex.quote(root + "/allow"))
        shell("printf %s " + shlex.quote(checker) + " > " + shlex.quote(root + "/checker"))

        base = inventory([config("main")], [process("created")])
        inv = write_inventory("create", base)
        run(["inventory", "validate", "-i", inv])
        run(["inventory", "show", "-i", inv])
        check("inspection does not execute commands", event_lines() == [] and absent(root + "/main"))
        inv, saved, output = prepare("create", base)
        check("diff does not execute commands or write config", event_lines() == [] and absent(root + "/main"))
        prepared = oneshots(saved)
        check("creation prepares one command with frozen argv, timeout and actual triggers",
              len(prepared) == 1 and prepared[0]["oneshot"]["command"] == base["services"]["test"]["processes"][0]["command"]
              and prepared[0]["oneshot"]["timeout"] == "5s" and prepared[0]["oneshot"]["triggers"] == ["config.main"])
        check("display reviews command executable, timeout and triggers",
              root + "/checker" in output and "5s" in output and "config.main" in output)
        apply(saved)
        check("create trigger runs direct argv once with literal shell characters and safe output", event_lines() == ["created"])
        apply(saved)
        check("successful named process is skipped on continuation", event_lines() == ["created"])
        _, noop, _ = prepare("unchanged", base)
        check("unchanged linked config prepares no task", tasks(noop) == [])

        changed = inventory([config("main", "updated\n")], [process("content")])
        _, saved, _ = prepare("content", changed)
        apply(saved)
        check("content change runs once", event_lines() == ["created", "content"])
        changed = inventory([config("main", "updated\n", "0600")], [process("attributes")])
        _, saved, _ = prepare("attributes", changed)
        check("attribute-only change records config trigger", oneshots(saved)[0]["oneshot"]["triggers"] == ["config.main"])
        apply(saved)
        check("attribute-only change runs once", event_lines()[-1] == "attributes" and shell("stat -c %a " + shlex.quote(root + "/main")).strip() == "600")
        changed = inventory([config("main", "merged\n"), config("other"), config("unchanged")],
                            [process("merged", ["config.main", "config.other", "config.unchanged"])])
        shell("printf 'prepared\\n' > " + shlex.quote(root + "/unchanged") + "; chmod 0640 " + shlex.quote(root + "/unchanged"))
        _, saved, _ = prepare("merged", changed)
        check("multiple refs merge once and omit unchanged refs from saved triggers",
              len(oneshots(saved)) == 1 and oneshots(saved)[0]["oneshot"]["triggers"] == ["config.main", "config.other"])
        before = len(event_lines())
        apply(saved)
        check("multiple changed refs execute one command", len(event_lines()) == before + 1 and event_lines()[-1] == "merged")
        changed["services"]["test"]["configs"][2]["template"]["inline"] = "unrelated\n"
        changed["services"]["test"]["processes"][0]["run_on"] = ["config.main", "config.other"]
        _, saved, _ = prepare("unrelated", changed)
        apply(saved)
        check("unrelated config changes do not execute command", len(event_lines()) == before + 1 and not oneshots(saved))

        frozen = inventory([config("frozen")], [process("frozen", ["config.frozen"], name=False)])
        inv, saved, _ = prepare("frozen", frozen)
        inv.unlink()
        before = len(event_lines())
        apply(saved)
        apply(saved)
        check("frozen command survives removed inventory and successful unnamed task is skipped",
              len(event_lines()) == before + 1 and event_lines()[-1] == "frozen")

        # The owned unit is just an ordering probe. No package fixture is needed.
        reset_events()
        systemd = {"name": "middle", "type": "systemd", "unit": unit, "unit_file": {
            "mode": "0644", "template": {"inline": "[Service]\nType=oneshot\nRemainAfterExit=yes\n"
                "ExecStart=/bin/sh -c 'echo systemd >> " + events + "'\n"}}}
        mixed = inventory([config("mixed")], [process("first", ["config.mixed"]), systemd,
                                               process("last", ["config.mixed"], name=False)])
        _, saved, _ = prepare("mixed", mixed)
        apply(saved)
        check("oneshot and systemd processes execute in declaration order", event_lines() == ["first", "systemd", "last"])
        _, saved, _ = prepare("mixed-absent", inventory([config("mixed")], mixed["services"]["test"]["processes"], "absent"))
        check("absence prepares no oneshot task", not oneshots(saved))
        apply(saved)
        check("absence removes owned resources without running commands", event_lines() == ["first", "systemd", "last"] and absent(root + "/mixed"))

        reset_events()
        failed = inventory([config("resume", "sensitive prepared\n", sensitive=True)],
                           [process("named-success", ["config.resume"]),
                            process("unnamed-success", ["config.resume"], name=False),
                            process("guard", ["config.resume"], name=False, behavior="guard"),
                            process("blocked", ["config.resume"])], later=True)
        inv, saved, _ = prepare("continuation", failed)
        original_ids = [task["id"] for task in tasks(saved)]
        apply(saved, False)
        check("command failure follows successful config write and blocks later host tasks",
              shell("cat " + shlex.quote(root + "/resume")) == "sensitive prepared\n" and absent(root + "/later")
              and [task["progress"]["status"] for task in tasks(saved)] == ["success", "success", "success", "error", "pending", "pending"])
        _, fresh, _ = prepare("fresh-after-failure", failed)
        check("fresh diff loses command trigger after successful config write", not oneshots(fresh))
        shell("printf 'manual edit\\n' > " + shlex.quote(root + "/resume") + "; touch " + shlex.quote(root + "/allow"))
        inv.unlink()
        apply(saved)
        check("saved continuation skips successful named and unnamed commands, retries failure, and unblocks later tasks",
              event_lines() == ["named-success", "unnamed-success", "guard", "guard", "blocked"]
              and shell("cat " + shlex.quote(root + "/resume")) == "manual edit\n" and not absent(root + "/later")
              and [task["id"] for task in tasks(saved)] == original_ids
              and all(task["progress"]["status"] == "success" for task in tasks(saved)))

        for behavior in ["failure", "missing", "timeout"]:
            p = process(behavior, ["config." + behavior], behavior=behavior,
                        timeout="200ms" if behavior == "timeout" else "5s")
            if behavior == "missing":
                p["command"] = [root + "/missing-executable"]
            _, saved, _ = prepare(behavior, inventory([config(behavior, sensitive=True)], [p]))
            start = time.monotonic()
            apply(saved, False)
            check(behavior + " fails safely after config write and persists unfinished command",
                  not absent(root + "/" + behavior) and tasks(saved)[-1]["progress"]["status"] == "error")
            if behavior == "timeout":
                check("command timeout is bounded", time.monotonic() - start < 10)

        if args.package:
            check("published package is initially absent",
                  target("rpm -q -- " + shlex.quote(args.package), ok=False).returncode != 0)
            package_requested = True
            binaries = [{"name": "package", "type": "rpm", "package": args.package}]
            binary_case = inventory([], [process("binary", ["binary.package"])], binaries=binaries)
            _, saved, _ = prepare("binary-trigger", binary_case)
            check("binary-only trigger needs no daemon reload",
                  len(oneshots(saved)) == 1 and oneshots(saved)[0]["oneshot"]["triggers"] == ["binary.package"]
                  and not any(task["operation"] == "daemon-reload" for task in tasks(saved)))
            before = len(event_lines())
            apply(saved)
            check("published package change executes oneshot once", len(event_lines()) == before + 1 and event_lines()[-1] == "binary")
            _, saved, _ = prepare("binary-unchanged", binary_case)
            check("installed unversioned package does not trigger command", not tasks(saved))
            _, saved, _ = prepare("binary-absent", inventory([], binary_case["services"]["test"]["processes"], "absent", binaries))
            apply(saved)
            check("package removal does not execute oneshot", len(event_lines()) == before + 1)
            package_requested = False

        # Change a safe tuning value on this authorized disposable host, then
        # restore it even when an assertion or apply later fails.
        sysctl_path = "/etc/sysctl.d/90-richt-oneshot-" + suffix + ".conf"
        changed_swappiness = "10" if original_swappiness != "10" else "20"
        sysctl_config = config("sysctl", "vm.swappiness = " + changed_swappiness + "\n")
        sysctl_config["path"] = sysctl_path
        p = {"name": "sysctl", "type": "oneshot", "command": ["/usr/sbin/sysctl", "-p", sysctl_path],
             "run_on": ["config.sysctl"]}
        _, saved, _ = prepare("sysctl", inventory([sysctl_config], [p]))
        apply(saved)
        check("native oneshot applies a real sysctl config without a systemd unit",
              shell("/usr/sbin/sysctl -n vm.swappiness").strip() == changed_swappiness)
        _, saved, _ = prepare("sysctl-absent", inventory([sysctl_config], [p], "absent"))
        apply(saved)
        check("sysctl config absence schedules no command", not oneshots(saved) and absent(sysctl_path))
        report = {"status": "passed", "count": len(checks), "checks": checks, "transport": transport,
                  "published_package": args.package, "binary_trigger_verified": bool(args.package)}
        (work / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
    finally:
        cleanup_errors = []
        if original_swappiness is not None:
            result = target("/usr/sbin/sysctl -w " + shlex.quote("vm.swappiness=" + original_swappiness), ok=False)
            if result.returncode:
                cleanup_errors.append("could not restore vm.swappiness")
        if fixtures_created:
            result = target("systemctl stop " + shlex.quote(unit) + " 2>/dev/null || :; "
                       "rm -f -- " + shlex.quote("/etc/systemd/system/" + unit) + " "
                       + shlex.quote("/etc/sysctl.d/90-richt-oneshot-" + suffix + ".conf") + "; "
                           "systemctl daemon-reload; rm -rf -- " + shlex.quote(root), ok=False)
            if result.returncode:
                cleanup_errors.append("fixture cleanup failed")
        if package_requested:
            result = target("dnf remove -y -- " + shlex.quote(args.package), ok=False, timeout=120)
            if result.returncode:
                cleanup_errors.append("published package cleanup failed")
        if args.work_root:
            print("Artifacts: " + str(work))
        else:
            shutil.rmtree(work)
        if cleanup_errors:
            raise AssertionError("; ".join(cleanup_errors))


if __name__ == "__main__":
    main()
