#!/usr/bin/env python3
"""Verify isolated systemd service/socket masks and ordering locally or through SSH/sudo."""

import argparse
import json
from pathlib import Path
import shlex
import subprocess
import uuid


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--ssh-host", help="Authorized disposable [user@]target; uses sudo -n")
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    ssh_user, ssh_host = (args.ssh_host.rsplit("@", 1) if args.ssh_host and "@" in args.ssh_host else (None, args.ssh_host))
    binary = str(Path(args.binary).resolve())
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    prefix = "richt-mask-" + uuid.uuid4().hex[:12]
    unit, owned, missing = [prefix + suffix + ".service" for suffix in ("", "-owned", "-missing")]
    fixture = "/usr/lib/systemd/system/" + unit
    target_config = "/tmp/" + prefix + ".conf"
    ordered_units = [prefix + f"-ordered-{index}.service" for index in range(4)]
    ordered_configs = ["/tmp/" + prefix + f"-ordered-{index}.conf" for index in range(4)]
    ordered_events = "/tmp/" + prefix + "-ordered.events"
    socket_unit = prefix + "-activation.socket"
    socket_service = prefix + "-activation.service"
    socket_vendor = "/usr/lib/systemd/system/" + socket_unit
    socket_service_vendor = "/usr/lib/systemd/system/" + socket_service
    socket_owned = "/etc/systemd/system/" + socket_unit
    socket_override = socket_owned + ".d/20-listener.conf"
    socket_path = "/run/" + prefix + ".sock"
    socket_events = "/tmp/" + prefix + "-socket.events"
    socket_config = "/tmp/" + prefix + "-socket.conf"
    checks = []
    observations = {}

    def command(argv, success=True):
        result = subprocess.run(argv, text=True, capture_output=True)
        if success and result.returncode:
            raise AssertionError(f"command failed ({result.returncode}): {result.stderr}")
        return result

    def target(script, success=True):
        if args.ssh_host:
            argv = ["ssh", "-o", "BatchMode=yes", args.ssh_host, "sudo -n sh -c " + shlex.quote(script)]
        else:
            argv = ["sh", "-c", script]
        return command(argv, success)

    def check(name, condition):
        if not condition:
            raise AssertionError(name)
        checks.append(name)

    def inventory(name, process, state="present", config=False):
        transport = "{type: local}"
        if args.ssh_host:
            transport = "{type: ssh, host: " + json.dumps(ssh_host)
            if ssh_user:
                transport += ", user: " + json.dumps(ssh_user)
            transport += ", sudo: true}"
        configs = ""
        if config:
            configs = f"    configs:\n      - name: main\n        path: {target_config}\n        template: {{inline: frozen}}\n"
        path = output / (name + ".yaml")
        path.write_text(f"tags:\n  test:\n    services:\n      test: {{state: {state}}}\nservices:\n  test:\n    order: 10\n" + configs + "    processes:\n      - name: main\n        type: systemd\n" + process + f"hosts:\n  - name: target\n    tags: [test]\n    transport: {transport}\n")
        return path

    def prepare(name, process, state="present", config=False, success=True):
        source = inventory(name, process, state, config)
        saved = output / (name + ".json")
        result = command([binary, "diff", "-i", str(source), "-o", str(saved)], success)
        (output / (name + ".diff")).write_text(result.stdout + result.stderr)
        return saved, result

    def apply(saved, success=True):
        result = command([binary, "apply", "-y", str(saved)], success)
        (output / (saved.stem + ".apply")).write_text(result.stdout + result.stderr)
        return result

    def status(name):
        result = target("systemctl show -p LoadState -p ActiveState -p UnitFileState -- " + name)
        return dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)

    def verify_sockets():
        transport = {"type": "local"}
        if args.ssh_host:
            transport = {"type": "ssh", "host": ssh_host, "sudo": True}
            if ssh_user:
                transport["user"] = ssh_user
        socket_definition = (
            "[Unit]\nDescription=Richt isolated socket v1\n"
            "[Socket]\n"
            f"ListenStream={socket_path}\nService={socket_service}\n"
            "SocketMode=0600\nRemoveOnStop=yes\n"
            f"ExecStartPost=/bin/sh -c 'echo socket-start >> {socket_events}'\n"
            "[Install]\nWantedBy=sockets.target\n"
        )
        service_definition = (
            "[Service]\nExecStart=/usr/bin/sleep infinity\n"
            f"ExecStartPost=/bin/sh -c 'echo service-start >> {socket_events}'\n"
        )
        for path in (socket_vendor, socket_service_vendor, socket_owned, socket_override, socket_path, socket_events, socket_config):
            target("test ! -e " + path + " && test ! -L " + path)
        for path, content in ((socket_vendor, socket_definition), (socket_service_vendor, service_definition)):
            target("printf %s " + shlex.quote(content) + " > " + path)
        target("systemctl daemon-reload")

        def prepare_socket(name, processes, state="present", configs=None, success=True):
            service = {"order": 10, "processes": processes}
            if configs:
                service["configs"] = configs
            desired = {
                "tags": {"test": {"services": {"socket": {"state": state}}}},
                "services": {"socket": service},
                "hosts": [{"name": "target", "tags": ["test"], "transport": transport}],
            }
            source = output / (name + "-inventory.json")
            source.write_text(json.dumps(desired, indent=2) + "\n")
            saved = output / (name + ".json")
            result = command([binary, "diff", "-i", str(source), "-o", str(saved)], success)
            (output / (name + ".diff")).write_text(result.stdout + result.stderr)
            return saved, result

        def tasks(saved):
            return json.loads(saved.read_text())["hosts"][0]["tasks"]

        def events():
            return target("cat " + socket_events).stdout.splitlines()

        def listening():
            return status(socket_unit) == {"LoadState": "loaded", "ActiveState": "active", "UnitFileState": "enabled"} and target(
                "systemctl show -p SubState --value " + socket_unit
            ).stdout.strip() == "listening" and target("test -S " + socket_path, False).returncode == 0

        def service_invocation():
            return target("systemctl show -p InvocationID --value " + socket_service).stdout.strip()

        process = {
            "name": "listener", "type": "systemd", "unit": socket_unit,
            "state": "running", "enabled": True,
            "unit_file": {"template": {"inline": socket_definition}},
            "overrides": {"20-listener.conf": {"template": {"inline": "[Socket]\nBacklog=16\n"}}},
        }
        saved, _ = prepare_socket("socket-owned-present", [process])
        apply(saved)
        check("owned socket starts once, listens, and enables", listening() and events() == ["socket-start"])
        check("socket start leaves associated service inactive", status(socket_service)["ActiveState"] == "inactive")
        saved, _ = prepare_socket("socket-owned-noop", [process])
        check("matching owned socket and drop-in are unchanged", not tasks(saved))

        # Both changed definitions must merge into one daemon reload and restart.
        process["unit_file"]["template"]["inline"] = socket_definition.replace("v1", "v2")
        process["overrides"]["20-listener.conf"]["template"]["inline"] = "[Socket]\nBacklog=32\n"
        saved, _ = prepare_socket("socket-owned-update", [process])
        prepared = tasks(saved)
        lifecycle = [task for task in prepared if task["operation"] == "systemd"]
        check("owned socket and drop-in changes merge daemon reload and restart", sum(
            task["operation"] == "daemon-reload" for task in prepared
        ) == 1 and [task["file"]["path"] for task in prepared if task["operation"] == "file"] == [socket_owned, socket_override] and len(lifecycle) == 1 and lifecycle[0]["process"]["unit"] == socket_unit and lifecycle[0]["process"]["reaction"] == "restart")
        apply(saved)
        check("owned socket changes execute exactly one restart", listening() and events() == ["socket-start", "socket-start"])

        bad = json.loads(json.dumps(process))
        bad["reload_on"] = ["config.main"]
        bad["unit_file"]["template"]["inline"] += "[Service]\nExecReload=/bin/true\n"
        trigger = [{"name": "main", "path": socket_config, "template": {"inline": "frozen\n"}}]
        _, result = prepare_socket("socket-reload-rejected", [bad], configs=trigger, success=False)
        check("socket reload is rejected despite a Service ExecReload section", result.returncode != 0 and "reload" in (result.stdout + result.stderr).lower())
        check("rejected socket reload does not write its trigger", target("test ! -e " + socket_config, False).returncode == 0)

        # A real connection activates only the fixture's paired service.
        connect = "import socket; s=socket.socket(socket.AF_UNIX); s.connect(" + repr(socket_path) + "); s.close()"
        target("python3 -c " + shlex.quote(connect))
        target("i=0; until systemctl is-active --quiet " + socket_service + "; do i=$((i+1)); test \"$i\" -lt 50 || exit 1; sleep 0.1; done")
        invocation = service_invocation()
        check("socket connection activates its service", bool(invocation) and status(socket_service)["ActiveState"] == "active")

        # Removing owned socket files exposes the isolated vendor fixture. Neither
        # socket stop/removal nor later masks may silently stop its active service.
        saved, _ = prepare_socket("socket-owned-absent", [process], state="absent")
        apply(saved)
        check("owned socket removal stops only the socket", status(socket_unit)["ActiveState"] == "inactive" and status(socket_service)["ActiveState"] == "active" and service_invocation() == invocation)
        check("owned socket removal deletes its full unit and drop-in", target("test ! -e " + socket_owned + " && test ! -e " + socket_override, False).returncode == 0)
        masked = {"type": "systemd", "unit": socket_unit, "state": "stopped", "enabled": False, "masked": True}
        saved, _ = prepare_socket("socket-mask", [masked])
        apply(saved)
        check("socket persistent mask leaves activated service running", status(socket_unit)["LoadState"] == "masked" and status(socket_service)["ActiveState"] == "active" and service_invocation() == invocation)
        saved, _ = prepare_socket("socket-mask-noop", [masked])
        check("matching socket mask is unchanged", not tasks(saved))
        unmasked = dict(masked, masked=False)
        saved, _ = prepare_socket("socket-unmask", [unmasked])
        apply(saved)
        target("systemctl mask --runtime " + socket_unit)
        saved, _ = prepare_socket("socket-runtime-to-persistent", [masked])
        apply(saved)
        check("runtime socket mask becomes persistent", status(socket_unit)["UnitFileState"] == "masked" and target("test -L " + socket_owned, False).returncode == 0)
        saved, _ = prepare_socket("socket-unmask-both", [unmasked])
        apply(saved)
        check("socket unmask removes both mask locations", target("test ! -L " + socket_owned + " && test ! -L /run/systemd/system/" + socket_unit, False).returncode == 0)

        running = dict(unmasked, state="running", enabled=True)
        saved, _ = prepare_socket("socket-start-active-service", [running])
        result = apply(saved, success=False)
        (output / "socket-start-active-service-initial.apply").write_text(result.stdout + result.stderr)
        # systemd can refuse a fresh listener while its service is already active.
        # Record that real constraint, without making an implicit service stop.
        observations["socket_start_with_active_service"] = "started" if result.returncode == 0 else "refused"
        check("socket start never implicitly stops its active service", status(socket_service)["ActiveState"] == "active" and service_invocation() == invocation and all(
            task.get("process", {}).get("unit") != socket_service for task in tasks(saved)
        ))
        if result.returncode:
            check("socket start refusal persists failed lifecycle progress", tasks(saved)[-1]["progress"]["status"] == "error")
            (output / "socket-start-active-service-failed.json").write_text(saved.read_text())
            evidence = target("systemctl show " + socket_unit + " " + socket_service + "; journalctl --no-pager -n 30 -u " + socket_unit + " -u " + socket_service, False)
            (output / "socket-start-active-service-evidence.txt").write_text(evidence.stdout + evidence.stderr)
        (output / "socket-start-active-service-inventory.json").unlink()
        service_stop = {"type": "systemd", "unit": socket_service, "state": "stopped"}
        stopped, _ = prepare_socket("socket-explicit-service-stop", [service_stop])
        apply(stopped)
        check("explicit service process stops the activated service", status(socket_service)["ActiveState"] == "inactive")
        apply(saved)
        check("saved socket start continues after explicit service stop", listening() and all(task["progress"]["status"] == "success" for task in tasks(saved)))

        # Force a socket mask failure after successful config/stop tasks, then
        # continue without source inventory. The edited successful file must stay.
        saved, _ = prepare_socket("socket-continue-mask", [masked], configs=trigger)
        target("cp " + socket_vendor + " " + socket_owned + "; systemctl daemon-reload")
        result = apply(saved, success=False)
        (output / "socket-continue-mask-failed.apply").write_text(result.stdout + result.stderr)
        (output / "socket-continue-mask-failed.json").write_text(saved.read_text())
        recorded = tasks(saved)
        check("socket mask failure persists successful config and failed mask", result.returncode != 0 and recorded[0]["progress"]["status"] == "success" and recorded[-1]["operation"] == "systemd-mask" and recorded[-1]["progress"]["status"] == "error")
        (output / "socket-continue-mask-inventory.json").unlink()
        target("printf edited > " + socket_config + "; rm " + socket_owned + "; systemctl daemon-reload")
        apply(saved)
        check("saved socket continuation skips successful file without inventory", target("cat " + socket_config).stdout == "edited" and status(socket_unit)["LoadState"] == "masked")

    def verify_ordered_components():
        transport = {"type": "local"}
        if args.ssh_host:
            transport = {"type": "ssh", "host": ssh_host, "sudo": True}
            if ssh_user:
                transport["user"] = ssh_user
        configs, processes = [], []
        for index, name in enumerate(("z-first", None, "a-third", None)):
            config = {"path": ordered_configs[index], "template": {"inline": f"item={index}\n"}}
            # Both explicit template type and its omitted default are supported.
            if index % 2 == 0:
                config["type"] = "template"
            definition = (
                "[Service]\nType=oneshot\nRemainAfterExit=yes\n"
                f"ExecStart=/bin/sh -c 'echo start-{index} >> {ordered_events}'\n"
                f"ExecStop=/bin/sh -c 'echo stop-{index} >> {ordered_events}'\n"
                "[Install]\nWantedBy=multi-user.target\n"
            )
            process = {
                "type": "systemd", "unit": ordered_units[index], "enabled": True,
                "unit_file": {"template": {"inline": definition}},
            }
            if name is not None:
                config["name"] = name
                process["name"] = name
            configs.append(config)
            processes.append(process)
        desired = {
            "tags": {"test": {"services": {"ordered": {}}}},
            "services": {"ordered": {"order": 10, "configs": configs, "processes": processes}},
            "hosts": [{"name": "target", "tags": ["test"], "transport": transport}],
        }

        def prepare_ordered(name):
            source = output / (name + "-inventory.json")
            source.write_text(json.dumps(desired, indent=2) + "\n")
            saved = output / (name + ".json")
            result = command([binary, "diff", "-i", str(source), "-o", str(saved)])
            (output / (name + ".diff")).write_text(result.stdout + result.stderr)
            return saved, json.loads(saved.read_text())["hosts"][0]["tasks"]

        saved, tasks = prepare_ordered("ordered-present")
        check("named and unnamed configs preserve declaration order", [
            task["file"]["path"] for task in tasks
            if task["operation"] == "file" and task["file"]["path"] in ordered_configs
        ] == ordered_configs)
        check("named and unnamed processes preserve declaration order", [
            task["process"]["unit"] for task in tasks if task["operation"] == "systemd"
        ] == ordered_units)
        apply(saved)
        starts = [f"start-{index}" for index in range(4)]
        check("systemd starts execute in declaration order", target("cat " + ordered_events).stdout.splitlines() == starts)
        check("all ordered configs contain prepared bytes", all(
            target("cat " + path).stdout == f"item={index}\n"
            for index, path in enumerate(ordered_configs)
        ))
        desired["tags"]["test"]["services"]["ordered"]["state"] = "absent"
        saved, tasks = prepare_ordered("ordered-absent")
        check("absent configs reverse their own declaration order", [
            task["file"]["path"] for task in tasks
            if task["operation"] == "file" and task["file"]["path"] in ordered_configs
        ] == ordered_configs[::-1])
        check("absent processes reverse their own declaration order", [
            task["process"]["unit"] for task in tasks if task["operation"] == "systemd"
        ] == ordered_units[::-1])
        check("each absent process stops before disablement", [
            (task["operation"], task["process"]["unit"]) for task in tasks
            if task["operation"] in ("systemd", "systemd-enable")
        ] == [
            (operation, name) for name in ordered_units[::-1]
            for operation in ("systemd", "systemd-enable")
        ])
        apply(saved)
        check("systemd stops execute in reverse declaration order", target("cat " + ordered_events).stdout.splitlines() == starts + [
            f"stop-{index}" for index in reversed(range(4))
        ])
        check("ordered removal deletes configs and owned units", all(
            target("test ! -e " + path, False).returncode == 0
            for path in ordered_configs + ["/etc/systemd/system/" + name for name in ordered_units]
        ))

    definition = "[Service]\nExecStart=/usr/bin/sleep infinity\n[Install]\nWantedBy=multi-user.target\n"
    try:
        platform = target("cat /etc/os-release; uname -m; id -u; getenforce; systemctl --version | head -1").stdout
        (output / "platform.txt").write_text(platform)
        target("test ! -e " + fixture + " && test ! -L /etc/systemd/system/" + unit)
        target("printf %s " + shlex.quote(definition) + " > " + fixture + "; systemctl daemon-reload; systemctl enable --now " + unit)
        masked = f"        unit: {unit}\n        state: stopped\n        enabled: false\n        masked: true\n"
        saved, _ = prepare("mask", masked)
        apply(saved)
        observed = status(unit)
        check("mask stops and disables before persistent masking", observed == {"LoadState": "masked", "ActiveState": "inactive", "UnitFileState": "masked"} and target("test ! -L /etc/systemd/system/multi-user.target.wants/" + unit, False).returncode == 0)
        saved, _ = prepare("matching", masked)
        check("matching masked stopped target is unchanged", not json.loads(saved.read_text())["hosts"][0]["tasks"])
        saved, _ = prepare("preserve", f"        unit: {unit}\n        state: stopped\n")
        check("mask omission preserves mask", not json.loads(saved.read_text())["hosts"][0]["tasks"])
        saved, _ = prepare("unmask-start", f"        unit: {unit}\n        state: running\n        enabled: true\n        masked: false\n")
        apply(saved)
        check("unmask before enable and start", status(unit) == {"LoadState": "loaded", "ActiveState": "active", "UnitFileState": "enabled"})
        target("systemctl stop " + unit + "; systemctl disable " + unit + "; systemctl mask --runtime " + unit)
        saved, _ = prepare("runtime-to-persistent", masked)
        apply(saved)
        check("runtime mask becomes persistent", status(unit)["UnitFileState"] == "masked")
        unmasked = f"        unit: {unit}\n        state: stopped\n        enabled: false\n        masked: false\n"
        saved, _ = prepare("unmask-both", unmasked)
        apply(saved)
        check("unmask removes both mask locations", target("test ! -L /etc/systemd/system/" + unit + " && test ! -L /run/systemd/system/" + unit, False).returncode == 0)
        target("systemctl enable " + unit + "; systemctl mask " + unit)
        saved, _ = prepare("unmask-hidden-enabled", unmasked)
        apply(saved)
        check("unmask disabled target removes hidden enable links", status(unit)["UnitFileState"] == "disabled" and target("test ! -L /etc/systemd/system/multi-user.target.wants/" + unit, False).returncode == 0)
        saved, _ = prepare("missing-mask", f"        unit: {missing}\n        state: stopped\n        enabled: false\n        masked: true\n")
        apply(saved)
        check("missing external unit can be masked", status(missing)["LoadState"] == "masked")
        saved, _ = prepare("missing-unmask", f"        unit: {missing}\n        state: stopped\n        enabled: false\n        masked: false\n")
        apply(saved)
        check("missing unit can be unmasked without enable failure", status(missing)["LoadState"] == "not-found")
        target("systemctl mask " + owned)
        full = f"        unit: {owned}\n        state: running\n        enabled: true\n        masked: false\n        unit_file:\n          template:\n            inline: |\n" + "".join("              " + line + "\n" for line in definition.splitlines())
        _, result = prepare("owned-preserve-rejected", full.replace("        masked: false\n", ""), success=False)
        check("masked owned full unit requires explicit unmask", result.returncode != 0 and "masked: false" in result.stderr)
        saved, _ = prepare("owned-unmask", full)
        check("owned unit write follows unmask task", json.loads(saved.read_text())["hosts"][0]["tasks"][0]["operation"] == "systemd-mask")
        apply(saved)
        check("owned full unit replaces mask and starts", status(owned)["ActiveState"] == "active" and target("test -f /etc/systemd/system/" + owned + " && test ! -L /etc/systemd/system/" + owned, False).returncode == 0)
        saved, _ = prepare("continue-mask", masked, config=True)
        target("cp " + fixture + " /etc/systemd/system/" + unit + "; systemctl daemon-reload")
        result = apply(saved, success=False)
        recorded = json.loads(saved.read_text())["hosts"][0]["tasks"]
        check("mask failure persists successful config separately", result.returncode != 0 and recorded[0]["progress"]["status"] == "success" and recorded[-1]["progress"]["status"] == "error")
        (output / "continue-mask.yaml").unlink()
        target("printf edited > " + target_config + "; rm /etc/systemd/system/" + unit + "; systemctl daemon-reload")
        apply(saved)
        check("saved continuation skips successful files after inventory deletion", target("cat " + target_config).stdout == "edited" and status(unit)["LoadState"] == "masked")
        target("systemctl stop " + owned + "; systemctl disable " + owned + "; rm /etc/systemd/system/" + owned + "; systemctl daemon-reload; systemctl mask " + owned)
        _, result = prepare("owned-absent-rejected", full, state="absent", success=False)
        check("absence cannot remove an owned full-unit mask", result.returncode != 0 and "explicitly unmask" in result.stderr)
        verify_ordered_components()
        verify_sockets()
    finally:
        for name in (unit, owned, missing, *ordered_units, socket_unit, socket_service):
            target("systemctl stop " + name + " 2>/dev/null || true; systemctl unmask " + name + " 2>/dev/null || true; systemctl unmask --runtime " + name + " 2>/dev/null || true; systemctl disable " + name + " 2>/dev/null || true; rm -f /etc/systemd/system/" + name + " /run/systemd/system/" + name, False)
        target("rm -f " + " ".join([fixture, target_config, ordered_events, *ordered_configs, socket_vendor, socket_service_vendor, socket_override, socket_path, socket_events, socket_config]) + "; rmdir " + socket_owned + ".d 2>/dev/null || true; systemctl daemon-reload", False)
        report = {"checks": checks, "passed": len(checks), "transport": "ssh-sudo" if args.ssh_host else "local-root", "observations": observations}
        (output / "acceptance.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
