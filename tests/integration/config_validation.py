#!/usr/bin/env python3
"""Validate private candidates on an authorized disposable Linux target.

Run locally as root, or supply a non-root SSH destination with sudo -n access.
Only unique temporary files are managed; the active SSH configuration is copied
for syntax checks and is never changed. GNU coreutils and openssh-server with
existing host keys are required on the target.
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
    parser.add_argument("--work-root", type=Path, help="Retain controller artifacts in this directory")
    args = parser.parse_args()
    if not args.host and os.geteuid() != 0:
        parser.error("local execution requires root on a disposable Linux host")
    binary = str(args.binary.resolve())
    work = args.work_root.resolve() if args.work_root else Path(tempfile.mkdtemp(prefix="richt-validation-"))
    work.mkdir(parents=True, exist_ok=True)
    root = "/var/tmp/richt-validation-" + uuid.uuid4().hex
    marker = "RICHT_VALIDATOR_CANARY_" + uuid.uuid4().hex
    checks = []
    sequence = 0
    transport = {"type": "local"}
    if args.host:
        host = args.host
        transport = {"type": "ssh", "host": host, "sudo": True}
        if "@" in host:
            transport["user"], transport["host"] = host.rsplit("@", 1)

    def target(script, ok=True):
        command = ["sh", "-c", script]
        if args.host:
            command = ["ssh", "-T", "-o", "BatchMode=yes", "--", args.host,
                       shlex.join(["sudo", "-n", "--"] + command)]
        result = subprocess.run(command, text=True, capture_output=True, timeout=30)
        if ok and result.returncode:
            raise AssertionError("target setup/check failed: " + result.stderr)
        return result.stdout

    def run(argv, ok=True):
        nonlocal sequence
        result = subprocess.run([binary] + [str(x) for x in argv], text=True,
                                capture_output=True, timeout=60)
        output = result.stdout + result.stderr
        if marker in output:
            raise AssertionError("validator output escaped through CLI diagnostics")
        sequence += 1
        (work / f"command-{sequence:03}.log").write_text(output)
        if (result.returncode == 0) != ok:
            raise AssertionError(f"unexpected CLI exit {result.returncode}: {argv}\n{output}")
        return output

    def check(name, condition=True):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def config(path, behavior="success", content="prepared\n", validator=None, mode="0640"):
        return {"path": path, "owner": "root", "group": "root", "mode": mode,
                "template": {"inline": content}, "validate": {
                    "args": validator or ["/bin/sh", root + "/checker", "{candidate}", behavior,
                                          root + "/candidates", "literal $HOME $(false); *"],
                    "timeout": "200ms" if behavior == "timeout" else "5s"}}

    def inventory(configs, state="present"):
        return {"tags": {"test": {"services": {"test": {"state": state}}}},
                "services": {"test": {"order": 10, "configs": configs}},
                "hosts": [{"name": "target", "tags": ["test"], "transport": transport}]}

    def prepare(name, configs, state="present"):
        inv, saved = work / (name + ".yaml"), work / (name + ".json")
        inv.write_text(json.dumps(inventory(configs, state)))
        run(["diff", "-i", inv, "-o", saved])
        return inv, saved

    def apply(saved, ok=True):
        return run(["apply", "-y", saved], ok)

    def snapshot(path):
        return target("cat -- " + shlex.quote(path) + "; stat -c '%a %u %g %i %C' -- " + shlex.quote(path))

    def candidates():
        return target("find /tmp -maxdepth 1 -type d -name 'richt-validate-*' -printf '%f\\n' | sort")

    def cleaned():
        target("if [ -f " + shlex.quote(root + "/candidates") + " ]; then while IFS= read -r candidate; do "
               "test ! -e \"${candidate%/*}\" || exit 1; done < " + shlex.quote(root + "/candidates") + "; fi")

    try:
        if args.host:
            identity = subprocess.check_output(["ssh", "-T", "-o", "BatchMode=yes", "--", args.host, "id -u"], text=True).strip()
            check("SSH login is non-root with explicit noninteractive sudo", identity != "0")
        target("mkdir -m 0700 -- " + shlex.quote(root))
        checker = r'''set -eu
candidate=$1
behavior=$2
printf '%s\n' "$candidate" >> "$3"
[ "$(stat -c %a -- "$candidate")" = 600 ]
[ "$(stat -c %a -- "${candidate%/*}")" = 700 ]
[ "$PWD" = / ]
[ "$4" = 'literal $HOME $(false); *' ]
printf '%s' CANARY
printf '%s' CANARY >&2
case "$behavior" in
  failure) exit 42;;
  timeout) sleep 30;;
  guard) test -e GUARD;;
esac
'''.replace("CANARY", shlex.quote(marker)).replace("GUARD", shlex.quote(root + "/allow"))
        target("printf %s " + shlex.quote(checker) + " > " + shlex.quote(root + "/checker"))
        path = root + "/managed"
        target("printf 'old\\n' > " + shlex.quote(path) + "; chmod 0640 " + shlex.quote(path))
        before = snapshot(path)
        _, saved = prepare("success", [dict(config(path), name="main")])
        check("diff leaves destination unchanged", snapshot(path) == before)
        apply(saved)
        check("direct argv succeeds with candidate mode0600, directory0700 and cwd root", snapshot(path).startswith("prepared\n640 0 0 "))
        cleaned()
        check("success cleans private candidate")

        for behavior in ["failure", "missing", "timeout"]:
            validator = [root + "/missing-checker", "{candidate}"] if behavior == "missing" else None
            before, dirs = snapshot(path), candidates()
            _, saved = prepare(behavior, [dict(config(path, behavior, "rejected\n", validator), name="main")])
            start = time.monotonic()
            apply(saved, False)
            check(behavior + " leaves destination bytes, owner, mode and inode unchanged", snapshot(path) == before)
            cleaned()
            check(behavior + " cleans candidate and discards stdout/stderr", candidates() == dirs)
            if behavior == "timeout":
                check("target timeout is bounded", time.monotonic() - start < 10)

        source = work / "frozen.tmpl"
        source.write_text("frozen from deleted source\n")
        c = config(path)
        c["template"] = {"file": str(source)}
        inv, saved = prepare("frozen", [dict(c, name="main")])
        source.unlink()
        inv.unlink()
        apply(saved)
        check("saved validator and bytes survive deleted inventory and template", snapshot(path).startswith("frozen from deleted source\n"))

        missing = [root + "/missing-checker", "{candidate}"]
        inv, _ = prepare("noop", [dict(config(path, content="frozen from deleted source\n", validator=missing), name="main")])
        check("matching file skips unavailable checker", "No changes" in run(["diff", "-i", inv]))
        _, saved = prepare("attributes", [dict(config(path, content="frozen from deleted source\n", validator=missing, mode="0600"), name="main")])
        apply(saved)
        check("attribute-only change skips checker", snapshot(path).splitlines()[-1].startswith("600 0 0 "))
        _, saved = prepare("remove", [dict(config(path, validator=missing), name="main")], "absent")
        apply(saved)
        target("test ! -e " + shlex.quote(path))
        check("removal skips checker")

        first, second, third, later = [root + "/" + name for name in ["first", "second", "third", "later"]]
        ordered = [dict(config(first), name="z-last"), config(second, "guard"),
                   config(third, validator=["/bin/sh", "-c", 'test -f "$1"', "richt-order", second, "{candidate}"]),
                   dict(config(later, validator=["/bin/sh", "-c", 'test -f "$1"', "richt-order", third, "{candidate}"]), name="a-first")]
        inv, saved = prepare("continuation", ordered)
        prepared = json.loads(saved.read_text())["hosts"][0]["tasks"]
        ids = [task["id"] for task in prepared]
        check("named and unnamed configs retain declaration order and distinct task IDs",
              [task["file"]["path"] for task in prepared] == [first, second, third, later] and len(set(ids)) == 4)
        apply(saved, False)
        failed = json.loads(saved.read_text())["hosts"][0]["tasks"]
        check("validation failure persists success and blocks later named and unnamed tasks",
              [task["progress"]["status"] for task in failed] == ["success", "error", "pending", "pending"] and
              [task["id"] for task in failed] == ids)
        target("printf 'manual\\n' > " + shlex.quote(first) + "; touch " + shlex.quote(root + "/allow"))
        inv.unlink()
        apply(saved)
        completed = json.loads(saved.read_text())["hosts"][0]["tasks"]
        check("continuation preserves ordered IDs, skips success and retries unfinished validation",
              snapshot(first).startswith("manual\n") and all(snapshot(path).startswith("prepared\n") for path in [second, third, later]) and
              [task["id"] for task in completed] == ids and all(task["progress"]["status"] == "success" for task in completed))
        cleaned()

        # Read the active config only; both validated writes use an isolated path.
        target("/usr/sbin/sshd -t -f /etc/ssh/sshd_config")
        ssh_config = target("cat /etc/ssh/sshd_config")
        isolated = root + "/sshd_config"
        actual = ["/usr/sbin/sshd", "-t", "-f", "{candidate}"]
        _, saved = prepare("real-sshd", [dict(config(isolated, content=ssh_config, validator=actual), name="main")])
        apply(saved)
        before = snapshot(isolated)
        _, saved = prepare("bad-sshd", [dict(config(isolated, content=ssh_config + "\nInvalidRichtTestDirective yes\n", validator=actual), name="main")])
        apply(saved, False)
        check("real sshd accepts copied valid config and rejects invalid candidate before write", snapshot(isolated) == before)
        cleaned()
        report = {"count": len(checks), "checks": checks, "transport": transport, "status": "passed"}
        (work / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
    finally:
        target("rm -rf -- " + shlex.quote(root), ok=False)
        if args.work_root:
            print("Artifacts: " + str(work))
        else:
            shutil.rmtree(work)


if __name__ == "__main__":
    main()
