#!/usr/bin/env python3
"""Verify published download/archive lifecycle on an authorized disposable SSH host.

Requires a Linux amd64/systemd/Python3 target, target HTTPS access to GitHub,
normal trusted noninteractive SSH, and sudo -n. The controller runs Richt.
Uses unique /opt paths and units; ports must be unused. K3s runs only kubectl
proxy against a deliberately unavailable API endpoint, never a server/agent.
All fixture resources are removed, while private controller evidence is kept.
"""

import argparse
import hashlib
import json
from pathlib import Path
import shlex
import subprocess
import uuid


NODE = {
    "1.10.1": "0662f0bc0d57470fe2188738c1bec1ec99716e899d0c557480fb94b9b1d9825f",
    "1.10.2": "c46e5b6f53948477ff3a19d97c58307394a29fe64a01905646f026ddc32cb65b",
}
K3S = {
    "1.34.2+k3s1": "15811d34187e03fe29d0e46108c42cc33e62cec1b3d9d80f50944536ebcad40a",
    "1.34.3+k3s1": "f95350e3997165974d1d1406f01aabaf869aed7ca790cc2d83a92a82f26a06c5",
}
# Digests are from the release's sha256sums.txt / sha256sum-amd64.txt.


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--host", required=True, help="Trusted SSH alias or user@host with sudo -n")
    parser.add_argument("--work-root", required=True, type=Path)
    parser.add_argument("--node-port", type=int, default=19183)
    parser.add_argument("--proxy-port", type=int, default=19184)
    parser.add_argument("--archive-only", action="store_true", help="Skip the K3s download/proxy fixture")
    args = parser.parse_args()
    binary = str(args.binary.resolve())
    work = args.work_root.resolve() / ("run-" + uuid.uuid4().hex[:12])
    work.mkdir(mode=0o700, parents=True)
    suffix = uuid.uuid4().hex[:12]
    root = "/opt/richt-artifacts-" + suffix
    units = {kind: "richt-artifacts-" + suffix + "-" + kind + ".service" for kind in ["node", "k3s"]}
    transport = {"type": "ssh", "host": args.host, "sudo": True}
    if "@" in args.host:
        transport["user"], transport["host"] = args.host.rsplit("@", 1)
    checks = []
    sequence = 0
    created = False
    versions = {}

    def target(script, ok=True, timeout=45):
        command = ["ssh", "-T", "-o", "BatchMode=yes", "--", args.host,
                   shlex.join(["sudo", "-n", "--", "sh", "-c", script])]
        result = subprocess.run(command, capture_output=True, text=True, timeout=timeout)
        if ok and result.returncode:
            raise AssertionError("target setup/check failed: " + result.stdout + result.stderr)
        return result

    def shell(script, timeout=45):
        return target(script, timeout=timeout).stdout

    def run(argv, ok=True):
        nonlocal sequence
        result = subprocess.run([binary] + [str(arg) for arg in argv], text=True, capture_output=True, timeout=420)
        sequence += 1
        output = result.stdout + result.stderr
        (work / ("command-%03d.log" % sequence)).write_text(output)
        if (result.returncode == 0) != ok:
            raise AssertionError("unexpected CLI exit " + str(argv) + "\n" + output)
        return output

    def check(name, condition=True):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def exists(path):
        return target("test -e " + shlex.quote(path), ok=False).returncode == 0

    def marker(kind):
        return shell("if test -f " + shlex.quote(root + "/" + kind + "-starts") + "; then cat "
                     + shlex.quote(root + "/" + kind + "-starts") + "; fi").splitlines()

    def component(kind, version):
        if kind == "node":
            return {"name": "artifact", "type": "archive", "url": "https://github.com/prometheus/node_exporter/releases/download/v"
                    + version + "/node_exporter-" + version + ".linux-amd64.tar.gz", "sha256": NODE[version],
                    "path": root + "/node", "format": "tar.gz", "strip_components": 1,
                    "owner": "root", "group": "root", "timeout": "5m"}
        return {"name": "artifact", "type": "download", "url": "https://github.com/k3s-io/k3s/releases/download/v"
                + version.replace("+", "%2B") + "/k3s", "sha256": K3S[version], "path": root + "/k3s",
                "owner": "root", "group": "root", "mode": "0755", "timeout": "5m"}

    def inventory(kind, version, state="present"):
        exe = root + "/node/node_exporter" if kind == "node" else root + "/k3s"
        command = exe + " --web.listen-address=127.0.0.1:" + str(args.node_port) if kind == "node" else (
            exe + " kubectl --kubeconfig=" + root + "/kubeconfig proxy --address=127.0.0.1 --port=" + str(args.proxy_port))
        environment = "" if kind == "node" else "Environment=K3S_DATA_DIR=" + root + "/k3s-data\nEnvironment=K3S_CONFIG_FILE=" + root + "/k3s-config.yaml\n"
        unit = "[Unit]\nDescription=Isolated Richt artifact acceptance\n[Service]\n" + environment
        unit += "ExecStartPre=/bin/sh -c 'echo start >> " + root + "/" + kind + "-starts'\nExecStart=" + command + "\n[Install]\nWantedBy=multi-user.target\n"
        configs = []
        if kind == "k3s":
            configs = [{"path": root + "/kubeconfig", "owner": "root", "group": "root", "mode": "0600", "template": {"inline":
                "apiVersion: v1\nkind: Config\nclusters:\n- name: unavailable\n  cluster: {server: 'http://127.0.0.1:9'}\ncontexts:\n- name: fixture\n  context: {cluster: unavailable, user: fixture}\ncurrent-context: fixture\nusers:\n- name: fixture\n  user: {}\n"}},
                {"path": root + "/k3s-config.yaml", "mode": "0600", "template": {"inline": "{}\n"}}]
        service = {"order": 10, "binaries": [component(kind, version)], "configs": configs, "processes": [{
            "name": "daemon", "type": "systemd", "unit": units[kind], "enabled": True, "timeout": "30s",
            "restart_on": ["binary.artifact"], "unit_file": {"owner": "root", "group": "root", "mode": "0644", "template": {"inline": unit}}}]}
        return {"tags": {"test": {"services": {"test": {"state": state}}}}, "services": {"test": service},
                "hosts": [{"name": "target", "tags": ["test"], "transport": transport}]}

    def prepare(label, kind, version, state="present", ok=True):
        inv, saved = work / (label + ".yaml"), work / (label + ".json")
        inv.write_text(json.dumps(inventory(kind, version, state), indent=2) + "\n")
        run(["diff", "-i", inv, "-o", saved], ok)
        return inv, saved

    def tasks(saved):
        return json.loads(saved.read_text())["hosts"][0]["tasks"]

    def verify(kind, version):
        state = shell("systemctl is-active " + shlex.quote(units[kind])).strip()
        check(kind + " process is active at " + version, state == "active")
        if kind == "node":
            output = shell("curl -fsS --max-time 10 --retry 20 --retry-connrefused --retry-delay 1 --retry-max-time 20 http://127.0.0.1:" + str(args.node_port) + "/metrics")
            check("node_exporter serves expected version metrics", 'node_exporter_build_info{' in output and 'version="' + version + '"' in output)
        else:
            output = shell("K3S_DATA_DIR=" + shlex.quote(root + "/k3s-data") + " K3S_CONFIG_FILE=" + shlex.quote(root + "/k3s-config.yaml") + " " + shlex.quote(root + "/k3s") + " --version")
            check("downloaded K3s version matches", "v" + version in output)
            probe = "import socket,time\nfor attempt in range(40):\n try:\n  socket.create_connection(('127.0.0.1', " + str(args.proxy_port) + "), 1).close(); break\n except OSError:\n  if attempt == 39: raise\n  time.sleep(0.5)\n"
            shell("python3 -c " + shlex.quote(probe))
            check("K3s kubectl proxy listens without creating a cluster", not exists("/var/lib/rancher/k3s"))
        versions[kind] = version
        check("neighbor file remains intact", shell("cat " + shlex.quote(root + "/neighbor")) == "unrelated neighbor\n")

    try:
        facts = shell("id; uname -m; cat /etc/os-release; systemctl --version; python3 --version; getenforce")
        (work / "platform.log").write_text(facts)
        check("fixture needs Linux amd64", "x86_64" in facts)
        check("unique target directory and units are absent", not exists(root)
              and all(not exists("/etc/systemd/system/" + unit) for unit in units.values()))
        if not args.archive_only:
            check("K3s default data directory is absent before isolated proxy probe", not exists("/var/lib/rancher/k3s"))
        ports = [args.node_port] + ([] if args.archive_only else [args.proxy_port])
        probe = "import socket\nsockets=[]\nfor port in " + repr(ports) + ":\n s=socket.socket(); s.bind(('127.0.0.1',port)); sockets.append(s)\n"
        shell("python3 -c " + shlex.quote(probe))
        check("requested loopback ports are initially unused")
        shell("mkdir -m 0755 " + shlex.quote(root) + "; printf 'unrelated neighbor\\n' > " + shlex.quote(root + "/neighbor"))
        created = True
        cases = [("node", "1.10.1", "1.10.2")]
        if not args.archive_only:
            cases.append(("k3s", "1.34.2+k3s1", "1.34.3+k3s1"))
        for kind, older, newer in cases:
            inv, saved = prepare(kind + "-install", kind, older)
            run(["inventory", "validate", "-i", inv])
            inv.unlink()
            run(["apply", "-y", saved])
            verify(kind, older)
            check(kind + " install starts once from frozen inventory", len(marker(kind)) == 1)
            run(["apply", "-y", saved])
            check(kind + " successful continuation skips process and artifact", len(marker(kind)) == 1)
            _, noop = prepare(kind + "-noop", kind, older)
            check(kind + " matching diff has no tasks", tasks(noop) == [])
            for label, version, expected in [("upgrade", newer, 2), ("downgrade", older, 3)]:
                _, saved = prepare(kind + "-" + label, kind, version)
                check(kind + " " + label + " prepares artifact and restart", any(task["operation"] == "artifact" for task in tasks(saved)))
                run(["apply", "-y", saved])
                verify(kind, version)
                check(kind + " " + label + " restarts exactly once", len(marker(kind)) == expected)
            if kind == "node":
                shell("printf foreign > " + shlex.quote(root + "/node/foreign"))
                prepare("node-foreign-update", kind, newer, ok=False)
                prepare("node-foreign-remove", kind, older, state="absent", ok=False)
                check("SSH unknown archive entry blocks update and removal", shell("cat " + shlex.quote(root + "/node/foreign")) == "foreign")
                shell("rm -- " + shlex.quote(root + "/node/foreign"))
            _, saved = prepare(kind + "-remove", kind, older, state="absent")
            run(["apply", "-y", saved])
            check(kind + " absent removes artifact and owned unit", not exists(component(kind, older)["path"])
                  and not exists("/etc/systemd/system/" + units[kind]))
            check(kind + " absent preserves neighbor", shell("cat " + shlex.quote(root + "/neighbor")) == "unrelated neighbor\n")
            _, noop = prepare(kind + "-absent-noop", kind, older, state="absent")
            check(kind + " repeated absent has no tasks", tasks(noop) == [])
        report = {"status": "passed", "count": len(checks), "checks": checks, "transport": "ssh-sudo",
                  "versions": versions, "binary_sha256": hashlib.sha256(Path(binary).read_bytes()).hexdigest(),
                  "k3s_scope": "kubectl proxy only; no cluster install, Kubernetes downgrade, readiness or teardown validation"}
        (work / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
    finally:
        if created:
            commands = []
            for unit in units.values():
                commands.append("systemctl stop " + shlex.quote(unit) + " 2>/dev/null || :")
                commands.append("systemctl disable " + shlex.quote(unit) + " 2>/dev/null || :")
                commands.append("rm -f -- " + shlex.quote("/etc/systemd/system/" + unit))
            commands.extend(["systemctl daemon-reload", "rm -rf -- " + shlex.quote(root)])
            result = target("; ".join(commands), ok=False, timeout=90)
            (work / "cleanup.log").write_text(result.stdout + result.stderr)
            if result.returncode:
                raise AssertionError("fixture cleanup failed; inspect " + str(work))
        print("Artifacts: " + str(work))


if __name__ == "__main__":
    main()
