#!/usr/bin/env python3
"""Isolated plain-file/directory acceptance, locally or over SSH with sudo."""

import argparse
import json
from pathlib import Path
import shlex
import subprocess
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--ssh-host", help="Authorized disposable [user@]host; sudo -n")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    output.chmod(0o700)
    binary = str(Path(args.binary).resolve())
    root = "/var/tmp/richt-typed-paths-" + uuid.uuid4().hex[:12]
    checks = []
    sequence = 0

    def command(argv, success=True):
        nonlocal sequence
        result = subprocess.run(argv, capture_output=True, text=True, umask=0o077)
        sequence += 1
        (output / f"{sequence:03d}.log").write_text(
            shlex.join(argv) + "\n" + result.stdout + result.stderr)
        if success != (result.returncode == 0):
            raise AssertionError(f"command exit {result.returncode}: {shlex.join(argv)}\n{result.stdout}{result.stderr}")
        return result

    def target(script, success=True):
        script = "set -eu\n" + script
        if args.ssh_host:
            return command(["ssh", "-o", "BatchMode=yes", args.ssh_host,
                            "sudo -n sh -c " + shlex.quote(script)], success)
        return command(["sh", "-c", script], success)

    def check(name, condition):
        if not condition:
            raise AssertionError(name)
        checks.append(name)

    def inventory(name, configs, state="present"):
        transport = {"type": "local"}
        if args.ssh_host:
            user, sep, host = args.ssh_host.rpartition("@")
            transport = {"type": "ssh", "host": host if sep else args.ssh_host, "sudo": True}
            if sep:
                transport["user"] = user
        source = output / (name + ".yaml")
        source.write_text("tags:\n  test:\n    services:\n      test: {state: " + state + "}\n"
                          "services:\n  test:\n    order: 10\n    configs:\n" +
                          "".join("      - " + json.dumps(config) + "\n" for config in configs) +
                          "hosts:\n  - name: target\n    tags: [test]\n    transport: " + json.dumps(transport) + "\n")
        return source

    def prepare(name, configs, state="present", success=True):
        source = inventory(name, configs, state)
        saved = output / (name + ".json")
        command([binary, "diff", "-i", str(source), "-o", str(saved)], success)
        return saved

    def apply(saved, success=True):
        return command([binary, "apply", "-y", str(saved)], success)

    try:
        target("mkdir -m 0700 " + root)
        directory = root + "/data"
        marker = directory + "/marker"
        configs = [{"type": "directory", "path": directory}, {"name": "marker", "type": "file", "path": marker}]
        source = inventory("inspect", configs)
        command([binary, "inventory", "show", "-i", str(source)])
        target("test ! -e " + directory)
        check("inventory inspection has no target effects", True)
        saved = prepare("create", configs)
        target("test ! -e " + directory)
        check("diff does not create directories or files", True)
        apply(saved)
        target("test -f " + marker + "; test ! -s " + marker + "; test \"$(stat -c %a " + marker + ")\" = 644; test \"$(stat -c %a " + directory + ")\" = 755")
        check("empty file and directory default to 0644/0755 under controller umask077", True)
        target("printf 'unmanaged contents\\n' > " + marker + "; printf child > " + directory + "/undeclared; chmod 0600 " + directory + "/undeclared")
        saved = prepare("unchanged", configs)
        check("unmanaged contents and children produce an empty diff", not json.loads(saved.read_text())["hosts"][0]["tasks"])
        attrs = [{"type": "directory", "path": directory, "mode": "0700", "owner": "root", "group": "root"},
                 {"type": "file", "path": marker, "mode": "0640", "owner": "root", "group": "root"}]
        saved = prepare("attributes", attrs)
        apply(saved)
        target("test \"$(cat " + marker + ")\" = 'unmanaged contents'; test \"$(stat -c %a " + marker + ")\" = 640; test \"$(stat -c %a " + directory + ")\" = 700; test \"$(stat -c %a " + directory + "/undeclared)\" = 600; test \"$(stat -c %U:%G " + marker + ")\" = root:root")
        check("attributes preserve contents and do not recurse into children", True)
        appeared = root + "/appeared"
        saved = prepare("appeared", [{"type": "file", "path": appeared}])
        target("printf 'appeared after diff' > " + appeared + "; chmod 0400 " + appeared)
        apply(saved)
        target("test \"$(cat " + appeared + ")\" = 'appeared after diff'; test \"$(stat -c %a " + appeared + ")\" = 400")
        apply(saved)
        target("test \"$(cat " + appeared + ")\" = 'appeared after diff'; test \"$(stat -c %a " + appeared + ")\" = 400")
        check("file appearing after diff is preserved and successful continuation skips it", True)
        saved = prepare("replacement", [{"type": "file", "path": appeared}], "absent")
        target("mv " + appeared + " " + appeared + ".original; printf replacement > " + appeared)
        apply(saved, False)
        target("test \"$(cat " + appeared + ")\" = replacement")
        check("unfinished deletion refuses a replacement file identity", True)
        link = root + "/named-link"
        target("ln -s " + directory + " " + link)
        saved = prepare("symlink", [{"type": "directory", "path": link}], "absent")
        apply(saved)
        target("test ! -L " + link + "; test -f " + marker)
        check("absent named directory symlink is unlinked without following", True)
        saved = prepare("incompatible-file", [{"type": "file", "path": directory}], success=False)
        saved = prepare("incompatible-directory", [{"type": "directory", "path": marker}], success=False)
        check("incompatible existing path types fail during preparation", True)
        mount_root = root + "/mounted-tree"
        bind_source = root + "/bind-source"
        bind_target = mount_root + "/nested"
        target("mkdir -p " + bind_source + " " + bind_target + "; printf retained > " + bind_source + "/data; mount --bind " + bind_source + " " + bind_target)
        saved = prepare("mounted-delete", [{"type": "directory", "path": mount_root}], "absent")
        apply(saved, False)
        target("mountpoint -q " + bind_target + "; test \"$(cat " + bind_source + "/data)\" = retained")
        check("recursive directory deletion refuses an active same-device bind mount", True)
        target("umount " + bind_target)
        apply(saved)
        target("test ! -e " + mount_root + "; test \"$(cat " + bind_source + "/data)\" = retained")
        check("explicit continuation after unmount deletes only the declared tree", True)
        saved = prepare("recursive-delete", configs, "absent")
        tasks = json.loads(saved.read_text())["hosts"][0]["tasks"]
        check("absent config tasks retain reverse declaration order", [task["path"]["type"] for task in tasks] == ["file", "directory"])
        apply(saved)
        target("test ! -e " + directory)
        check("absent directory recursively removes undeclared data", True)
        print(json.dumps({"checks": checks, "count": len(checks), "transport": "ssh-sudo" if args.ssh_host else "local", "fixture": root}, indent=2))
        (output / "acceptance.json").write_text(json.dumps({"checks": checks, "count": len(checks)}, indent=2) + "\n")
    finally:
        target("if mountpoint -q " + root + "/mounted-tree/nested; then umount " + root + "/mounted-tree/nested; fi; rm -rf --one-file-system -- " + root)


if __name__ == "__main__":
    main()
