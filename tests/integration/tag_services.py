#!/usr/bin/env python3
"""Apply the KRG-91 tag workload to three explicitly named disposable hosts.

Requires fresh AlmaLinux/DNF4/systemd targets, trusted SSH keys, noninteractive
sudo, enabled CRB repositories, Python 3, curl, and ss. Installs PostgreSQL,
Kubernetes/CRI-O packages, and node exporter; it never bootstraps a cluster.
Resources and private evidence remain available after the run. Reuse requires
restoring the disposable hosts and choosing an empty work directory.
Use --verify-existing for read-only probes and a final diff of an existing run.
"""

import argparse
import hashlib
import json
import os
import re
import secrets
import shlex
import subprocess
from pathlib import Path


ROOT = "/etc/richt-krg91"
ROLE = "richt_krg91"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--work-root", required=True, type=Path)
    parser.add_argument("--postgres-host", required=True)
    parser.add_argument("--kubernetes-host", action="append", required=True)
    parser.add_argument("--postgres-address", help="Database address reachable from the Kubernetes hosts; defaults to --postgres-host")
    parser.add_argument("--user", default="almalinux")
    parser.add_argument("--exporter-unit", default="node_exporter.service")
    parser.add_argument("--verify-existing", action="store_true", help="Verify existing private evidence and hosts without applying changes")
    args = parser.parse_args()
    targets = [args.postgres_host] + args.kubernetes_host
    if len(args.kubernetes_host) != 2 or len(set(targets)) != 3:
        parser.error("provide one PostgreSQL host and exactly two distinct --kubernetes-host targets")
    for value in targets + [args.user, args.postgres_address or args.postgres_host]:
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", value):
            parser.error("hosts, address, and user must be plain names or IPv4 addresses without options")
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.@-]*\.service", args.exporter_unit):
        parser.error("--exporter-unit must be a systemd service unit name")
    binary = args.binary.resolve(strict=True)
    work = args.work_root.resolve()
    if not args.verify_existing and work.exists() and (not work.is_dir() or any(work.iterdir())):
        parser.error("--work-root must be a new or empty directory")
    os.umask(0o077)
    source = work / "inventory.yaml"
    if args.verify_existing:
        if not work.is_dir() or work.stat().st_mode & 0o077:
            parser.error("--verify-existing requires an existing private work directory")
        for value in (source, work / "secrets.yaml"):
            if not value.is_file() or value.stat().st_mode & 0o077:
                parser.error("--verify-existing requires private inventory.yaml and secrets.yaml")
        try:
            inventory = json.loads(source.read_text())
            password = json.loads((work / "secrets.yaml").read_text())["database"]["password"]
            expected = {"postgres": args.postgres_host, **{f"kubernetes-{i}": host for i, host in enumerate(args.kubernetes_host, 1)}}
            actual = {host["name"]: host["transport"] for host in inventory["hosts"]}
            valid = len(inventory["hosts"]) == 3 and actual == {
                name: {"type": "ssh", "host": host, "user": args.user, "sudo": True} for name, host in expected.items()}
            valid = valid and inventory["services"]["node-exporter"]["processes"][0]["unit"] == args.exporter_unit
            secret_path = Path(inventory["secret_sources"]["default"]["file"])
            valid = valid and (work / secret_path).resolve() == (work / "secrets.yaml").resolve()
            valid = valid and isinstance(password, str) and bool(re.fullmatch(r"[a-f0-9]{64}", password))
            previous = json.loads((work / "acceptance.json").read_text()) if (work / "acceptance.json").is_file() else {}
            valid = valid and (args.postgres_address or args.postgres_host) == previous.get("postgres_address", args.postgres_host)
        except (KeyError, TypeError, ValueError, OSError):
            parser.error("existing evidence does not contain the expected workload inventory and secret")
        if not valid:
            parser.error("CLI hosts, user, exporter unit, or database address do not match existing workload evidence")
        services = inventory["services"]
    else:
        work.mkdir(parents=True, exist_ok=True, mode=0o700)
        work.chmod(0o700)
        password = secrets.token_hex(32)
    wrong_password = secrets.token_hex(32)
    prefix = "verify-command-" + secrets.token_hex(4) if args.verify_existing else "command"
    checks, commands = [], []
    report = {"status": "running", "targets": targets, "checks": checks,
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "exporter_unit": args.exporter_unit, "commands": commands,
              "postgres_address": args.postgres_address or args.postgres_host,
              "verify_existing": args.verify_existing}

    def private(path, value):
        path.write_text(value)
        path.chmod(0o600)
        return path

    def sanitized(value):
        return str(value).replace(password, "[redacted]").replace(wrong_password, "[redacted]")

    def run(argv, data=None, timeout=180):
        number = len(commands) + 1
        argv = [str(value) for value in argv]
        logfile = f"{prefix}-{number:03}.log"
        try:
            result = subprocess.run(argv, input=data, text=True, stdout=subprocess.PIPE,
                                    stderr=subprocess.STDOUT, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            output = error.stdout or ""
            if isinstance(output, bytes):
                output = output.decode(errors="replace")
            private(work / logfile, "$ " + shlex.join(argv) + "\n" + sanitized(output))
            commands.append({"log": logfile, "timed_out": True})
            raise AssertionError(f"command timed out; see {logfile}") from None
        private(work / logfile, "$ " + shlex.join(argv) + "\n" + sanitized(result.stdout))
        commands.append({"log": logfile, "exit_code": result.returncode})
        if any(value in result.stdout for value in (password, wrong_password)):
            raise AssertionError(f"credential appeared in output; sanitized evidence: {logfile}")
        if result.returncode:
            raise AssertionError(f"command failed ({result.returncode}); see {logfile}")
        return result.stdout

    def remote(host, script, data=None, sudo=True):
        command = (["sudo", "-n"] if sudo else []) + ["sh", "-c", script]
        return run(["ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                    "-o", "ConnectTimeout=10", "-l", args.user, "--", host,
                    shlex.join(command)], data=data)

    def check(name, condition):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def config(name, target, content, mode="0644", sensitive=False, postgres=False):
        template = private(work / (name + ".gotmpl"), content)
        value = {"path": target, "owner": "root", "group": "root", "mode": mode,
                 "template": {"file": str(template)}}
        if sensitive:
            value["sensitive"] = True
        if postgres:
            value["selinux"] = "system_u:object_r:postgresql_etc_t:s0"
        return value

    def rpm(*packages):
        return [{"name": name.replace("-", "_"), "type": "rpm", "package": name} for name in packages]

    def unit(name, text):
        result = config(name, "/etc/systemd/system/" + name + ".service", text)
        result.pop("path")
        return result

    def process(name, unit_file=None):
        result = {"type": "systemd", "unit": name + ".service", "state": "running", "timeout": "120s"}
        if unit_file:
            result["unit_file"] = unit_file
        return result

    def prepare(name):
        target = work / (name + "-plan.json")
        run([binary, "diff", "-i", source, "-o", target], timeout=300)
        check(name + " plan is private", target.stat().st_mode & 0o777 == 0o600)
        return target, json.loads(target.read_text())

    try:
        print("Artifacts: " + str(work), flush=True)
        if not args.verify_existing:
            identities = []
            for host in targets:
                check(host + " non-root SSH login", remote(host, "id -u", sudo=False).strip() != "0")
                identities.append(remote(host, "cat /etc/machine-id").strip())
                remote(host, "set -eu\ncat /etc/os-release\nuname -m\ngetenforce\n"
                       "test \"$(getenforce)\" = Enforcing\ndnf --version\nrpm --version\nsystemctl --version\n"
                       "command -v python3 curl ss\ndnf repolist --enabled\n"
                       "test ! -e " + ROOT + "\n"
                       "test ! -e /etc/yum.repos.d/richt-krg91-kubernetes.repo\n"
                       "test ! -e /etc/yum.repos.d/richt-krg91-crio.repo\n"
                       "test ! -e /etc/systemd/system/richt-krg91-pg-init.service\n"
                       "test ! -e /etc/systemd/system/richt-krg91-pg-bootstrap.service\n"
                       "test ! -e /etc/systemd/system/postgresql.service.d/90-richt-krg91.conf\n"
                       "if systemctl is-active --quiet firewalld; then echo 'Open the required ports in the disposable environment before running this suite'; exit 1; fi")
            check("three independent operating system instances", len(set(identities)) == 3)
            remote(args.postgres_host, "if test -e /var/lib/pgsql/data || systemctl is-active --quiet postgresql.service; then echo 'Fresh PostgreSQL target required: PGDATA exists or PostgreSQL is active'; exit 1; fi")

            sql = r"""SET password_encryption = 'scram-sha-256';
SELECT 'CREATE ROLE richt_krg91 LOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'richt_krg91') \gexec
ALTER ROLE richt_krg91 WITH LOGIN PASSWORD '{{secret "database.password"}}';
SELECT 'CREATE DATABASE richt_krg91 OWNER richt_krg91' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'richt_krg91') \gexec
"""
            pg_config = """data_directory = '/var/lib/pgsql/data'
listen_addresses = '0.0.0.0'
port = 5432
unix_socket_directories = '/var/run/postgresql'
hba_file = '/etc/richt-krg91/pg_hba.conf'
password_encryption = 'scram-sha-256'
"""
            init = unit("richt-krg91-pg-init", """[Unit]
Description=Initialize the disposable Richt PostgreSQL database
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c 'test -s /var/lib/pgsql/data/PG_VERSION || /usr/bin/postgresql-setup --initdb'
""")
            bootstrap = unit("richt-krg91-pg-bootstrap", """[Unit]
Description=Bootstrap the disposable Richt database role
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/sh -c '/usr/sbin/runuser -u postgres -- /usr/bin/psql -X -v ON_ERROR_STOP=1 < /etc/richt-krg91/bootstrap.sql'
""")
            pg = process("postgresql")
            pg["enabled"] = True
            override = config("postgresql-override", "unused", "[Service]\nExecStart=\nExecStart=/usr/bin/postgres -D /var/lib/pgsql/data -c config_file=/etc/richt-krg91/postgresql.conf\n")
            override.pop("path")
            pg["overrides"] = {"90-richt-krg91.conf": override}
            pg["restart_on"] = ["config.server", "config.hba"]
            repo = lambda section, url: f"[{section}]\nname=Richt KRG-91 {section}\nbaseurl={url}\nenabled=1\ngpgcheck=1\nrepo_gpgcheck=1\ngpgkey={url}repodata/repomd.xml.key\n"
            services = {
                "epel": {"order": 5, "binaries": rpm("epel-release")},
                "repositories": {"order": 10, "configs": [dict(config("kubernetes-repo", "/etc/yum.repos.d/richt-krg91-kubernetes.repo", repo("richt-krg91-kubernetes", "https://pkgs.k8s.io/core:/stable:/v1.36/rpm/")), name="kubernetes"), dict(config("crio-repo", "/etc/yum.repos.d/richt-krg91-crio.repo", repo("richt-krg91-crio", "https://download.opensuse.org/repositories/isv:/cri-o:/stable:/v1.36/rpm/")), name="crio")]},
                "kubernetes-node": {"order": 30, "binaries": rpm("kubeadm", "kubelet", "kubectl", "cri-o", "postgresql"),
                                    "processes": [{"name": "kubelet", "type": "systemd", "unit": "kubelet.service", "state": "stopped", "enabled": False}]},
                "postgresql": {"order": 40, "binaries": rpm("postgresql-server", "postgresql"), "configs": [dict(config("postgresql", ROOT + "/postgresql.conf", pg_config, postgres=True), name="server"), dict(config("pg-hba", ROOT + "/pg_hba.conf", "local all all peer\nhost all all 192.168.0.0/16 scram-sha-256\n", postgres=True), name="hba"), dict(config("bootstrap", ROOT + "/bootstrap.sql", sql, mode="0600", sensitive=True), name="bootstrap")],
                    "processes": [dict(process("richt-krg91-pg-init", unit_file=init), name="init"), dict(pg, name="database"), dict(process("richt-krg91-pg-bootstrap", unit_file=bootstrap), name="bootstrap")]},
                "node-exporter": {"order": 50, "binaries": rpm("node-exporter"),
                                  "processes": [{"name": "exporter", "type": "systemd", "unit": args.exporter_unit, "enabled": True, "state": "running", "timeout": "30s"}]}}
            secret = private(work / "secrets.yaml", json.dumps({"database": {"password": password}}))
            inventory = {"secret_sources": {"default": {"type": "yaml", "file": str(secret)}},
                         "tags": {"monitoring": {"order": 5, "services": {"epel": {}, "node-exporter": {}}},
                                  "kubernetes": {"order": 10, "services": {"repositories": {}, "kubernetes-node": {}}},
                                  "postgresql": {"order": 10, "services": {"postgresql": {}}}}, "services": services,
                         "hosts": [{"name": "postgres", "tags": ["postgresql", "monitoring"], "transport": {"type": "ssh", "host": args.postgres_host, "user": args.user, "sudo": True}}] + [
                             {"name": f"kubernetes-{i}", "tags": ["kubernetes", "monitoring"], "transport": {"type": "ssh", "host": host, "user": args.user, "sudo": True}}
                             for i, host in enumerate(args.kubernetes_host, 1)]}
            source = private(work / "inventory.yaml", json.dumps(inventory, indent=2) + "\n")
        resolved = json.loads(run([binary, "inventory", "show", "-i", source]))
        for host in resolved["hosts"]:
            orders = [service["order"] for service in host["services"]]
            check(host["name"] + " services sorted by definition order", orders == sorted(orders))
        monitored = json.loads(run([binary, "inventory", "show", "-i", source, "--tag", "monitoring"]))
        check("monitoring tag selects all three hosts", len(monitored["hosts"]) == 3)
        exporters = json.loads(run([binary, "inventory", "show", "-i", source, "--service", "node-exporter"]))
        check("service selector retains exactly exporter on every host", len(exporters["hosts"]) == 3 and all(
            [service["name"] for service in host["services"]] == ["node-exporter"] for host in exporters["hosts"]))
        if not args.verify_existing:
            target, prepared = prepare("first")
            for host in prepared["hosts"]:
                orders = [services[task["service"]]["order"] for task in host["tasks"]]
                check(host["name"] + " executable tasks follow service order", orders == sorted(orders))
            run([binary, "apply", "-y", "--parallelism", "3", target], timeout=1800)

        for host in targets:
            remote(host, "set -eu\nrpm -q epel-release node-exporter\nsystemctl is-active " + shlex.quote(args.exporter_unit) +
                   "\nsystemctl is-enabled " + shlex.quote(args.exporter_unit) + "\nsystemctl show -p Id -p Names -p FragmentPath " + shlex.quote(args.exporter_unit))
            metrics = remote(host, "curl --fail --silent --show-error --max-time 10 --write-out '\\nRICHT_HTTP_STATUS=%{http_code}\\n' http://127.0.0.1:9100/metrics")
            check(host + " exporter HTTP 200 and node_uname_info", metrics.splitlines()[-1:] == ["RICHT_HTTP_STATUS=200"] and
                  any(line.startswith("node_uname_info{") for line in metrics.splitlines()))
        for host in args.kubernetes_host:
            remote(host, "set -eu\nrpm -q kubeadm kubelet kubectl cri-o postgresql\n"
                   "test \"$(systemctl is-active kubelet.service || :)\" = inactive\n"
                   "test \"$(systemctl is-enabled kubelet.service || :)\" = disabled")
            check(host + " Kubernetes packages installed and kubelet stopped/disabled", True)
        settings = remote(args.postgres_host, "runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -Atc " + shlex.quote(
            "SELECT current_setting('listen_addresses'), current_setting('config_file'), current_setting('hba_file'), current_setting('password_encryption');"))
        check("PostgreSQL effective settings", settings.strip() == "0.0.0.0|/etc/richt-krg91/postgresql.conf|/etc/richt-krg91/pg_hba.conf|scram-sha-256")
        hba = remote(args.postgres_host, "runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -Atc " + shlex.quote(
            "SELECT type, address, netmask, auth_method, error IS NULL FROM pg_hba_file_rules ORDER BY line_number;"))
        check("PostgreSQL HBA uses peer and restricted SCRAM only", hba.splitlines() == ["local|||peer|t", "host|192.168.0.0|255.255.0.0|scram-sha-256|t"])
        stored = remote(args.postgres_host, "runuser -u postgres -- psql -X -v ON_ERROR_STOP=1 -Atc " + shlex.quote(
            "SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_authid WHERE rolname = 'richt_krg91';"))
        check("database role has SCRAM password hash", stored.strip() == "t")
        listener = remote(args.postgres_host, "ss -H -ltn4 'sport = :5432'")
        check("PostgreSQL listens on all IPv4 interfaces", "0.0.0.0:5432" in listener)
        probe = """import json, os, subprocess, sys
value = json.load(sys.stdin)
command = ['psql', '-X', '-h', value['host'], '-U', 'richt_krg91', '-d', 'richt_krg91', '-w', '-Atc', 'SELECT 1']
environment = dict(os.environ, PGCONNECT_TIMEOUT='10', PGPASSWORD=value['good'])
good = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=20)
environment['PGPASSWORD'] = value['bad']
bad = subprocess.run(command, env=environment, text=True, capture_output=True, timeout=20)
if good.returncode or good.stdout.strip() != '1' or not bad.returncode or 'password authentication failed' not in bad.stderr.lower():
    sys.exit('database authentication checks failed; verify connectivity, HBA, and credentials')
print('good password authenticated; wrong password rejected')
"""
        for host in args.kubernetes_host:
            result = remote(host, "python3 -c " + shlex.quote(probe), sudo=False, data=json.dumps(
                {"host": args.postgres_address or args.postgres_host, "good": password, "bad": wrong_password}))
            check(host + " database good/bad password checks", result.strip() == "good password authenticated; wrong password rejected")
        _, noop = prepare("verification-noop" if args.verify_existing else "final-noop")
        check("final diff has zero tasks on every host", len(noop["hosts"]) == 3 and all(not host["tasks"] for host in noop["hosts"]))
        report["status"] = "passed"
    except Exception as error:
        report["status"] = "failed"
        report["error"] = sanitized(error)
        raise
    finally:
        private(work / ("verification.json" if args.verify_existing else "acceptance.json"), json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
