#!/usr/bin/env python3
"""Build a new checksum-pinned HTTPS catalog; never publish credentials or plans.

The generated JSON documents are valid YAML. Serve the output directory with
an existing HTTPS server. This command builds files only; it does not upload
anything, contact targets, or create a TLS server.
"""

import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
from urllib.parse import urlsplit


def encode(value):
    return (json.dumps(value, indent=2) + "\n").encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path, help="New release directory (must not exist)")
    parser.add_argument("--base-url", required=True, help="HTTPS URL serving that release directory")
    parser.add_argument("--host", required=True, action="append", help="SSH hostname or client alias (repeatable)")
    parser.add_argument("--user", help="Optional SSH login user")
    parser.add_argument("--sudo", action="store_true", help="Explicitly request noninteractive sudo on targets")
    parser.add_argument("--message", default="Hello from HTTPS", help="Example merged template variable")
    args = parser.parse_args()
    base = urlsplit(args.base_url)
    if base.scheme != "https" or not base.netloc or base.username is not None or base.password is not None or base.query or base.fragment or "?" in args.base_url or "#" in args.base_url:
        parser.error("base-url must be absolute HTTPS without credentials, query or fragment")
    if len(set(args.host)) != len(args.host) or any(not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*", host) for host in args.host):
        parser.error("hosts must be distinct SSH hostnames or aliases, without user@ or options")
    if args.user and not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_.-]*", args.user):
        parser.error("invalid SSH user")
    if args.output.exists():
        parser.error("output already exists; build a new release directory")
    templates = Path(__file__).parent / "templates"
    service = {
        "order": 10,
        "configs": [{"name": "greeting", "path": "/var/tmp/richt-https-example/greeting.env", "mode": "0644",
                     "template": {"url": "templates/greeting.gotmpl", "sha256": digest((templates / "greeting.gotmpl").read_bytes())}}],
        "processes": [{"name": "demo", "type": "systemd", "unit": "richt-https-example.service", "enabled": True,
                       "restart_on": ["config.greeting"], "unit_file": {
                           "template": {"url": "templates/demo.service.gotmpl", "sha256": digest((templates / "demo.service.gotmpl").read_bytes())}}}],
    }
    service_bytes = encode(service)
    hosts = []
    for index, host in enumerate(args.host, 1):
        transport = {"type": "ssh", "host": host, "sudo": args.sudo}
        if args.user:
            transport["user"] = args.user
        hosts.append({"name": f"host-{index}", "tags": ["demo"], "transport": transport})
    inventory = {
        "tags": {"demo": {"services": {"demo": {}}, "vars": {"message": args.message}}},
        "services": {"demo": {"url": "service.yaml", "sha256": digest(service_bytes)}},
        "hosts": hosts,
    }
    # Create the complete new release before making it available at the chosen
    # HTTPS location. The caller controls publication of this directory.
    args.output.mkdir(parents=True)
    shutil.copytree(templates, args.output / "templates")
    (args.output / "service.yaml").write_bytes(service_bytes)
    inventory_bytes = encode(inventory)
    (args.output / "inventory.yaml").write_bytes(inventory_bytes)
    local = json.loads(json.dumps(inventory))
    local["services"]["demo"]["url"] = args.base_url.rstrip("/") + "/service.yaml"
    (args.output / "local-inventory.yaml").write_bytes(encode(local))
    absent = json.loads(json.dumps(inventory))
    absent["tags"]["demo"]["services"]["demo"]["state"] = "absent"
    (args.output / "absent.yaml").write_bytes(encode(absent))
    standalone = json.loads(json.dumps(inventory))
    standalone["services"]["demo"] = json.loads(service_bytes)
    standalone["services"]["demo"]["configs"][0]["template"] = {"inline": (templates / "greeting.gotmpl").read_text()}
    standalone["services"]["demo"]["processes"][0]["unit_file"]["template"] = {"inline": (templates / "demo.service.gotmpl").read_text()}
    (args.output / "standalone.yaml").write_bytes(encode(standalone))
    print("Catalog built:", args.output)
    print("Inventory URL:", args.base_url.rstrip("/") + "/inventory.yaml")
    print("Inventory SHA-256:", digest(inventory_bytes))


if __name__ == "__main__":
    main()
