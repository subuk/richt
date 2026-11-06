#!/usr/bin/env python3
"""APT acceptance on an explicitly authorized disposable Linux systemd target.

Run locally as root, or pass --ssh-host for a login with sudo -n. The controller
needs the Richt binary; the target needs apt-utils, dpkg-dev, gnupg, Python 3,
systemd and root privileges. This builds unique packages and a signed file
repository, changes the target package database and temporarily holds its native
frontend lock. Do not run concurrently with other package work. Unique target
assets are cleaned; private inventories, plans, command logs and acceptance.json
are retained in --output (or --work-root).
"""

import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile
import time
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--ssh-host", help="SSH alias or user@host with sudo -n")
    destination = parser.add_mutually_exclusive_group()
    destination.add_argument("--output", type=Path)
    destination.add_argument("--work-root", type=Path)
    args = parser.parse_args()
    if not args.ssh_host and os.geteuid() != 0:
        parser.error("local acceptance requires root on an authorized disposable host")
    output = (args.output or args.work_root or Path(tempfile.mkdtemp(prefix="richt-apt-"))).resolve()
    output.mkdir(mode=0o700, parents=True, exist_ok=True)
    output.chmod(0o700)
    binary = str(args.binary.resolve())
    suffix = uuid.uuid4().hex
    name = "richt-apt-" + suffix
    root = "/var/tmp/" + name
    unit = name + ".service"
    conffile = "/etc/" + name + ".conf"
    source = "/etc/apt/sources.list.d/" + name + ".list"
    key = "/etc/apt/keyrings/" + name + ".asc"
    repo = root + "/repo"
    transport = {"type": "local"}
    if args.ssh_host:
        user, separator, host = args.ssh_host.rpartition("@")
        transport = {"type": "ssh", "host": host if separator else args.ssh_host, "sudo": True}
        if separator:
            transport["user"] = user
    checks = []
    sequence = 0
    fixtures_owned = False
    failure = None
    source_comment = ""
    source_repo = repo

    def command(argv, ok=True, timeout=300):
        nonlocal sequence
        sequence += 1
        label = output / f"{sequence:03d}.log"
        try:
            result = subprocess.run(argv, capture_output=True, text=True, timeout=timeout, umask=0o077)
        except subprocess.TimeoutExpired as error:
            label.write_text(shlex.join(argv) + "\nTIMEOUT\n" + str(error))
            raise
        label.write_text(shlex.join(argv) + "\n" + result.stdout + result.stderr)
        if ok is not None and ok != (result.returncode == 0):
            raise AssertionError(f"exit {result.returncode}; see {label}\n{result.stdout}{result.stderr}")
        return result

    def target(script, ok=True, timeout=300):
        argv = ["sh", "-c", "set -eu\n" + script]
        if args.ssh_host:
            argv = ["ssh", "-T", "-o", "BatchMode=yes", "--", args.ssh_host,
                    shlex.join(["sudo", "-n", "--"] + argv)]
        return command(argv, ok=ok, timeout=timeout)

    def write_target(filename, content, mode="0644"):
        target("python3 -c " + shlex.quote(
            "from pathlib import Path; p=Path(" + repr(filename) + "); p.write_text(" + repr(content) + "); p.chmod(0o" + mode + ")"))

    def check(label, condition=True):
        if not condition:
            raise AssertionError(label)
        checks.append(label)
        print("PASS " + label, flush=True)

    def inventory(version="1.0-1", state="present", later=None):
        binary_request = {"name": "package", "type": "deb", "package": name}
        if version is not None:
            binary_request["version"] = version
        services = {
            "repository": {"order": 10, "configs": [
                {"path": key, "mode": "0644", "template": {"inline": public_key}},
                {"path": source, "template": {"inline": f"deb [signed-by={key}] file:{source_repo} ./\n" + source_comment}}]},
            "application": {"order": 20,
                            "binaries": [binary_request],
                            "configs": [{"path": root + "/managed", "template": {"inline": "prepared configuration\n"}}],
                            "processes": [{"type": "systemd", "unit": unit, "enabled": True,
                                           "state": "running", "restart_on": ["binary.package"]}]}}
        assignments = {"repository": {}, "application": {"state": state}}
        if later:
            services["later"] = {"order": 30, "configs": [{"path": root + "/" + later,
                                                          "template": {"inline": "later task completed\n"}}]}
            assignments["later"] = {}
        return {"tags": {"test": {"services": assignments}}, "services": services,
                "hosts": [{"name": "target", "tags": ["test"], "transport": transport}]}

    def prepare(label, version="1.0-1", state="present", later=None, ok=True):
        inv = output / (label + ".yaml")
        inv.write_text(json.dumps(inventory(version, state, later), indent=2) + "\n")
        saved = output / (label + ".json")
        command([binary, "diff", "-i", str(inv), "-o", str(saved)], ok=ok)
        return saved

    def tasks(saved):
        return json.loads(saved.read_text())["hosts"][0]["tasks"]

    def apply(saved, ok=True):
        return command([binary, "apply", "-y", str(saved)], ok=ok)

    def installed(version):
        target("test \"$(dpkg-query --show --showformat='${Version}' " + name + ")\" = " + shlex.quote(version))

    def publish(versions):
        target(f"rm -f {repo}/*.deb; " + " ".join(f"cp {root}/packages/{name}_{v}_all.deb {repo}/;" for v in versions) +
               f" cd {repo}; dpkg-scanpackages --multiversion . /dev/null > Packages; gzip -c Packages > Packages.gz; "
               "apt-ftparchive release . > Release; "
               f"gpg --homedir {root}/gnupg --batch --yes --pinentry-mode loopback --passphrase '' --clearsign -o InRelease Release; "
               f"gpg --homedir {root}/gnupg --batch --yes --pinentry-mode loopback --passphrase '' --armor --detach-sign -o Release.gpg Release; "
               "chmod 0644 ./*")

    def lists_snapshot():
        return target("python3 -c " + shlex.quote(
            "import pathlib,hashlib,json; root=pathlib.Path('/var/lib/apt/lists'); "
            "print(json.dumps(sorted((str(p),p.stat().st_mtime_ns,p.stat().st_size,hashlib.sha256(p.read_bytes()).hexdigest()) "
            "for p in root.rglob('*') if p.is_file()),sort_keys=True))")).stdout

    def cache_stamp():
        return json.loads(target("cat /var/lib/richt/apt/success.json").stdout)

    def release_lock():
        target(f"rm -f {root}/hold-lock; "
               f"for n in $(seq 1 100); do test ! -e {root}/lock-ready && exit 0; sleep 0.1; done; exit 1")

    try:
        facts = target("test \"$(id -u)\" = 0; uname -m; cat /etc/os-release; "
                       "apt-get --version; dpkg --version; systemctl --version; "
                       "systemctl show --property=Version; python3 --version; "
                       "command -v apt-ftparchive dpkg-deb dpkg-scanpackages gpg; "
                       "if command -v getenforce >/dev/null; then getenforce; else echo 'SELinux tool unavailable'; fi").stdout
        (output / "platform.log").write_text(facts)
        target(f"test ! -e {root}; test ! -e {source}; test ! -e {key}; "
               f"test ! -e /usr/lib/systemd/system/{unit}; test ! -e {conffile}; "
               f"test -z \"$(dpkg-query --show --showformat='${{db:Status-Abbrev}}' {name} 2>/dev/null || true)\"")
        target(f"mkdir -m 0755 {root}; mkdir -m 0755 {repo} {root}/packages; mkdir -m 0700 {root}/gnupg; mkdir -p /etc/apt/keyrings")
        fixtures_owned = True
        build_script = f'''from pathlib import Path
import subprocess
root=Path({root!r}); name={name!r}; unit={unit!r}; conf={conffile!r}
for version in ['1.0-1','2.0-1']:
    stage=root/('build-'+version)
    for directory in ['DEBIAN','etc','usr/lib/systemd/system']:
        (stage/directory).mkdir(parents=True)
    (stage/'DEBIAN/control').write_text('Package: '+name+'\\nVersion: '+version+'\\nArchitecture: all\\nMaintainer: Richt acceptance <acceptance@example.invalid>\\nDescription: Disposable Richt APT acceptance fixture\\n')
    (stage/'DEBIAN/conffiles').write_text(conf+'\\n')
    (stage/'DEBIAN/postinst').write_text('#!/bin/sh\\nset -eu\\nif test -e {root}/fail-postinst; then exit 42; fi\\nexit 0\\n')
    (stage/'DEBIAN/postinst').chmod(0o755)
    (stage/conf.lstrip('/')).write_text('package conffile '+version+'\\n')
    (stage/'usr/lib/systemd/system'/unit).write_text('[Unit]\\nDescription=Richt disposable fixture\\n[Service]\\nExecStart=/bin/sleep infinity\\n[Install]\\nWantedBy=multi-user.target\\n')
    stage.chmod(0o755)
    for entry in stage.rglob('*'):
        entry.chmod(0o755 if entry.is_dir() else 0o644)
    (stage/'DEBIAN/postinst').chmod(0o755)
    subprocess.run(['dpkg-deb','--build','--root-owner-group',str(stage),str(root/'packages'/(name+'_'+version+'_all.deb'))],check=True)
'''
        target("python3 -c " + shlex.quote(build_script))
        target(f"gpg --homedir {root}/gnupg --batch --pinentry-mode loopback --passphrase '' --quick-generate-key '{name} <acceptance@example.invalid>' rsa2048 sign 0")
        exported = target(f"gpg --homedir {root}/gnupg --batch --armor --export").stdout
        # APT requires .asc for armored keys; text templates prepare the public
        # trust key alongside the new source without an external setup command.
        public_key = exported
        publish(["1.0-1"])
        before = lists_snapshot()
        target(f"mv {repo} {root}/offline-repo")
        saved = prepare("install")
        target(f"mv {root}/offline-repo {repo}")
        check("diff succeeds with the new signed repository offline and leaves APT indexes unchanged", before == lists_snapshot())
        check("repository configs precede package, managed config and process tasks",
              [t["service"] for t in tasks(saved)][:2] == ["repository", "repository"] and
              next(i for i, t in enumerate(tasks(saved)) if t["operation"] == "deb") <
              next(i for i, t in enumerate(tasks(saved)) if t["service"] == "application" and t["operation"] == "file"))
        (output / "install.yaml").unlink()
        apply(saved)
        installed("1.0-1")
        target(f"systemctl is-active --quiet {unit}; systemctl is-enabled --quiet {unit}; test -f {root}/managed")
        check("saved apply installs from the signed repository before config and process without inventory")
        check("second diff is empty", not tasks(prepare("noop")))

        unavailable = prepare("unavailable", "2.0-1", later="after-unavailable")
        fresh_stamp = cache_stamp()
        apply(unavailable, ok=False)
        target(f"test ! -e {root}/after-unavailable")
        check("unavailable exact version fails and blocks later host tasks",
              next(t for t in tasks(unavailable) if t["operation"] == "deb")["progress"]["status"] == "error")
        check("a fresh successful cache is reused for the next package request", cache_stamp() == fresh_stamp)
        publish(["1.0-1", "2.0-1"])
        target("apt-get -o APT::Update::Error-Mode=any update", timeout=300)
        (output / "unavailable.yaml").unlink()
        apply(unavailable)
        installed("2.0-1")
        target(f"test -f {root}/after-unavailable")
        check("manual continuation succeeds after publishing the missing version without inventory")
        write_target(conffile, "operator conffile\n")
        prior_stamp = cache_stamp()
        source_comment = "# Richt acceptance source invalidation\n"
        apply(prepare("downgrade", "1.0-1"))
        installed("1.0-1")
        check("an earlier repository service change invalidates the fresh APT cache",
              cache_stamp()["config"] != prior_stamp["config"] and cache_stamp()["time"] > prior_stamp["time"])
        check("an unversioned request preserves the installed version when a newer repository version exists",
              not tasks(prepare("unversioned-preserve", None)))
        installed("1.0-1")
        upgrade = prepare("upgrade", "2.0-1")
        target("python3 -c " + shlex.quote("import json; from pathlib import Path; p=Path('/var/lib/richt/apt/success.json'); v=json.loads(p.read_text()); v['time']=0; p.write_text(json.dumps(v)); p.chmod(0o600)"))
        apply(upgrade)
        installed("2.0-1")
        check("an expired 48-hour freshness stamp causes a successful refresh", cache_stamp()["time"] > 0)
        target(f"test \"$(cat {conffile})\" = 'operator conffile'")
        check("exact upgrade and downgrade preserve an operator-modified conffile")
        check("Debian equality accepts the zero epoch", not tasks(prepare("epoch-equality", "0:2.0-1")))

        locked = prepare("native-lock", "1.0-1", later="after-lock")
        data = json.loads(locked.read_text())
        for task in data["hosts"][0]["tasks"]:
            if task["operation"] == "deb":
                task["deb"]["lock_timeout"] = "1s"
        locked.write_text(json.dumps(data, indent=2) + "\n")
        lock_script = """import fcntl,time
from pathlib import Path
root=Path(ROOT)
with open('/var/lib/dpkg/lock-frontend','a') as lock:
    fcntl.lockf(lock,fcntl.LOCK_EX)
    (root/'lock-ready').touch()
    try:
        while (root/'hold-lock').exists(): time.sleep(0.1)
    finally: (root/'lock-ready').unlink(missing_ok=True)
""".replace("ROOT", repr(root))
        write_target(root + "/hold-native-lock.py", lock_script)
        target(f"touch {root}/hold-lock; nohup python3 {root}/hold-native-lock.py >{root}/native-lock.log 2>&1 </dev/null &")
        target(f"for n in $(seq 1 100); do test -e {root}/lock-ready && exit 0; sleep 0.1; done; exit 1")
        try:
            started = time.monotonic()
            locked_result = apply(locked, ok=False)
            elapsed = time.monotonic() - started
            target(f"test ! -e {root}/after-lock")
            installed("2.0-1")
            lock_output = (locked_result.stdout + locked_result.stderr).lower()
            check("native frontend lock contention fails within ten seconds for the frozen one-second budget before mutation",
                  0.75 <= elapsed < 10 and "lock" in lock_output and "timed out" in lock_output)
        finally:
            release_lock()
        (output / "native-lock.yaml").unlink()
        apply(locked)
        installed("1.0-1")
        check("explicit continuation succeeds after releasing the native lock")

        prior_stamp = cache_stamp()
        apply(prepare("fresh-upgrade", "2.0-1"))
        installed("2.0-1")
        check("a successful exact version change reuses unchanged fresh repository indexes", cache_stamp() == prior_stamp)
        apply(prepare("before-half-configured", "1.0-1"))

        broken = prepare("half-configured", "2.0-1", later="after-half-configured")
        prior_stamp = cache_stamp()
        target(f"find /var/lib/apt/lists -maxdepth 1 -type f -name '*{suffix}*Packages*' -print -delete")
        target(f"touch {root}/fail-postinst")
        apply(broken, ok=False)
        check("deleted repository package indexes invalidate freshness before a transaction",
              cache_stamp()["time"] > prior_stamp["time"])
        target(f"test \"$(dpkg-query --show --showformat='${{db:Status-Abbrev}}' {name})\" = 'iF '; test ! -e {root}/after-half-configured")
        prepare("broken-database", "2.0-1", ok=False)
        check("real postinst failure leaves half-configured state and blocks later tasks and preparation")
        target(f"rm {root}/fail-postinst; dpkg --configure {name}")
        (output / "half-configured.yaml").unlink()
        apply(broken)
        installed("2.0-1")
        target(f"test -f {root}/after-half-configured")
        check("explicit dpkg repair then saved continuation completes without inventory")

        source_repo = root + "/missing-repository"
        failed_refresh = prepare("failed-refresh", "1.0-1", later="after-failed-refresh")
        apply(failed_refresh, ok=False)
        installed("2.0-1")
        target(f"test ! -e {root}/after-failed-refresh; test ! -e /var/lib/richt/apt/success.json")
        check("a failed signed repository refresh prevents installation and leaves no success stamp")
        target(f"ln -s {repo} {source_repo}")
        (output / "failed-refresh.yaml").unlink()
        apply(failed_refresh)
        installed("1.0-1")
        check("an unavailable repository can be corrected externally before explicit saved continuation")
        source_repo = repo

        absent = prepare("absent", "2.0-1", state="absent")
        package_position = next(i for i, t in enumerate(tasks(absent)) if t["operation"] == "deb")
        check("absence prepares process stop and disable before package removal",
              any(t["operation"].startswith("systemd") for t in tasks(absent)[:package_position]))
        apply(absent)
        target(f"test \"$(dpkg-query --show --showformat='${{db:Status-Abbrev}}' {name})\" = 'rc '; "
               f"test \"$(cat {conffile})\" = 'operator conffile'; test ! -e {root}/managed; "
               f"test ! -e /etc/systemd/system/multi-user.target.wants/{unit}; "
               f"if systemctl is-active --quiet {unit}; then exit 1; fi")
        check("explicit absence stops and disables the unit, removes the package and retains conffiles")
        check("repeated absent diff is empty", not tasks(prepare("absent-noop", "2.0-1", state="absent")))
    except Exception as error:
        failure = str(error)
        raise
    finally:
        cleanup_failure = None
        if fixtures_owned:
            result = target(f"rm -f {root}/hold-lock {root}/fail-postinst; "
                            f"systemctl stop {unit} >/dev/null 2>&1 || true; systemctl disable {unit} >/dev/null 2>&1 || true; "
                            f"dpkg --purge {name}; rm -f {source} {key} {conffile} {conffile}.dpkg-dist {conffile}.dpkg-old {conffile}.dpkg-new; systemctl daemon-reload; "
                            f"find /var/lib/apt/lists -maxdepth 1 -type f -name '*{suffix}*' -delete; "
                            f"find /var/cache/apt/archives -maxdepth 1 -type f -name '{name}_*.deb' -delete; "
                            f"gpgconf --homedir {root}/gnupg --kill gpg-agent; rm -rf --one-file-system -- {root}", ok=None)
            if result.returncode:
                cleanup_failure = "fixture cleanup failed; see final command log"
        report = {"checks": checks, "count": len(checks), "transport": "ssh-sudo" if args.ssh_host else "local",
                  "fixture": name, "failure": failure, "cleanup_failure": cleanup_failure}
        (output / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2), flush=True)
        if cleanup_failure and failure is None:
            raise AssertionError(cleanup_failure)


if __name__ == "__main__":
    main()
