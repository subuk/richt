#!/usr/bin/env python3
"""Validate Linux facts locally or over an explicitly supplied disposable SSH target.

Only a unique /var/tmp/richt-facts-* directory is mutated on the target. It is
removed in finally; private controller logs and acceptance.json are retained.
The target needs iproute2 JSON support, Python 3 isolated mode, readable
procfs/sysfs and standard tools.
"""

import argparse
import hashlib
import json
import os
import re
import shlex
import shutil
import subprocess
import tempfile
import uuid
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--host", help="Optional disposable SSH host or user@host")
    parser.add_argument("--sudo", action="store_true", help="Explicit sudo -n on target commands")
    parser.add_argument("--work-root", type=Path, help="Parent for a unique retained artifact directory")
    args = parser.parse_args()
    if args.host and not re.fullmatch(r"(?:[A-Za-z0-9_][A-Za-z0-9_.-]*@)?[A-Za-z0-9][A-Za-z0-9_.:-]*", args.host):
        parser.error("--host must be a plain SSH host or user@host without options")
    if args.host and args.host.startswith("-"):
        parser.error("--host cannot begin with an option")
    os.umask(0o077)
    if args.work_root:
        args.work_root.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="richt-facts-", dir=args.work_root)).resolve()
    binary = args.binary.resolve()
    root = "/var/tmp/richt-facts-" + uuid.uuid4().hex[:16]
    connection = {"type": "ssh" if args.host else "local"}
    if args.host:
        user, separator, host = args.host.rpartition("@")
        connection["host"] = host if separator else args.host
        if separator:
            connection["user"] = user
    if args.sudo:
        connection["sudo"] = True
    checks = []
    report = {"status": "running", "transport": connection["type"], "sudo": args.sudo,
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "checks": checks}
    commands = 0

    def run(argv, ok=True, env=None):
        nonlocal commands
        result = subprocess.run([str(x) for x in argv], text=True, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=180, env=env)
        commands += 1
        (work / f"command-{commands:03}.log").write_text(
            "$ " + shlex.join([str(x) for x in argv]) + "\nstdout:\n" + result.stdout +
            "\nstderr:\n" + result.stderr)
        if ok and result.returncode:
            raise AssertionError(f"command {commands} failed ({result.returncode}); see private log")
        return result

    def target(script, ok=True):
        command = ["sudo", "-n", "--", "sh", "-c", script] if args.sudo else ["sh", "-c", script]
        if args.host:
            command = ["ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
                       "--", args.host, shlex.join(command)]
        return run(command, ok=ok)

    def check(name, condition):
        if not condition:
            raise AssertionError(name)
        checks.append(name)
        print("PASS " + name, flush=True)

    def inventory(path, configs=None):
        document = {"tags": {"assigned": {"services": {"report": {}}}},
                    "services": {"report": {"order": 10, "configs": configs or [
                        {"path": root + "/plain", "template": {"inline": "plain\n"}}]}},
                    "hosts": [{"name": "assigned", "tags": ["assigned"], "transport": connection},
                              {"name": "unassigned", "tags": [], "transport": connection}]}
        path.write_text(json.dumps(document, indent=2))
        return path

    def facts(path, cache, *flags, env=None):
        result = run([binary, "facts", "-i", path, "--facts-cache-dir", cache,
                      "--progress=off", *flags], env=env)
        return json.loads(result.stdout)["hosts"]

    def diff(path, cache, plan):
        return run([binary, "diff", "-i", path, "--host", "assigned", "--facts-cache-dir", cache,
                    "--progress=off", "-o", plan])

    try:
        print("Artifacts: " + str(work), flush=True)
        actual = target("set -eu; uname -s; uname -r; uname -m; hostname").stdout.splitlines()
        check("target is Linux", len(actual) == 4 and actual[0] == "Linux")
        target("mkdir -- " + shlex.quote(root))
        source = inventory(work / "inventory.yaml", [{"path": root + "/unused",
                            "template": {"file": "missing-template.gotmpl"}}])
        cache = work / "cache"
        live = facts(source, cache, "--refresh")
        check("live sorted assigned and unassigned hosts", [h["host"] for h in live] == ["assigned", "unassigned"]
              and all(h["source"] == "live" for h in live))
        observed = live[0]["facts"]
        check("target uname and hostname match snapshot", observed["kernel"]["release"] == actual[1]
              and observed["architecture"] == actual[2] and observed["hostname"] == actual[3])
        check("typed CPU memory and network facts", isinstance(observed["cpu"]["logical_count"], int)
              and observed["cpu"]["logical_count"] > 0 and isinstance(observed["memory"]["total_bytes"], int)
              and observed["memory"]["total_bytes"] > 0 and isinstance(observed["network"]["interfaces"], dict))
        check("schema defaults and availability materialized", isinstance(observed["os"]["id_like"], list)
              and live[0]["schema_version"] == "1" and bool(live[0]["availability"])
              and all(observed["network"][key]["status"] in ("selected", "absent", "ambiguous")
                      for key in ("default_ipv4", "default_ipv6")))
        trap_bin = work / "trap-bin"
        trap_bin.mkdir()
        trap_file = work / "unexpected-target-command"
        for executable in ("ssh", "uname", "ip", "cat", "hostname", "python3"):
            trap = trap_bin / executable
            trap.write_text('#!/bin/sh\nprintf invoked >> "$FACTS_TRAP"\nexit 91\n')
            trap.chmod(0o700)
        offline_env = dict(os.environ, PATH=str(trap_bin) + os.pathsep + os.environ.get("PATH", ""),
                           FACTS_TRAP=str(trap_file))
        cached = facts(source, cache, "--cached", env=offline_env)
        check("cached-only opens no template and runs no target command", not trap_file.exists())
        check("cached snapshots preserve collection times", all(h["source"] == "cache" for h in cached)
              and [h["collected_at"] for h in cached] == [h["collected_at"] for h in live])
        stale = facts(source, cache, "--cached", "--facts-max-age=0", "--host", "unassigned")
        check("stale cached-only selection includes unassigned host", len(stale) == 1
              and stale[0]["host"] == "unassigned" and stale[0]["source"] == "cache" and stale[0]["stale"])
        refreshed = facts(source, cache, "--host", "assigned", "--refresh")
        check("refresh collects replacement", refreshed[0]["source"] == "live"
              and refreshed[0]["collected_at"] >= live[0]["collected_at"])
        before = {p.name: p.read_bytes() for p in cache.glob("*.json")}
        uncached = facts(source, cache, "--no-facts-cache", "--host", "assigned")
        check("no-cache leaves persistent records unchanged", uncached[0]["source"] == "live"
              and before == {p.name: p.read_bytes() for p in cache.glob("*.json")})

        lazy_cache = work / "unused-cache"
        lazy = inventory(work / "lazy.yaml", [{"path": root + "/lazy", "template": {
            "inline": '{{ if false }}{{ fact "hostname" }}{{ end }}plain\n'}}])
        diff(lazy, lazy_cache, work / "lazy-plan.json")
        check("unexecuted fact branch does not touch cache", not lazy_cache.exists())
        plain = inventory(work / "plain.yaml")
        diff(plain, lazy_cache, work / "plain-plan.json")
        check("template without facts does not touch cache", not lazy_cache.exists())

        template = ('os={{ fact "os.id" }}\narch={{ fact "architecture" }}\n'
                    'workers={{ printf "%d" (fact "cpu.logical_count") }}\n'
                    '{{ range fact "network.interfaces" }}iface={{ .name }} mtu={{ .mtu }}\n{{ end }}'
                    '{{ with index (fact "network.interfaces") "richt.absent.123" }}unexpected{{ end }}')
        frozen_source = inventory(work / "frozen.yaml", [
            {"path": root + "/report", "template": {"inline": template}},
            {"path": root + "/blocked/second", "template": {"inline": '{{ fact "hostname" }}\n'}},
            {"path": root + "/last", "template": {"inline": "last\n"}}])
        frozen_cache = work / "frozen-cache"
        saved = work / "frozen-plan.json"
        diff(frozen_source, frozen_cache, saved)
        check("fact template preparation creates snapshot cache", bool(list(frozen_cache.glob("*.json"))))
        frozen_source.unlink()
        shutil.rmtree(frozen_cache)
        target("printf obstruction > " + shlex.quote(root + "/blocked"))
        first = run([binary, "apply", "-y", "--progress=off", saved], ok=False)
        check("saved-plan failure blocks later file", first.returncode != 0
              and target("test ! -e " + shlex.quote(root + "/last"), ok=False).returncode == 0)
        rendered = target("cat -- " + shlex.quote(root + "/report")).stdout
        check("typed facts render from frozen prepared bytes", "arch=" + actual[2] + "\n" in rendered
              and "workers=" + str(observed["cpu"]["logical_count"]) + "\n" in rendered
              and "unexpected" not in rendered)
        target("rm -- " + shlex.quote(root + "/blocked") + "; printf 'external after interruption\\n' > "
               + shlex.quote(root + "/report"))
        resumed = run([binary, "apply", "-y", "--progress=off", saved])
        check("continuation skips completed file after inventory and cache deletion",
              "previously completed" in resumed.stdout
              and target("cat -- " + shlex.quote(root + "/report")).stdout == "external after interruption\n"
              and target("cat -- " + shlex.quote(root + "/blocked/second")).stdout == actual[3] + "\n"
              and target("cat -- " + shlex.quote(root + "/last")).stdout == "last\n"
              and not frozen_source.exists() and not frozen_cache.exists())
        report["status"] = "passed"
    except Exception as error:
        report["status"] = "failed"
        report["error"] = str(error)
        raise
    finally:
        cleanup = target("rm -rf -- " + shlex.quote(root), ok=False)
        report["cleanup_succeeded"] = cleanup.returncode == 0
        if cleanup.returncode:
            report["status"] = "failed"
        (work / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
        if cleanup.returncode:
            raise AssertionError("target cleanup failed; inspect private logs")


if __name__ == "__main__":
    main()
