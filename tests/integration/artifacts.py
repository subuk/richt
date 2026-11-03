#!/usr/bin/env python3
"""Exercise artifact CLI operations on an authorized disposable Linux host.

Run locally as root with Python 3.8+, OpenSSL and a Linux Richt binary.
A private HTTPS server and CA exercise verified TLS without disabling checks.
All managed destinations are unique; no package or existing service is touched.
With --work-root, private plans/logs and the acceptance report are retained.
"""

import argparse
import hashlib
import http.server
import io
import json
import os
from pathlib import Path
import shutil
import ssl
import subprocess
import tarfile
import tempfile
import threading
import time
import urllib.parse


def digest(data):
    return hashlib.sha256(data).hexdigest()


def archive(entries):
    stream = io.BytesIO()
    with tarfile.open(fileobj=stream, mode="w:gz") as bundle:
        for name, contents, kind in entries:
            item = tarfile.TarInfo(name)
            item.mode = 0o755 if name.endswith("/app") else 0o644
            if kind == "symlink":
                item.type = tarfile.SYMTYPE
                item.linkname = contents
                bundle.addfile(item)
            elif kind == "hardlink":
                item.type = tarfile.LNKTYPE
                item.linkname = contents
                bundle.addfile(item)
            elif kind == "fifo":
                item.type = tarfile.FIFOTYPE
                bundle.addfile(item)
            else:
                data = contents.encode()
                item.size = len(data)
                bundle.addfile(item, io.BytesIO(data))
    return stream.getvalue()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--work-root", type=Path)
    args = parser.parse_args()
    if os.geteuid() != 0 or os.uname().sysname != "Linux":
        parser.error("run as root on an authorized disposable Linux target")
    binary = str(args.binary.resolve())
    if args.work_root:
        args.work_root.mkdir(mode=0o700, parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="richt-artifacts-", dir=str(args.work_root) if args.work_root else None))
    work.chmod(0o700)
    target = work / "targets"
    target.mkdir(mode=0o700)
    checks, requests = [], []
    sequence = 0
    script1 = b'#!/bin/sh\nprintf "v1\\n" >> "$1"\n'
    script2 = b'#!/bin/sh\nprintf "v2\\n" >> "$1"\n'
    files = {
        "/v1": script1, "/v2": script2,
        "/tree1": archive([("dist/app", "one", "file"), ("dist/readme", "readme", "file"), ("dist/link", "app", "symlink")]),
        "/tree2": archive([("dist/app", "two", "file"), ("dist/readme", "new", "file")]),
    }
    for name, entries in {
        "partial-extraction": [("good", "staged", "file"), ("../escaped", "bad", "file")],
        "traversal": [("../escaped", "bad", "file")],
        "absolute": [("/escaped", "bad", "file")],
        "symlink-escape": [("link", "../../escaped", "symlink")],
        "symlink-parent": [("link", "real", "symlink"), ("link/payload", "bad", "file")],
        "hardlink": [("hard", "outside", "hardlink")],
        "fifo": [("fifo", "", "fifo")],
        "duplicate": [("same", "first", "file"), ("same", "second", "file")],
    }.items():
        files["/" + name] = archive(entries)

    files["/corrupt"] = b"this is not a gzip stream"

    class Handler(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_GET(self):
            requests.append(self.path)
            if self.path in ["/redirect", "/redirect-signed", "/downgrade", "/loop"]:
                self.send_response(302)
                destination = "/v1" if self.path == "/redirect" else "/v1?signature=fixture" if self.path == "/redirect-signed" else "http://localhost/forbidden"
                self.send_header("Location", "/loop" if self.path == "/loop" else destination)
                self.end_headers()
                return
            data = files.get(urllib.parse.urlsplit(self.path).path)
            if data is None:
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Length", str(len(data) + (100 if self.path == "/interrupted" else 0)))
            self.end_headers()
            if self.path == "/slow":
                time.sleep(2)
            try:
                self.wfile.write(data)
            except (BrokenPipeError, ConnectionResetError, ssl.SSLError):
                pass
            self.close_connection = True

    cert, key = work / "ca.pem", work / "key.pem"
    config = work / "openssl.conf"
    config.write_text("[req]\ndistinguished_name=dn\nx509_extensions=ext\nprompt=no\n"
                      "[dn]\nCN=localhost\n[ext]\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n"
                      "basicConstraints=critical,CA:TRUE\nkeyUsage=critical,digitalSignature,keyEncipherment,keyCertSign\n")
    subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1",
                    "-keyout", str(key), "-out", str(cert), "-config", str(config)],
                   check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    key.chmod(0o600)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(str(cert), str(key))
    server.socket = context.wrap_socket(server.socket, server_side=True)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    base_url = "https://localhost:" + str(server.server_port)
    environment = dict(os.environ, SSL_CERT_FILE=str(cert))

    def run(argv, ok=True, trust=True):
        nonlocal sequence
        env = environment if trust else dict(os.environ, SSL_CERT_FILE=str(work / "missing-ca"))
        result = subprocess.run([binary] + [str(arg) for arg in argv], text=True, capture_output=True,
                                env=env, timeout=45)
        sequence += 1
        (work / ("command-%03d.log" % sequence)).write_text(result.stdout + result.stderr)
        if (result.returncode == 0) != ok:
            raise AssertionError("unexpected CLI exit: " + str(argv) + "\n" + result.stdout + result.stderr)
        return result.stdout + result.stderr

    def check(name, condition=True):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def component(endpoint="/v1", path="app", kind="download", checksum=None):
        result = {"name": "artifact", "type": kind, "url": base_url + endpoint,
                  "sha256": checksum or digest(files.get(endpoint, script1)), "path": str(target / path),
                  "owner": "root", "group": "root", "timeout": "5s"}
        if kind == "archive":
            result.update(format="tar.gz", strip_components=1 if endpoint.startswith("/tree") else 0)
        return result

    def inventory(item, state="present", reaction=False):
        service = {"order": 10, "binaries": [item]}
        if reaction:
            service["processes"] = [{"type": "oneshot", "command": ["/bin/sh", str(target / "app"), str(target / "events")],
                                     "run_on": ["binary.artifact"]}]
        return {"tags": {"test": {"services": {"test": {"state": state}}}}, "services": {"test": service},
                "hosts": [{"name": "target", "tags": ["test"], "transport": {"type": "local"}}]}

    def prepare(name, item, state="present", reaction=False, ok=True):
        inv = work / (name + ".yaml")
        inv.write_text(json.dumps(inventory(item, state, reaction), indent=2) + "\n")
        saved = work / (name + ".json")
        output = run(["diff", "-i", inv, "-o", saved], ok)
        return inv, saved, output

    def tasks(saved):
        return json.loads(saved.read_text())["hosts"][0]["tasks"]

    def apply(saved, ok=True, trust=True):
        return run(["apply", "-y", saved], ok, trust)

    try:
        (work / "platform.log").write_text(subprocess.check_output(
            ["sh", "-c", "id; uname -a; cat /etc/os-release; python3 --version; getenforce"], text=True))
        item = component()
        inv, saved, _ = prepare("install", item, reaction=True)
        run(["inventory", "validate", "-i", inv])
        run(["inventory", "show", "-i", inv])
        check("inspection and preparation make no HTTPS request or managed file", requests == [] and not (target / "app").exists())
        text = saved.read_text()
        check("plan freezes URL/checksum and excludes binary bytes", item["sha256"] in text and base_url in text and script1.decode() not in text)
        inv.unlink()
        apply(saved)
        check("frozen download installs executable and triggers oneshot", (target / "app").read_bytes() == script1 and (target / "app").stat().st_mode & 0o777 == 0o755 and (target / "events").read_text() == "v1\n")
        before = len(requests)
        apply(saved)
        check("saved successful tasks skip without fetch or reaction", len(requests) == before and (target / "events").read_text() == "v1\n")
        _, noop, _ = prepare("noop", item, reaction=True)
        check("matching download is no-op", tasks(noop) == [])
        for label, endpoint in [("upgrade", "/v2"), ("downgrade", "/v1")]:
            item = component(endpoint)
            _, saved, _ = prepare(label, item, reaction=True)
            apply(saved)
            check(label + " publishes verified bytes and executes reaction", (target / "app").read_bytes() == files[endpoint])
        check("artifact changes coalesce into one reaction each", (target / "events").read_text() == "v1\nv2\nv1\n")
        (target / "app").write_bytes(b"drift")
        _, saved, _ = prepare("repair", item)
        apply(saved)
        check("tracked byte drift repairs", (target / "app").read_bytes() == script1)
        item["mode"] = "0700"
        _, saved, _ = prepare("attributes", item)
        apply(saved)
        check("mode-only update converges", (target / "app").stat().st_mode & 0o777 == 0o700)
        _, saved, _ = prepare("checksum", component("/v2", checksum="0" * 64))
        apply(saved, False)
        check("checksum failure preserves published bytes", (target / "app").read_bytes() == script1)
        files["/interrupted"] = script2[:10]
        files["/slow"] = script2
        for label, endpoint, expected in [("interrupted", "/interrupted", script2), ("timeout", "/slow", script2)]:
            failed = component(endpoint, checksum=digest(expected))
            if label == "timeout":
                failed["timeout"] = "300ms"
            _, saved, _ = prepare(label, failed)
            apply(saved, False)
            check(label + " leaves old installation complete", (target / "app").read_bytes() == script1)
        _, saved, _ = prepare("tls", component("/v2"))
        apply(saved, False, trust=False)
        check("untrusted TLS is rejected before publication", (target / "app").read_bytes() == script1)
        for endpoint in ["/downgrade", "/loop"]:
            _, saved, _ = prepare("redirect-" + endpoint[1:], component(endpoint))
            apply(saved, False)
            check(endpoint[1:] + " redirect rejected", (target / "app").read_bytes() == script1)
        _, saved, _ = prepare("redirect-valid", component("/redirect"))
        apply(saved)
        check("verified HTTPS redirect succeeds", (target / "app").read_bytes() == script1)
        _, saved, _ = prepare("redirect-signed", component("/redirect-signed", path="signed-redirect"))
        apply(saved)
        check("transient signed HTTPS redirect succeeds without persisting query",
              (target / "signed-redirect").read_bytes() == script1 and "signature=fixture" not in saved.read_text())
        files["/resume"] = b"broken"
        frozen = component("/resume", path="resume", checksum=digest(script2))
        data = inventory(frozen)
        prior = component(path="prior")
        prior["name"] = "prior"
        data["services"]["test"]["binaries"].insert(0, prior)
        data["services"]["test"]["configs"] = [{"path": str(target / "after"), "mode": "0600",
                                                   "template": {"inline": "frozen later config\n"}}]
        inv, saved = work / "continuation.yaml", work / "continuation.json"
        inv.write_text(json.dumps(data, indent=2) + "\n")
        run(["diff", "-i", inv, "-o", saved])
        apply(saved, False)
        check("failed download records progress and blocks later tasks",
              [task["progress"]["status"] for task in tasks(saved)] == ["success", "error", "pending"]
              and (target / "prior").read_bytes() == script1 and not (target / "resume").exists()
              and not (target / "after").exists())
        (target / "prior").write_bytes(b"edited after successful task")
        inv.unlink()
        files["/resume"] = script2
        apply(saved)
        check("continuation uses frozen inputs and skips earlier success after external repair",
              (target / "resume").read_bytes() == script2 and (target / "prior").read_bytes() == b"edited after successful task"
              and (target / "after").read_text() == "frozen later config\n"
              and all(task["progress"]["status"] == "success" for task in tasks(saved)))
        tree = component("/tree1", "tree", "archive")
        _, saved, _ = prepare("archive", tree)
        apply(saved)
        check("archive strips directory and preserves safe symlink", (target / "tree/app").read_text() == "one" and os.readlink(target / "tree/link") == "app" and (target / "tree/app").stat().st_mode & 0o777 == 0o755)
        _, saved, _ = prepare("archive-noop", tree)
        check("matching archive is no-op", tasks(saved) == [])
        (target / "tree/readme").write_text("drift")
        _, saved, _ = prepare("archive-repair", tree)
        apply(saved)
        check("tracked archive entry drift repairs", (target / "tree/readme").read_text() == "readme")
        (target / "tree/foreign").write_text("preserve")
        prepare("foreign-update", component("/tree2", "tree", "archive"), ok=False)
        prepare("foreign-remove", tree, state="absent", ok=False)
        check("unknown archive entry blocks update and removal", (target / "tree/foreign").read_text() == "preserve")
        (target / "tree/foreign").unlink()
        for label, endpoint in [("archive-upgrade", "/tree2"), ("archive-downgrade", "/tree1")]:
            tree = component(endpoint, "tree", "archive")
            _, saved, _ = prepare(label, tree)
            apply(saved)
            check(label + " publishes complete tree", (target / "tree/app").read_text() == ("two" if endpoint == "/tree2" else "one"))
        _, saved, _ = prepare("archive-bad-update", component("/partial-extraction", "tree", "archive"))
        apply(saved, False)
        check("unsafe archive update preserves complete published tree", (target / "tree/app").read_text() == "one")
        for attack in ["corrupt", "partial-extraction", "traversal", "absolute", "symlink-escape", "symlink-parent", "hardlink", "fifo", "duplicate"]:
            _, saved, _ = prepare(attack, component("/" + attack, "attack-" + attack, "archive"))
            apply(saved, False)
            check(attack + " archive rejected before publication", not (target / ("attack-" + attack)).exists() and not (work / "escaped").exists())
        (target / "foreign-file").write_bytes(script1)
        (target / "foreign-tree").mkdir()
        prepare("adopt-file", component(path="foreign-file"), ok=False)
        prepare("adopt-tree", component("/tree1", "foreign-tree", "archive"), ok=False)
        check("matching foreign file and empty tree are never adopted", (target / "foreign-file").read_bytes() == script1 and (target / "foreign-tree").is_dir())
        for alias_kind in ["symlink", "bind"]:
            alias = work / ("alias-" + alias_kind)
            if alias_kind == "symlink":
                alias.symlink_to(target, target_is_directory=True)
            else:
                alias.mkdir()
                subprocess.run(["mount", "--bind", str(target), str(alias)], check=True)
            try:
                data = inventory(tree)
                data["services"]["config"] = {"order": 20, "configs": [{"path": str(alias / "tree/readme"),
                    "template": {"inline": "must not overwrite archive"}}]}
                data["tags"]["test"]["services"]["config"] = {}
                inv = work / ("ownership-" + alias_kind + ".yaml")
                inv.write_text(json.dumps(data, indent=2) + "\n")
                output = run(["diff", "-i", inv, "--service", "config"], ok=False)
                check(alias_kind + " alias conflicts before filtered artifact observation",
                      "resource owned" in output and (target / "tree/readme").read_text() == "readme")
            finally:
                if alias_kind == "bind":
                    subprocess.run(["umount", str(alias)], check=True)
                    alias.rmdir()
                else:
                    alias.unlink()
        for label, managed in [("app", item), ("prior", component(path="prior")), ("resume", frozen), ("tree", tree)]:
            before = len(requests)
            _, saved, _ = prepare("remove-" + label, managed, state="absent")
            apply(saved)
            check(label + " removal needs no download", not (target / label).exists() and len(requests) == before)
        report = {"status": "passed", "count": len(checks), "checks": checks,
                  "binary_sha256": digest(Path(binary).read_bytes()), "transport": "local", "https_requests": len(requests)}
        (work / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2))
    finally:
        server.shutdown()
        server.server_close()
        shutil.rmtree(target)
        if args.work_root:
            print("Artifacts: " + str(work))
        else:
            shutil.rmtree(work)


if __name__ == "__main__":
    main()
