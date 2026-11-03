#!/usr/bin/env python3
"""Verify typed configs on an explicitly authorized disposable Linux target.

Run locally as root, or use --host with a non-root SSH login and sudo -n.
Requires mdadm, LVM2, xfsprogs, util-linux, GNU coreutils and systemd already
installed. Creates two sparse 2GiB backing files and attaches disposable loops;
Richt creates and deletes their RAID1 -> VG -> LV -> XFS -> mount stack.
No real disks are selected. Unique resources are removed on success/failure.
Boot persistence is verified separately using a persistent loop fixture because
ordinary losetup attachments do not survive reboot.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import sys
import subprocess
import tempfile
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--host", help="SSH alias or user@host with noninteractive sudo")
    parser.add_argument("--work-root", type=Path, help="Retain private controller artifacts")
    parser.add_argument("--source-revision", help="Source revision for copied-target evidence")
    parser.add_argument("--loop-size", default="2G", help="Sparse backing-file size (default 2G)")
    parser.add_argument("--lv-size", default="512MiB", help="Test LV size (default 512MiB)")
    args = parser.parse_args()
    if not args.host and os.geteuid() != 0:
        parser.error("local execution requires root on a disposable Linux host")
    binary = str(args.binary.resolve())
    work = args.work_root.resolve() if args.work_root else Path(tempfile.mkdtemp(prefix="richt-typed-"))
    work.mkdir(mode=0o700, parents=True, exist_ok=True)
    os.chmod(work, 0o700)
    suffix = uuid.uuid4().hex[:12]
    root = "/var/tmp/richt-typed-" + suffix
    array = "/dev/md/richt-" + suffix
    vg = "richt_" + suffix
    source = "/dev/" + vg + "/data"
    mountpoint = root + "/mounted"
    mountpoint_second = root + "/mounted-second"
    marker = "# richt-typed-fstab-" + suffix
    transport = {"type": "local"}
    if args.host:
        transport = {"type": "ssh", "host": args.host, "sudo": True}
        if "@" in args.host:
            transport["user"], transport["host"] = args.host.rsplit("@", 1)
    loops = []
    checks = []
    sequence = 0
    busy_pid = None
    fixtures_created = False
    fstab_modified = False
    expected_md_uuid = None

    def target(script, ok=True, timeout=120):
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

    def run(argv, ok=True, timeout=180):
        nonlocal sequence
        result = subprocess.run([binary] + [str(arg) for arg in argv], text=True,
                                capture_output=True, timeout=timeout)
        sequence += 1
        output = result.stdout + result.stderr
        (work / f"command-{sequence:03}.log").write_text(output)
        if (result.returncode == 0) != ok:
            raise AssertionError(f"unexpected CLI exit {result.returncode}: {argv}\n{output}")
        return output

    def check(name, condition=True):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def inventory(configs, state="present"):
        return {"tags": {"test": {"services": {"test": {"state": state}}}},
                "services": {"test": {"order": 10, "configs": configs}},
                "hosts": [{"name": "target", "tags": ["test"], "transport": transport}]}

    def prepare(name, data):
        inv = work / (name + ".yaml")
        inv.write_text(json.dumps(data, indent=2) + "\n")
        saved = work / (name + ".json")
        output = run(["diff", "-i", inv, "-o", saved])
        return inv, saved, output

    def tasks(saved):
        return json.loads(saved.read_text())["hosts"][0]["tasks"]

    def apply(saved, ok=True):
        return run(["apply", "-y", saved], ok=ok)

    def mounted(path=mountpoint):
        return target("mountpoint -q -- " + shlex.quote(path), ok=False).returncode == 0

    def block_exists(path):
        return target("test -b " + shlex.quote(path), ok=False).returncode == 0

    def fstab():
        return shell("cat /etc/fstab")

    def fstab_target_lines(path=mountpoint):
        return [line for line in fstab().splitlines()
                if line.strip() and not line.lstrip().startswith("#")
                and len(line.split()) > 1 and line.split()[1] == path]

    def metadata_clean(device):
        output = shell("wipefs --json --output TYPE,UUID -- " + shlex.quote(device))
        return json.loads(output).get("signatures", []) == []

    try:
        if args.host:
            identity = subprocess.check_output(["ssh", "-T", "-o", "BatchMode=yes", "--",
                                                args.host, "id -u"], text=True).strip()
            check("SSH login is non-root with explicit noninteractive sudo", identity != "0")
        facts = shell("id -u; uname -m; cat /etc/os-release; systemctl --version; "
                      "mdadm --version 2>&1; lvm version; mkfs.xfs -V; losetup --version")
        (work / "platform.log").write_text(facts)
        check("target operations have root privileges", facts.splitlines()[0] == "0")
        check("unique array and VG are initially absent", not block_exists(array)
              and target("vgs --noheadings -- " + shlex.quote(vg), ok=False).returncode != 0)
        shell("mkdir -m 0700 -- " + shlex.quote(root))
        fixtures_created = True
        for index in range(2):
            backing = root + f"/disk-{index}.img"
            shell("truncate --size " + shlex.quote(args.loop_size) + " -- " + shlex.quote(backing))
            loop = shell("losetup --find --show -- " + shlex.quote(backing)).strip()
            if not loop.startswith("/dev/loop") or "\n" in loop:
                raise AssertionError("unexpected loop path: " + loop)
            loops.append(loop)
        check("two distinct disposable loops are attached", len(set(loops)) == 2)
        shell("printf '%s\\n' " + shlex.quote(marker) + " >> /etc/fstab")
        fstab_modified = True
        original_fstab = fstab()
        mount = {"type": "mount", "source": source, "target": mountpoint,
                 "filesystem": "xfs", "options": ["defaults", "noatime"]}
        storage = [{"type": "mdadm", "array": array, "level": "raid1", "metadata": "1.2",
                    "members": loops},
                   {"type": "lvm-vg", "vg": vg, "devices": [array]},
                   {"type": "lvm-lv", "vg": vg, "lv": "data", "size": args.lv_size,
                    "filesystem": "xfs"}]
        second_mount = dict(mount, target=mountpoint_second)
        configs = storage + [mount, second_mount,
                  {"type": "directory", "path": mountpoint + "/data", "mode": "0750"},
                  {"type": "file", "path": mountpoint + "/data/marker", "mode": "0640"},
                  {"name": "settings", "path": mountpoint + "/data/settings.conf",
                   "template": {"inline": "frozen typed config\n"}, "mode": "0644"}]
        present = inventory(configs)
        inv, saved, output = prepare("create", present)
        prepared = tasks(saved)
        expected_md_uuid = prepared[0]["storage"]["identity"]
        check("preparation leaves array, VG, LV and mount absent",
              not block_exists(array) and not block_exists(source) and not mounted())
        check("preparation freezes all eight configs in declaration order", len(prepared) == 8)
        mount_tasks = [task for task in prepared if task.get("mount")]
        check("downstream mount retains anticipated filesystem UUID and fstab edit",
              len(mount_tasks) == 2 and bool(mount_tasks[0]["mount"]["identity"].get("uuid"))
              and mountpoint in mount_tasks[0]["mount"]["fstab_after"])
        check("described operations expose creation, filesystem and target checks",
              "xfs" in output and array in output and mountpoint in output)
        inv.unlink()
        apply(saved)
        check("saved plan applies without inventory or existing downstream inputs", mounted())
        actual = json.loads(shell("findmnt --json --mountpoint " + shlex.quote(mountpoint)
                                  + " --output SOURCE,FSTYPE,OPTIONS"))["filesystems"][0]
        check("active source is XFS with requested options", actual["fstype"] == "xfs"
              and "noatime" in actual["options"].split(","))
        check("files inside the new filesystem have frozen contents and modes",
              shell("cat -- " + shlex.quote(mountpoint + "/data/settings.conf")) == "frozen typed config\n"
              and shell("stat -c '%a' -- " + shlex.quote(mountpoint + "/data")).strip() == "750"
              and shell("stat -c '%a' -- " + shlex.quote(mountpoint + "/data/marker")).strip() == "640")
        filesystem_uuid = shell("blkid -s UUID -o value -- " + shlex.quote(source)).strip()
        check("fstab persists filesystem UUID and preserves unrelated bytes",
              len(fstab_target_lines()) == 1 and fstab_target_lines()[0].split()[0] == "UUID=" + filesystem_uuid
              and original_fstab in fstab())
        check("multiple mount entries compose without overwriting each other",
              mounted(mountpoint_second) and len(fstab_target_lines(mountpoint_second)) == 1
              and len(fstab_target_lines()) == 1)
        apply(saved)
        check("successful saved tasks are skipped", all(task["progress"]["status"] == "success" for task in tasks(saved)))
        _, unchanged, _ = prepare("unchanged", present)
        check("compatible existing stack adoption produces empty diff", tasks(unchanged) == [])
        shell("printf 'unmanaged data\\n' > " + shlex.quote(mountpoint + "/data/marker"))
        _, preserve, _ = prepare("preserve-file", present)
        check("plain file contents remain unmanaged", tasks(preserve) == [])

        _, mount_absent, _ = prepare("mount-only-absent", inventory([mount], "absent"))
        apply(mount_absent)
        check("mount-only absent removes active mount and fstab entry", not mounted() and not fstab_target_lines())
        check("mount-only absent preserves source and filesystem", block_exists(source)
              and shell("blkid -s UUID -o value -- " + shlex.quote(source)).strip() == filesystem_uuid)
        _, remount, _ = prepare("mount-only-present", inventory([mount]))
        apply(remount)
        check("present restores persisted mount without formatting", mounted()
              and shell("cat -- " + shlex.quote(mountpoint + "/data/marker")) == "unmanaged data\n")

        busy_pid = shell("sh -c " + shlex.quote("cd " + shlex.quote(mountpoint)
                         + "; exec sleep 300") + " </dev/null >/dev/null 2>&1 & echo $!").strip()
        if not busy_pid.isdigit():
            raise AssertionError("unexpected busy fixture PID")
        _, busy, _ = prepare("busy-mount", inventory([mount], "absent"))
        apply(busy, ok=False)
        check("busy unmount fails without force and retains persistence/source", mounted()
              and len(fstab_target_lines()) == 1 and block_exists(source)
              and tasks(busy)[0]["progress"]["status"] == "error")
        shell("kill -- " + busy_pid)
        busy_pid = None
        apply(busy)
        check("explicit continuation completes unfinished unmount", not mounted()
              and tasks(busy)[0]["progress"]["status"] == "success")
        _, remount, _ = prepare("restore-before-delete", inventory([mount]))
        apply(remount)
        _, remove, deletion = prepare("delete", inventory(configs, "absent"))
        removed = tasks(remove)
        check("deletion exposes data loss, scope and reverse config order", len(removed) == 8
              and removed[0]["component"] != prepared[0]["component"]
              and source in deletion and array in deletion)
        apply(remove)
        check("explicit absent unmounts and destroys array, LV and VG", not mounted()
              and not block_exists(source) and not block_exists(array) and not mounted(mountpoint_second)
              and target("vgs --noheadings -- " + shlex.quote(vg), ok=False).returncode != 0)
        check("array destruction removes metadata on both exact members", all(metadata_clean(loop) for loop in loops))
        check("deletion removes fstab entry and preserves unrelated comments", not fstab_target_lines()
              and fstab() == original_fstab)
        apply(remove)
        check("successful destructive tasks are skipped on continuation",
              all(task["progress"]["status"] == "success" for task in tasks(remove)))
        _, already_absent, _ = prepare("already-absent", inventory(configs, "absent"))
        check("already absent stack prepares no work", tasks(already_absent) == [])
    finally:
        active_error = sys.exc_info()[1]
        cleanup_errors = []
        if busy_pid:
            target("kill -- " + busy_pid, ok=False)
        if fixtures_created:
            # Cleanup is restricted to unique resources and the exact loops we attached.
            for cleanup_mount in [mountpoint_second, mountpoint]:
                if mounted(cleanup_mount):
                    result = target("umount -- " + shlex.quote(cleanup_mount), ok=False)
                    if result.returncode:
                        cleanup_errors.append("fixture mount remains busy; backing storage retained")
            if not mounted() and not mounted(mountpoint_second):
                target("lvremove -y -- " + shlex.quote(source), ok=False)
                target("vgremove -y -- " + shlex.quote(vg), ok=False)
                cleanup_array = array if block_exists(array) else None
                if cleanup_array is None and expected_md_uuid:
                    # udev may remove the friendly /dev/md/name alias while the
                    # kernel array remains. Match the fixed prepared UUID.
                    scan = shell("for entry in /sys/block/md*; do [ -e \"$entry\" ] || continue; "
                                 "device=/dev/${entry##*/}; printf '%s ' \"$device\"; "
                                 "mdadm --detail --export \"$device\" | sed -n 's/^MD_UUID=//p'; done")
                    for line in scan.splitlines():
                        fields = line.split()
                        if len(fields) == 2 and fields[1] == expected_md_uuid:
                            cleanup_array = fields[0]
                if cleanup_array:
                    target("pvremove -y -- " + shlex.quote(cleanup_array), ok=False)
                    result = target("mdadm --stop " + shlex.quote(cleanup_array), ok=False)
                    if result.returncode:
                        cleanup_errors.append("cannot stop the exact prepared fixture array")
                if not cleanup_errors:
                    for loop in loops:
                        target("mdadm --zero-superblock " + shlex.quote(loop), ok=False)
                        result = target("losetup --detach " + shlex.quote(loop), ok=False)
                        if result.returncode and target("losetup " + shlex.quote(loop), ok=False).returncode == 0:
                            cleanup_errors.append("cannot detach " + loop)
                if not cleanup_errors:
                    target("rm -rf -- " + shlex.quote(root), ok=False)
        if fstab_modified:
            # Remove our exact test entry/comment while retaining other host entries.
            script = "awk -v target=" + shlex.quote(mountpoint) + " -v second=" + shlex.quote(mountpoint_second) + " -v marker=" + shlex.quote(marker) + " '$0 != marker && $2 != target && $2 != second' /etc/fstab > " + shlex.quote(root + "-fstab-clean") + "; cat " + shlex.quote(root + "-fstab-clean") + " > /etc/fstab; rm -f -- " + shlex.quote(root + "-fstab-clean") + "; systemctl daemon-reload"
            result = target(script, ok=False)
            if result.returncode:
                cleanup_errors.append("cannot clean fixture fstab entries")
        def git_metadata(*arguments):
            if not shutil.which("git"):
                return ""
            return subprocess.run(["git"] + list(arguments), text=True,
                                  capture_output=True).stdout.strip()

        report = {"checks": checks, "passed": len(checks), "target": args.host or "local",
                  "fixture": root, "loops": loops, "cleanup_errors": cleanup_errors,
                  "boot_persistence": "Requires separate persistent-loop fixture and reboot",
                  "binary_sha256": hashlib.sha256(args.binary.read_bytes()).hexdigest(),
                  "source_revision": args.source_revision or git_metadata("rev-parse", "HEAD"),
                  "working_tree": git_metadata("status", "--short").splitlines(),
                  "failure": str(active_error) if active_error else None}
        (work / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        if cleanup_errors:
            if active_error:
                print("Cleanup: " + "; ".join(cleanup_errors), file=sys.stderr)
            else:
                raise AssertionError("; ".join(cleanup_errors))
    print(f"Passed {len(checks)} typed config checks; artifacts: {work}")


if __name__ == "__main__":
    main()
