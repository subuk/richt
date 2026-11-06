#!/usr/bin/env python3
"""Exercise HTTPS inventories and service catalogs on two disposable SSH hosts.

Run on the controller with Python 3.8+, OpenSSL, SSH authentication, and a Richt
binary built for the controller. Both targets must be authorized disposable
AlmaLinux hosts with noninteractive sudo and published DNF repositories. Only a
unique fixture root/unit and, when initially absent, the published tree RPM are
changed. Private plans and sanitized command logs remain in --work-root.
"""

import argparse
import copy
import hashlib
import http.server
import ipaddress
import json
import os
from pathlib import Path
import re
import shlex
import ssl
import subprocess
import tempfile
import threading
import uuid


def digest(data):
    return hashlib.sha256(data).hexdigest()


def encoded(value):
    return (json.dumps(value, indent=2) + "\n").encode()


def private_write(filename, data):
    if isinstance(data, str):
        data = data.encode()
    with os.fdopen(os.open(str(filename), os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600), "wb") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(data)


def without_provenance(value):
    if isinstance(value, dict):
        return {key: without_provenance(item) for key, item in value.items()
                if key not in ("source_file", "source_url")}
    if isinstance(value, list):
        return [without_provenance(item) for item in value]
    return value


def valid_host(value):
    try:
        ipaddress.ip_address(value)
        return True
    except ValueError:
        return re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", value) is not None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--host-a", required=True, help="First authorized disposable SSH alias or IP")
    parser.add_argument("--host-b", required=True, help="Second authorized disposable SSH alias or IP")
    parser.add_argument("--user", help="SSH user; omission preserves SSH client settings")
    parser.add_argument("--work-root", required=True, type=Path, help="Parent directory for retained private evidence")
    args = parser.parse_args()
    if not all(valid_host(host) for host in (args.host_a, args.host_b)):
        parser.error("targets must be SSH aliases or IPs without user@ or options")
    if args.host_a == args.host_b:
        parser.error("two distinct disposable targets are required")
    if args.user and not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_.-]*", args.user):
        parser.error("invalid SSH user")
    binary = args.binary.resolve()
    if not binary.is_file():
        parser.error("--binary must identify a controller executable")
    os.umask(0o077)
    args.work_root.mkdir(mode=0o700, parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="richt-sources-", dir=str(args.work_root.resolve())))
    work.chmod(0o700)
    fixture_id = uuid.uuid4().hex[:12]
    root = "/var/tmp/richt-sources-" + fixture_id
    unit = "richt-sources-" + fixture_id + ".service"
    aliases = {"A": args.host_a, "B": args.host_b}
    baseline, owned = {}, []
    checks, requests, cleanup_errors = [], [], []
    sequence = 0
    server = None
    server_thread = None
    request_lock = threading.Lock()
    documents, redirects = {}, {}
    token = "RICHT_SOURCE_TOKEN_" + uuid.uuid4().hex
    secret_parts = ["RICHT_SOURCE_SECRET_" + uuid.uuid4().hex for _ in range(2)]
    secret = "\n".join(secret_parts) + "\n"
    markers = [token] + secret_parts
    report = {"status": "running", "targets": ["A", "B"], "remote_root": root,
              "unit": unit, "checks": checks, "binary_sha256": digest(binary.read_bytes()),
              "baseline_tree": baseline, "cleanup_errors": cleanup_errors}

    def sanitize(text):
        for marker in markers:
            text = text.replace(marker, "<redacted>")
        for label, alias in aliases.items():
            text = re.sub(r"(?<![A-Za-z0-9_.:-])" + re.escape(alias) + r"(?![A-Za-z0-9_.:-])", label, text)
        return text

    def check(name, condition=True):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def run(argv, ok=True, timeout=180, env=None, allow_failure=False):
        nonlocal sequence
        argv = [str(item) for item in argv]
        sequence += 1
        logfile = work / ("command-%03d.log" % sequence)
        try:
            result = subprocess.run(argv, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                    timeout=timeout, env=env)
        except subprocess.TimeoutExpired as error:
            partial = error.stdout or b""
            if isinstance(partial, bytes):
                partial = partial.decode(errors="replace")
            private_write(logfile, "$ " + sanitize(shlex.join(argv)) + "\n" + sanitize(partial) + "\nTIMEOUT\n")
            raise AssertionError("command timed out; see " + logfile.name) from None
        private_write(logfile, "$ " + sanitize(shlex.join(argv)) + "\n" + sanitize(result.stdout))
        if any(marker in result.stdout for marker in markers):
            raise AssertionError("secret or source credential escaped in output; see redacted " + logfile.name)
        if not allow_failure and ((result.returncode == 0) != ok):
            raise AssertionError("unexpected command exit %d; see %s" % (result.returncode, logfile.name))
        return result

    def remote(label, script, sudo=True, allow_failure=False):
        command = ["sudo", "-n", "sh", "-c", script] if sudo else ["sh", "-c", script]
        ssh = ["ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"]
        if args.user:
            ssh += ["-l", args.user]
        return run(ssh + ["--", aliases[label], shlex.join(command)], allow_failure=allow_failure)

    def transport(label):
        value = {"type": "ssh", "host": aliases[label], "sudo": True}
        if args.user:
            value["user"] = args.user
        return value

    def hosts(tags=("fleet",)):
        return [{"name": label, "tags": list(tags) + ["cohort-" + label.lower()],
                 "transport": transport(label)} for label in aliases]

    def inventory(service, name="app"):
        return {"tags": {"fleet": {"services": {name: {}}},
                         "cohort-a": {"vars": {"label": "A"}}, "cohort-b": {"vars": {"label": "B"}}},
                "services": {name: service}, "hosts": hosts()}

    def write_inventory(name, value):
        filename = work / (name + ".yaml")
        private_write(filename, encoded(value))
        return filename

    client_file = work / "source-config.yaml"
    token_file = work / "bearer-token"
    secret_file = work / "secrets.yaml"
    cert, key = work / "ca.pem", work / "key.pem"

    def source_args(location, pin=None):
        argv = ["-i", location, "--source-config", client_file]
        if pin is not None:
            argv += ["--inventory-sha256", pin]
        return argv

    def prepare(name, location, pin=None, ok=True):
        saved = work / (name + "-plan.json")
        result = run([binary, "diff", "--progress", "off"] + source_args(location, pin) + ["-o", saved], ok=ok)
        if not ok:
            check(name + " leaves no executable plan", not saved.exists())
            return saved, None, result
        check(name + " saves a private executable plan", saved.stat().st_mode & 0o777 == 0o600)
        text = saved.read_text()
        check(name + " excludes HTTPS authentication", token not in text and "Authorization" not in text)
        return saved, json.loads(text), result

    def apply(name, saved, ok=True):
        result = run([binary, "apply", "--progress", "off", "-y", "--parallelism", "2", saved], ok=ok, timeout=600)
        private_write(work / (name + ".apply.log"), sanitize(result.stdout))
        return result

    def statuses(saved):
        return {host["name"]: [task["progress"]["status"] for task in host["tasks"]]
                for host in json.loads(saved.read_text())["hosts"]}

    def snapshot_requests():
        with request_lock:
            return list(requests)

    def stop_server():
        nonlocal server, server_thread
        if server is not None:
            server.shutdown()
            server.server_close()
            if server_thread:
                server_thread.join(timeout=5)
            server = None
            server_thread = None

    unit_body = ("[Unit]\nDescription=Richt HTTPS source acceptance\n[Service]\n"
                 "Type=simple\nExecStart=/usr/bin/sleep infinity\n[Install]\nWantedBy=multi-user.target\n")
    dropin_body = "[Service]\nEnvironment=RICHT_SOURCE_ACCEPTANCE=yes\n"
    app_service = None

    def cleanup():
        # These inventories are local and contain only inline templates. Cleanup
        # never depends on the HTTPS fixture, credential file, or secret source.
        for label in owned:
            try:
                definition = {"order": 10,
                              "configs": [{"name": "main", "path": root + "/app.conf", "sensitive": True,
                                           "template": {"inline": "cleanup\n"}}],
                              "processes": [{"type": "systemd", "unit": unit,
                                             "unit_file": {"template": {"inline": unit_body}},
                                             "overrides": {"20-source.conf": {"template": {"inline": dropin_body}}}}]}
                if baseline.get(label) == "absent":
                    definition["binaries"] = [{"type": "rpm", "package": "tree"}]
                value = {"tags": {"cleanup": {"services": {"app": {"state": "absent"}}}},
                         "services": {"app": definition},
                         "hosts": [{"name": label, "tags": ["cleanup"], "transport": transport(label)}]}
                local = write_inventory("cleanup-" + label, value)
                saved = work / ("cleanup-" + label + "-plan.json")
                run([binary, "diff", "--progress", "off", "-i", local, "-o", saved])
                apply("cleanup-" + label, saved)
            except Exception as error:
                cleanup_errors.append(label + " local absent cleanup: " + sanitize(str(error)))
            try:
                # The fallback only removes paths whose absence was checked
                # before this suite claimed the unique fixture namespace.
                script = ("set -eu\nsystemctl stop " + shlex.quote(unit) + " >/dev/null 2>&1 || :\n"
                          "systemctl disable " + shlex.quote(unit) + " >/dev/null 2>&1 || :\n"
                          "rm -f -- " + shlex.quote("/etc/systemd/system/" + unit) + "\n"
                          "rm -rf -- " + shlex.quote("/etc/systemd/system/" + unit + ".d") + " " + shlex.quote(root) + "\n"
                          "systemctl daemon-reload\n")
                if baseline.get(label) == "absent":
                    script += ("if rpm -q tree >/dev/null 2>&1; then "
                               "dnf -y --setopt=clean_requirements_on_remove=False remove tree; fi\n")
                script += ("test ! -e " + shlex.quote(root) + "\n"
                           "test ! -e " + shlex.quote("/etc/systemd/system/" + unit) + "\n"
                           "test ! -e " + shlex.quote("/etc/systemd/system/" + unit + ".d") + "\n"
                           "if rpm -q tree >/dev/null 2>&1; then printf present; else printf absent; fi\n")
                result = remote(label, script)
                if not result.stdout.rstrip().endswith(baseline[label]):
                    raise AssertionError("tree package baseline was not restored")
                report.setdefault("cleanup_restored", []).append(label)
            except Exception as error:
                cleanup_errors.append(label + " fixture cleanup: " + sanitize(str(error)))

    try:
        print("Artifacts: " + str(work), flush=True)
        for label in aliases:
            platform = remote(label, "set -eu\nid\nuname -a\ncat /etc/os-release\ngetenforce\ncommand -v dnf\ncommand -v rpm\ncommand -v systemctl\ncommand -v python3\n")
            private_write(work / ("platform-" + label + ".log"), sanitize(platform.stdout))
            unique = remote(label, "test ! -e " + shlex.quote(root) + " && test ! -e " +
                            shlex.quote("/etc/systemd/system/" + unit) + " && test ! -e " +
                            shlex.quote("/etc/systemd/system/" + unit + ".d") + " && test ! -L " +
                            shlex.quote(root) + " && test ! -L " + shlex.quote("/etc/systemd/system/" + unit) +
                            " && test ! -L " + shlex.quote("/etc/systemd/system/" + unit + ".d") +
                            " && ! systemctl cat " + shlex.quote(unit) + " >/dev/null 2>&1", allow_failure=True)
            check(label + " fixture namespace is unused", unique.returncode == 0)
            baseline[label] = remote(label, "if rpm -q tree >/dev/null 2>&1; then printf present; else printf absent; fi").stdout.strip()
            check(label + " package baseline recorded", baseline[label] in ("present", "absent"))
            owned.append(label)
            remote(label, "mkdir -m 0700 -- " + shlex.quote(root))

        openssl_config = work / "openssl.conf"
        private_write(openssl_config, "[req]\ndistinguished_name=dn\nx509_extensions=ext\nprompt=no\n"
                      "[dn]\nCN=localhost\n[ext]\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n"
                      "basicConstraints=critical,CA:TRUE\nkeyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\n")
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-keyout", key,
             "-out", cert, "-config", openssl_config])
        key.chmod(0o600)

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_):
                pass

            def do_GET(self):
                authorized = self.headers.get("Authorization") == "Bearer " + token
                with request_lock:
                    requests.append({"path": self.path, "authorized": authorized})
                if not authorized:
                    self.send_response(401)
                    self.end_headers()
                    return
                if self.path in redirects:
                    self.send_response(302)
                    self.send_header("Location", redirects[self.path])
                    self.end_headers()
                    return
                data = documents.get(self.path)
                if data is None:
                    self.send_response(404)
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                try:
                    self.wfile.write(data)
                except (BrokenPipeError, ConnectionResetError, ssl.SSLError):
                    pass

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(str(cert), str(key))
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        server_thread = threading.Thread(target=server.serve_forever, daemon=True)
        server_thread.start()
        base = "https://localhost:" + str(server.server_port)
        private_write(token_file, token + "\n")
        private_write(secret_file, encoded({"database": {"password": secret}}))
        private_write(client_file, encoded({"ca_file": cert.name,
                      "auth": [{"origin": base, "path_prefix": "/", "bearer": {"file": token_file.name}}],
                      "secret_sources": {"default": {"type": "yaml", "file": secret_file.name}}}))

        # KRG-80: a standalone HTTPS inventory with inline service definitions.
        inline = inventory({"order": 5, "configs": [{"path": root + "/inline", "mode": "0644",
                           "template": {"inline": "inline host={{.label}}\n"}}]}, name="inline")
        documents["/standalone.yaml"] = encoded(inline)
        local_inline = write_inventory("standalone-local", inline)
        remote_inline = run([binary, "inventory", "show"] + source_args(base + "/standalone.yaml", digest(documents["/standalone.yaml"])))
        local_result = run([binary, "inventory", "show"] + source_args(local_inline))
        check("HTTPS root and local root resolve equivalent assignments", without_provenance(json.loads(remote_inline.stdout)) == without_provenance(json.loads(local_result.stdout)))
        for flags in (["--host", "A"], ["--tag", "cohort-a"], ["--service", "inline", "--host", "A"]):
            shown = run([binary, "inventory", "show"] + source_args(base + "/standalone.yaml") + flags)
            check("HTTPS selector " + " ".join(flags), [host["name"] for host in json.loads(shown.stdout)["hosts"]] == ["A"])
        saved, plan, _ = prepare("standalone", base + "/standalone.yaml", digest(documents["/standalone.yaml"]))
        check("standalone root prepares both hosts", [host["name"] for host in plan["hosts"]] == ["A", "B"])
        apply("standalone", saved)
        for label in aliases:
            check(label + " inline HTTPS root applies", remote(label, "cat -- " + shlex.quote(root + "/inline")).stdout == "inline host=" + label + "\n")

        # KRG-81: redirects establish the bases for relative catalog/templates.
        config_body = 'host={{.label}}\npassword={{secret "database.password"}}\n'
        documents["/releases/templates/app.gotmpl"] = config_body.encode()
        documents["/releases/templates/unit.gotmpl"] = unit_body.encode()
        documents["/releases/templates/dropin.gotmpl"] = dropin_body.encode()

        def template(name):
            return {"url": "../templates/" + name + ".gotmpl", "sha256": digest(documents["/releases/templates/" + name + ".gotmpl"])}

        def publish_app():
            nonlocal app_service
            app_service = {"order": 10, "binaries": [{"name": "tree", "type": "rpm", "package": "tree"}],
                           "configs": [{"name": "main", "path": root + "/app.conf", "mode": "0600", "owner": "root", "group": "root", "template": template("app")}],
                           "processes": [{"type": "systemd", "unit": unit, "state": "running", "enabled": True,
                                          "restart_on": ["config.main"], "unit_file": {"template": template("unit")},
                                          "overrides": {"20-source.conf": {"template": template("dropin")}}}]}
            documents["/releases/services/app.yaml"] = encoded(app_service)
            reference = {"url": "../catalog/app.yaml", "sha256": digest(documents["/releases/services/app.yaml"])}
            documents["/tenant/inventory.yaml"] = encoded(inventory(reference))
            redirects["/catalog/app.yaml"] = "/releases/services/app.yaml"
            redirects["/entry.yaml"] = "/tenant/inventory.yaml"
            return digest(documents["/tenant/inventory.yaml"])

        app_pin = publish_app()
        catalog_local = inventory({"url": base + "/catalog/app.yaml", "sha256": digest(documents["/releases/services/app.yaml"])})
        local_catalog = write_inventory("catalog-local", catalog_local)
        remote_shown = run([binary, "inventory", "show"] + source_args(base + "/entry.yaml", app_pin))
        local_shown = run([binary, "inventory", "show"] + source_args(local_catalog))
        check("local inventory and HTTPS root share the same catalog", without_provenance(json.loads(remote_shown.stdout)) == without_provenance(json.loads(local_shown.stdout)))

        # An SSH trap and deliberately unresolvable target prove inspection
        # does not invoke SSH, open templates, or read bound secret values.
        trap = work / "ssh-trap"
        trap.mkdir(mode=0o700)
        trap_marker = work / "unexpected-ssh"
        private_write(trap / "ssh", "#!/bin/sh\nprintf called >> " + shlex.quote(str(trap_marker)) + "\nexit 99\n")
        (trap / "ssh").chmod(0o700)
        inspection = inventory({"url": "../catalog/app.yaml", "sha256": digest(documents["/releases/services/app.yaml"])})
        for host in inspection["hosts"]:
            host["transport"]["host"] = "unresolvable-sources.invalid"
        documents["/tenant/inspection.yaml"] = encoded(inspection)
        before = len(snapshot_requests())
        secret_file.rename(work / "secrets-not-available.yaml")
        try:
            env = dict(os.environ, PATH=str(trap) + os.pathsep + os.environ.get("PATH", ""))
            for command in ("validate", "show"):
                run([binary, "inventory", command] + source_args(base + "/tenant/inspection.yaml"), env=env)
        finally:
            (work / "secrets-not-available.yaml").rename(secret_file)
        inspected_paths = [request["path"] for request in snapshot_requests()[before:]]
        check("inspection fetches only inventory/catalog without SSH or templates", not trap_marker.exists() and inspected_paths and not any("templates/" in path for path in inspected_paths))

        before = len(snapshot_requests())
        saved, plan, output = prepare("catalog-install", base + "/entry.yaml", app_pin)
        fetched = [request["path"] for request in snapshot_requests()[before:]]
        check("two hosts share one template snapshot per source", all(fetched.count("/releases/templates/" + name + ".gotmpl") == 1 for name in ("app", "unit", "dropin")))
        check("secret rendering is retained and marked sensitive", all(any(
            task.get("file", {}).get("content") == "host=" + host["name"] + "\npassword=" + secret + "\n" and task["file"].get("sensitive")
            for task in host["tasks"]) for host in plan["hosts"]))
        check("package observation respects each baseline", all(
            any(task["operation"] == "rpm" for task in host["tasks"]) == (baseline[host["name"]] == "absent")
            for host in plan["hosts"]))
        check("sensitive diff hides both secret canaries", all(marker not in output.stdout for marker in secret_parts))
        apply("catalog-install", saved)

        def verify_app(revision=""):
            for label in aliases:
                expected = "host=" + label + "\npassword=" + secret + "\n" + revision
                result = remote(label, "set -eu\nrpm -q tree >/dev/null\nsystemctl is-active " + shlex.quote(unit) +
                                "\nsystemctl is-enabled " + shlex.quote(unit) + "\nstat -c '%a' -- " + shlex.quote(root + "/app.conf") +
                                "\nsha256sum -- " + shlex.quote(root + "/app.conf") + "\ntest -f " +
                                shlex.quote("/etc/systemd/system/" + unit + ".d/20-source.conf"))
                lines = result.stdout.splitlines()
                check(label + " package/config/unit/drop-in converge" + (" after frozen apply" if revision else ""), len(lines) == 4 and lines[:3] == ["active", "enabled", "600"] and lines[3].split()[0] == digest(expected.encode()))

        verify_app()
        for name, location, pin in (("catalog-noop", base + "/entry.yaml", app_pin), ("local-catalog-noop", local_catalog, None)):
            _, noop, _ = prepare(name, location, pin)
            check(name + " has no tasks on either host", all(not host["tasks"] for host in noop["hosts"]))

        # Root, service, and template integrity failures cannot change targets.
        prepare("root-sha-mismatch", base + "/entry.yaml", "0" * 64, ok=False)
        bad_service = inventory({"url": "../catalog/app.yaml", "sha256": "0" * 64})
        documents["/tenant/bad-service.yaml"] = encoded(bad_service)
        prepare("service-sha-mismatch", base + "/tenant/bad-service.yaml", digest(documents["/tenant/bad-service.yaml"]), ok=False)
        bad_definition = copy.deepcopy(app_service)
        bad_definition["configs"][0]["path"] = root + "/must-not-exist"
        bad_definition["configs"][0]["template"]["sha256"] = "0" * 64
        documents["/releases/services/bad.yaml"] = encoded(bad_definition)
        documents["/tenant/bad-template.yaml"] = encoded(inventory({"url": "../releases/services/bad.yaml", "sha256": digest(documents["/releases/services/bad.yaml"])}))
        prepare("template-sha-mismatch", base + "/tenant/bad-template.yaml", digest(documents["/tenant/bad-template.yaml"]), ok=False)
        for label in aliases:
            check(label + " checksum failures make no target writes", remote(label, "test ! -e " + shlex.quote(root + "/must-not-exist"), allow_failure=True).returncode == 0)
        verify_app()

        documents["/releases/templates/app.gotmpl"] = (config_body + "revision=two\n").encode()
        frozen_pin = publish_app()
        frozen, frozen_plan, _ = prepare("frozen-offline", base + "/entry.yaml", frozen_pin)
        check("frozen plan contains actual secret bytes", all(marker in frozen.read_text() for marker in secret_parts))

        progress_configs = []
        for name, destination in (("first", "progress-first"), ("second", "progress-parent/second"), ("third", "progress-third")):
            path = "/releases/templates/progress-" + name + ".gotmpl"
            documents[path] = (name + " frozen\n").encode()
            progress_configs.append({"name": name, "path": root + "/" + destination,
                                     "template": {"url": "../templates/progress-" + name + ".gotmpl", "sha256": digest(documents[path])}})
        documents["/releases/services/progress.yaml"] = encoded({"order": 10, "configs": progress_configs})
        documents["/tenant/progress.yaml"] = encoded(inventory({"url": "../releases/services/progress.yaml", "sha256": digest(documents["/releases/services/progress.yaml"])}))
        progress, progress_plan, _ = prepare("partial-progress", base + "/tenant/progress.yaml", digest(documents["/tenant/progress.yaml"]))
        check("continuation plan freezes three file tasks per host", all(len(host["tasks"]) == 3 and all(task["operation"] == "file" for task in host["tasks"]) for host in progress_plan["hosts"]))
        remote("A", "printf blocker > " + shlex.quote(root + "/progress-parent"))

        stop_server()
        for filename in (client_file, token_file, secret_file, cert, key):
            filename.unlink()
        check("HTTPS server and local client/secret inputs are unavailable", server is None and not any(filename.exists() for filename in (client_file, token_file, secret_file, cert, key)))
        requests_at_stop = len(snapshot_requests())
        apply("frozen-offline", frozen)
        verify_app("revision=two\n")
        apply("partial-failure", progress, ok=False)
        progress_status = statuses(progress)
        check("one host failure blocks its later task while the peer completes", progress_status["A"] == ["success", "error", "pending"] and progress_status["B"] == ["success", "success", "success"])
        remote("A", "set -eu\nrm -- " + shlex.quote(root + "/progress-parent") +
               "\nprintf 'external change\\n' > " + shlex.quote(root + "/progress-first"))
        resumed = apply("partial-resume", progress)
        check("explicit continuation skips completed tasks", "previously completed" in resumed.stdout and remote("A", "cat -- " + shlex.quote(root + "/progress-first")).stdout == "external change\n" and all(status == "success" for values in statuses(progress).values() for status in values))
        for label in aliases:
            check(label + " unfinished tasks use frozen bytes", remote(label, "cat -- " + shlex.quote(root + "/progress-parent/second") + " " + shlex.quote(root + "/progress-third")).stdout == "second frozen\nthird frozen\n")
        check("saved apply and continuation need no HTTPS access", len(snapshot_requests()) == requests_at_stop)
        check("all HTTPS fixture requests used scoped authentication", all(request["authorized"] for request in snapshot_requests()))
        report["status"] = "passed"
    except Exception as error:
        report["status"] = "failed"
        report["error"] = sanitize(str(error))
        raise
    finally:
        stop_server()
        cleanup()
        # Credentials are ephemeral even when a failure occurs before offline
        # apply. Saved plans intentionally retain synthetic secret-bearing data.
        for filename in (client_file, token_file, secret_file, work / "secrets-not-available.yaml", key):
            if filename.exists():
                filename.unlink()
        report["count"] = len(checks)
        report["https_requests"] = len(snapshot_requests())
        report["commands"] = sequence
        if cleanup_errors:
            report["status"] = "failed"
        private_write(work / "https-requests.json", encoded(snapshot_requests()))
        private_write(work / "source-manifest.json", encoded({path: digest(data) for path, data in documents.items()}))
        private_write(work / "source-documents.json", encoded({path: data.decode() for path, data in documents.items()}))
        private_write(work / "acceptance.json", encoded(report))
        print("Acceptance report: " + str(work / "acceptance.json"), flush=True)
        print(json.dumps(report, indent=2), flush=True)
    if cleanup_errors:
        raise AssertionError("fixture cleanup failed; inspect acceptance.json")


if __name__ == "__main__":
    main()
