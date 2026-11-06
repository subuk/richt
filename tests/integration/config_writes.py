#!/usr/bin/env python3
"""KRG-103 file-write acceptance on an authorized disposable Linux target.

The controller needs Python 3 and a native Richt binary. The target needs root,
Python 3.8+, GNU coreutils, getenforce/chcon, getfacl/setfacl, an xattr/ACL-capable
filesystem, and SELinux Enforcing. --host uses existing SSH trust/credentials and
explicit sudo -n, through Richt's SSH transport; no target Richt is installed.
Only a unique /var/tmp fixture is changed. It is always removed; private plans,
command logs and acceptance.json are retained in a unique --work-root child.
No packages, accounts, services, or host policy are changed.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import tempfile
import uuid


SNAPSHOT = r'''
try:
    info = os.lstat(p["path"])
except FileNotFoundError:
    print(json.dumps({"kind": "absent"}))
else:
    import stat
    kind = "symlink" if stat.S_ISLNK(info.st_mode) else "regular" if stat.S_ISREG(info.st_mode) else "other"
    result = {"kind": kind, "inode": info.st_ino, "device": info.st_dev,
              "uid": info.st_uid, "gid": info.st_gid, "mode": stat.S_IMODE(info.st_mode),
              "xattrs": {key: base64.b64encode(os.getxattr(p["path"], key, follow_symlinks=False)).decode()
                         for key in os.listxattr(p["path"], follow_symlinks=False)}}
    if kind == "regular":
        data = open(p["path"], "rb").read()
        result.update(sha256=hashlib.sha256(data).hexdigest(), data=base64.b64encode(data).decode())
    if kind == "symlink":
        result["link"] = os.readlink(p["path"])
    print(json.dumps(result))
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path, help="Native controller Richt binary")
    parser.add_argument("--host", help="Authorized disposable SSH alias or user@host, with sudo -n")
    parser.add_argument("--work-root", type=Path, default=Path("/tmp"),
                        help="Existing parent for unique private retained controller evidence")
    args = parser.parse_args()
    if not args.host and (sys.platform != "linux" or os.geteuid() != 0):
        parser.error("local execution requires root on a disposable Linux target")
    if not args.work_root.is_dir():
        parser.error("--work-root must be an existing directory")
    os.umask(0o077)
    work = Path(tempfile.mkdtemp(prefix="richt-config-writes-", dir=args.work_root.resolve()))
    binary = str(args.binary.resolve(strict=True))
    root = "/var/tmp/richt-config-writes-" + uuid.uuid4().hex
    transport = {"type": "local"}
    if args.host:
        user, separator, host = args.host.rpartition("@")
        transport = {"type": "ssh", "host": host if separator else args.host, "sudo": True}
        if separator:
            transport["user"] = user
    checks, sequence = [], 0
    report = {"status": "running", "checks": checks, "fixture": root,
              "transport": "ssh-sudo" if args.host else "local",
              "binary_sha256": hashlib.sha256(Path(binary).read_bytes()).hexdigest()}
    created = False

    def command(argv, ok=True, data=None):
        nonlocal sequence
        sequence += 1
        try:
            result = subprocess.run([str(x) for x in argv], input=data, text=True,
                                    capture_output=True, timeout=120)
        except subprocess.TimeoutExpired as error:
            def decoded(value):
                return value.decode(errors="replace") if isinstance(value, bytes) else value or ""
            (work / f"command-{sequence:03d}.log").write_text(
                shlex.join([str(x) for x in argv]) + "\ntimeout\n" +
                ("stdin:\n" + data + "\n" if data is not None else "") +
                decoded(error.stdout) + decoded(error.stderr))
            raise
        (work / f"command-{sequence:03d}.log").write_text(
            shlex.join([str(x) for x in argv]) + "\nexit=" + str(result.returncode) + "\n" +
            ("stdin:\n" + data + "\n" if data is not None else "") + result.stdout + result.stderr)
        if ok is not None and (result.returncode == 0) != ok:
            raise AssertionError(f"unexpected exit {result.returncode}; see command-{sequence:03d}.log")
        return result

    def target(script, params=None, ok=True):
        program = ("import base64, hashlib, json, os, shutil, subprocess, sys\n" +
                   "p = json.loads(" + repr(json.dumps(params or {})) + ")\n" + script)
        argv = ["python3", "-I", "-"]
        if args.host:
            argv = ["ssh", "-T", "-o", "BatchMode=yes", "--", args.host,
                    shlex.join(["sudo", "-n", "--"] + argv)]
        return command(argv, ok, program)

    def snap(path):
        return json.loads(target(SNAPSHOT, {"path": path}).stdout)

    def write(path, data="old\n", **attrs):
        target('''with open(p["path"], "wb") as stream:
    stream.write(p["data"].encode())
os.chown(p["path"], p["uid"], p["gid"])
os.chmod(p["path"], p["mode"])
''', dict(path=path, data=data, mode=attrs.get("mode", 0o640),
          uid=attrs.get("uid", 0), gid=attrs.get("gid", 0)))

    def unlink(path):
        target('os.unlink(p["path"])', {"path": path})

    def link(source, dest, symbolic=False):
        target('os.symlink(p["source"], p["dest"])' if symbolic else 'os.link(p["source"], p["dest"])',
               {"source": source, "dest": dest})

    def check(name, condition=True):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def config(path, content="new\n", **options):
        return dict(path=path, template={"inline": content}, **options)

    def prepare(name, configs, state="present"):
        inventory = {"tags": {"test": {"services": {"test": {"state": state}}}},
                     "services": {"test": {"order": 10, "configs": configs}},
                     "hosts": [{"name": "target", "tags": ["test"], "transport": transport}]}
        inv, saved = work / (name + ".yaml"), work / (name + ".json")
        inv.write_text(json.dumps(inventory, indent=2) + "\n")
        command([binary, "diff", "-i", inv, "-o", saved])
        return saved

    def tasks(saved):
        return json.loads(saved.read_text())["hosts"][0]["tasks"]

    def file_task(saved):
        result = tasks(saved)
        if len(result) != 1 or "file" not in result[0]:
            raise AssertionError("expected one prepared file task: " + str(saved))
        return result[0]["file"]

    def edit_plan(saved, change):
        value = json.loads(saved.read_text())
        change(value)
        saved.write_text(json.dumps(value, indent=2) + "\n")

    def unfinished(saved):
        edit_plan(saved, lambda value: value["hosts"][0]["tasks"][0].update(progress={"status": "running"}))

    def apply(saved, ok=True):
        return command([binary, "apply", "-y", saved], ok)

    def metadata(snapshot):
        return {key: snapshot[key] for key in ("uid", "gid", "mode", "xattrs")}

    def identical(first, second):
        return {k: v for k, v in first.items() if k not in ("inode", "device")} == {
            k: v for k, v in second.items() if k not in ("inode", "device")}

    def backups(path):
        return json.loads(target('print(json.dumps(sorted(name for name in os.listdir(p["root"]) '
                                 'if name.startswith(p["name"] + ".") and name.endswith("~"))))',
                                 {"root": root, "name": Path(path).name}).stdout)

    try:
        facts = json.loads(target('''import platform
assert sys.platform == "linux" and os.geteuid() == 0, "target must be Linux root (sudo -n)"
for tool in ["getenforce", "chcon", "getfacl", "setfacl"]:
    assert shutil.which(tool), "required target tool missing: " + tool
assert subprocess.check_output(["getenforce"], text=True).strip() == "Enforcing", "SELinux Enforcing required"
print(json.dumps({"uname": platform.uname()._asdict(), "python": sys.version,
                  "os_release": open("/etc/os-release").read(), "selinux": "Enforcing"}))
''').stdout)
        report["target"] = facts
        target('os.mkdir(p["root"], 0o700)', {"root": root})
        created = True
        if args.host:
            login = command(["ssh", "-T", "-o", "BatchMode=yes", "--", args.host, "id -u"]).stdout.strip()
            check("SSH uses non-root login and explicit noninteractive sudo", login != "0")

        path = root + "/atomic"
        saved = prepare("create", [config(path)])
        task = file_task(saved)
        check("default atomic write freezes absent/regular states with no backup",
              task["atomic_writes"] is True and task["backup"] is False and
              task["expected"]["kind"] == "absent" and task["desired"]["kind"] == "regular" and
              task["desired"]["sha256"] == hashlib.sha256(b"new\n").hexdigest() and not task.get("backup_path") and
              snap(path)["kind"] == "absent")
        apply(saved)
        check("default create uses prepared bytes and mode0644", snap(path)["mode"] == 0o644 and
              snap(path)["data"] == base64.b64encode(b"new\n").decode())
        created_path = root + "/create-with-backup"
        created_plan = prepare("create-with-backup", [config(created_path, backup=True)])
        apply(created_plan)
        check("creating an absent target with backup requested allocates no backup",
              not file_task(created_plan).get("backup_path") and not backups(created_path))
        before = snap(path)
        saved = prepare("atomic-replace", [config(path, "replacement\n")])
        apply(saved)
        check("default content replacement changes inode", snap(path)["inode"] != before["inode"])

        path, alias = root + "/inplace", root + "/inplace-hardlink"
        write(path)
        link(path, alias)
        before = snap(path)
        saved = prepare("non-atomic", [config(path, atomic_writes=False, backup=True)])
        check("explicit non-atomic setting survives preparation", file_task(saved)["atomic_writes"] is False)
        apply(saved)
        check("non-atomic write preserves inode and updates shared hardlink", snap(path)["inode"] == before["inode"] and
              snap(alias)["sha256"] == snap(path)["sha256"] == hashlib.sha256(b"new\n").hexdigest())

        check("non-atomic backup captures old bytes before shared inode changes",
              identical(before, snap(file_task(saved)["backup_path"])) and
              snap(file_task(saved)["backup_path"])["inode"] != snap(alias)["inode"])

        referent = root + "/referent"
        write(referent, "new\n")
        for label, destination in [("equal-link", referent), ("dangling-link", root + "/missing")]:
            path = root + "/" + label
            link(destination, path, True)
            saved = prepare(label, [config(path)])
            check(label + " freezes link text", file_task(saved)["expected"]["kind"] == "symlink" and
                  file_task(saved)["expected"]["link"] == destination)
            apply(saved)
            check(label + " publishes regular file without following final link", snap(path)["kind"] == "regular" and
                  snap(destination)["kind"] == ("regular" if destination == referent else "absent"))

        path = root + "/remove-link"
        link(referent, path, True)
        before, link_before = snap(referent), snap(path)
        saved = prepare("remove-link", [config(path, backup=True)], "absent")
        backup = file_task(saved)["backup_path"]
        apply(saved)
        check("symlink removal backup retains link and leaves referent intact", snap(path)["kind"] == "absent" and
              identical(link_before, snap(backup)) and snap(referent) == before)

        path, alias = root + "/preserved", root + "/preserved-hardlink"
        write(path, "exact old bytes\x00\n", uid=12501, gid=12502, mode=0o6750)
        target('''os.setxattr(p["path"], "user.richt_acceptance", b"xattr\\x00value")
subprocess.run(["setfacl", "-m", "u:12503:r--", p["path"]], check=True)
subprocess.run(["chcon", "system_u:object_r:etc_t:s0", p["path"]], check=True)
''', {"path": path})
        before = snap(path)
        check("fixture has ACL and SELinux xattrs", "system.posix_acl_access" in before["xattrs"] and
              "security.selinux" in before["xattrs"])
        link(path, alias)
        saved = prepare("preservation", [config(path, backup=True)])
        task = file_task(saved)
        backup = task["backup_path"]
        check("backup destination and identity are frozen in reviewed plan",
              re.fullmatch(re.escape(path) + r"\.\d{8}T\d{6}\.\d{9}Z\." + task["operation_id"] + r"~", backup) is not None and
              re.fullmatch(r"[0-9a-f]{32}", task["operation_id"]) is not None and
              task["expected"]["metadata"] == metadata(before) and snap(backup)["kind"] == "absent")
        apply(saved)
        check("backup preserves exact bytes uid gid mode xattrs ACL and SELinux", identical(before, snap(backup)))
        check("atomic replacement preserves unmanaged metadata", metadata(snap(path)) == metadata(before))
        write(alias, "mutated hardlink\n")
        check("regular backup is independent of old shared inode", identical(before, snap(backup)) and
              snap(backup)["inode"] != snap(alias)["inode"])
        receipt = snap(backup + ".richt.json")
        check("backup receipt is private and tied to frozen operation", receipt["kind"] == "regular" and
              receipt["mode"] == 0o600 and json.loads(base64.b64decode(receipt["data"]))["operation_id"] == task["operation_id"])
        unfinished(saved)
        backup_before = snap(backup)
        apply(saved)
        check("unfinished desired state reuses stable verified backup", snap(backup) == backup_before and
              file_task(saved)["backup_path"] == backup)
        unfinished(saved)
        unlink(backup)
        apply(saved, False)
        check("unfinished desired state refuses missing required backup", snap(path)["sha256"] == hashlib.sha256(b"new\n").hexdigest())

        path = root + "/attributes"
        write(path, "new\n")
        saved = prepare("attributes", [config(path, backup=True, mode="0600")])
        check("attribute-only task allocates no backup", not file_task(saved).get("backup_path"))
        before = snap(path)
        apply(saved)
        saved = prepare("noop", [config(path, backup=True, mode="0600")])
        check("attributes and no-op create no backups", not tasks(saved) and not backups(path) and
              snap(path)["inode"] == before["inode"] and snap(path)["mode"] == 0o600)

        for kind in ["edit", "appearance", "disappearance", "type", "link"]:
            path = root + "/conflict-" + kind
            if kind == "link":
                link(referent, path, True)
            elif kind != "appearance":
                write(path)
            saved = prepare("conflict-" + kind, [config(path, backup=True)])
            if kind in ("edit", "appearance"):
                write(path, "external change\n")
            elif kind == "disappearance":
                unlink(path)
            elif kind == "type":
                unlink(path)
                target('os.mkdir(p["path"])', {"path": path})
            else:
                unlink(path)
                link(root + "/different-referent", path, True)
            before = snap(path)
            apply(saved, False)
            check("prepared " + kind + " conflict fails before target or backup publication",
                  snap(path) == before and not backups(path))

        path = root + "/same-bytes"
        write(path)
        saved = prepare("same-bytes", [config(path)])
        alias = path + "-retained"
        link(path, alias)
        unlink(path)
        write(path)
        check("fixture has equal bytes with replaced inode", snap(path)["inode"] != snap(alias)["inode"])
        apply(saved)
        check("equal old bytes with new inode satisfy prepared precondition", snap(path)["sha256"] == hashlib.sha256(b"new\n").hexdigest())

        path = root + "/live-attributes"
        write(path, "old\n", mode=0o640)
        saved = prepare("live-attributes", [config(path, backup=True)])
        target('os.chmod(p["path"], 0o604); os.setxattr(p["path"], "user.richt_live", b"live")', {"path": path})
        before = snap(path)
        apply(saved)
        check("attribute drift preserves live unmanaged metadata in target and backup",
              metadata(snap(path)) == metadata(before) and identical(snap(file_task(saved)["backup_path"]), before))
        unfinished(saved)
        apply(saved)
        check("continuation verifies actual backup metadata rather than stale prepared attributes",
              metadata(snap(path)) == metadata(before))

        for behavior in ["failure", "mutation"]:
            path = root + "/validator-" + behavior
            write(path)
            script = ('import sys; sys.exit(42)' if behavior == "failure" else
                      'import pathlib,sys; pathlib.Path(sys.argv[1]).write_text("validator corruption\\n")')
            saved = prepare("validator-" + behavior, [config(path, backup=True, validate={
                "args": ["/usr/bin/python3", "-c", script, "{candidate}"], "timeout": "5s"})])
            before = snap(path)
            apply(saved, behavior != "failure")
            valid = (snap(path) == before and not backups(path)) if behavior == "failure" else (
                snap(path)["sha256"] == hashlib.sha256(b"new\n").hexdigest())
            check("validator " + behavior + " preserves publication contract", valid)

        path = root + "/collision"
        write(path)
        saved = prepare("collision", [config(path, backup=True)])
        backup = file_task(saved)["backup_path"]
        write(backup, "unrelated backup collision\n")
        before, existing = snap(path), snap(backup)
        apply(saved, False)
        check("occupied frozen backup destination fails before publication", snap(path) == before and snap(backup) == existing)

        path = root + "/tampered-path"
        write(path)
        saved = prepare("tampered-path", [config(path, backup=True)])
        edit_plan(saved, lambda value: value["hosts"][0]["tasks"][0]["file"].update(backup_path=root + "/unrelated"))
        before = snap(path)
        apply(saved, False)
        check("tampered frozen backup path is rejected before any mutation",
              snap(path) == before and snap(root + "/unrelated")["kind"] == "absent")

        path = root + "/tampered-backup"
        write(path)
        saved = prepare("tampered-backup", [config(path, backup=True)])
        apply(saved)
        write(file_task(saved)["backup_path"], "tampered old bytes\n")
        unfinished(saved)
        before = snap(path)
        apply(saved, False)
        check("continuation refuses a modified previously published backup", snap(path) == before)

        path = root + "/remove-regular"
        write(path)
        before = snap(path)
        saved = prepare("remove-regular", [config(path, backup=True)], "absent")
        apply(saved)
        check("regular removal preserves old bytes and metadata in frozen backup",
              snap(path)["kind"] == "absent" and identical(before, snap(file_task(saved)["backup_path"])))
        unfinished(saved)
        apply(saved)
        check("unfinished removal recognizes absent desired state with verified backup", snap(path)["kind"] == "absent")

        path = root + "/unproven-desired"
        write(path)
        saved = prepare("unproven-desired", [config(path, backup=True)])
        write(path, "new\n")
        unfinished(saved)
        before = snap(path)
        apply(saved, False)
        check("unfinished desired bytes without backup cannot claim success", snap(path) == before and not backups(path))

        path = root + "/legacy"
        write(path)
        saved = prepare("legacy", [config(path)])
        edit_plan(saved, lambda value: value.update(version="1.6"))
        before = snap(path)
        apply(saved, False)
        check("legacy version1.6 plan rejected before mutation", snap(path) == before)
        report["status"] = "passed"
    except BaseException as error:
        report.update(status="failed", error=str(error))
        raise
    finally:
        if created:
            try:
                cleanup = target('shutil.rmtree(p["root"])', {"root": root}, ok=None)
                report["cleanup"] = "removed" if cleanup.returncode == 0 else "failed"
            except BaseException as cleanup_error:
                report["cleanup"] = "failed: " + str(cleanup_error)
            if report["cleanup"] != "removed":
                report["status"] = "failed"
                print("Fixture cleanup failed: " + root, file=sys.stderr)
        report["count"] = len(checks)
        (work / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print("Artifacts: " + str(work), flush=True)
    if report["status"] != "passed":
        raise AssertionError("acceptance or cleanup failed; inspect retained evidence")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
