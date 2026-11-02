---
title: "RPM and systemd services"
description: "Manage exact package requests, process reactions, unit files, and explicit removal."
weight: 60
---

Richt manages each service in binary → config → process order. A service may
contain any nonempty combination of these component sets. Services run in their
definition's `order`; tags supply assignments and `after` orders processes
within one service. References and
cycles are validated before diff. There are no cross-service dependency links.

The supported package backend is **DNF 4**, including its **YUM 4 compatibility
command**. YUM 3, DNF 5, APT, and non-systemd process managers are unsupported.
The end-to-end verification environment is **AlmaLinux 10.2 x86_64, DNF 4.20.0,
YUM 4 compatibility, systemd 257, SELinux Enforcing**. Other Linux versions have
not been verified. `manager --version` is checked during preparation without
reading repository metadata. Local and SSH transports are supported, including explicit noninteractive sudo.
See [SSH and parallel apply](ssh.md) for connection and authentication rules.

## Package requests

```yaml
binaries:
  app:
    type: rpm
    manager: dnf                 # dnf (default), or yum backed by DNF 4
    package: richt-demo          # RPM NAME, without an architecture selector
    version: 0:1.0-1.noarch     # optional; epoch:version-release.arch
```

An omitted `version` means install the package only if it is absent. An already
installed package is left alone, even if a newer repository version exists.
An explicit version includes all of epoch, version, release, and architecture;
write epoch `0` explicitly when the package has no epoch. The installed RPM
database determines whether the exact request already holds. An exact request
uses `dnf -y install-nevra -- NAME-EPOCH:VERSION-RELEASE.ARCH` (or `yum`), allowing
both upgrades and downgrades. An unversioned request uses `install-n`; removal
uses `remove`. Package names and versions are validated as individual arguments. Local
execution passes them directly; SSH safely quotes each argument for the target
shell.

Diff reads the installed RPM database, without consulting repository metadata.
A preceding config-only service can therefore create a `.repo` file that does
not exist at diff time. The JSON plan already contains the package operation;
apply does not add tasks or reconstruct the plan. The host downloads packages
when that operation runs.

The plan fixes the requested package and exact NEVRA when specified. It does
not freeze dependency resolution, repository metadata, RPM checksums, scriptlet
side effects, or all packages in the transaction. Those are resolved by DNF on
the target at execution. Repository trust and signature checking follow the
host's repository configuration. Large RPM artifacts are not embedded in JSON.

## Process lifecycle

```yaml
processes:
  web:
    type: systemd
    unit: richt-demo.service
    state: running              # running (default), or stopped
    enabled: true               # omitted: preserve current enablement
    timeout: 5s                 # positive Go duration; default 5s
    restart_on: [binary.app]
    reload_on: [config.main]
  worker:
    type: systemd
    unit: richt-worker.service
    after: [web]
```

Only resources whose diff actually changes contribute a reaction. Multiple
reload requests combine into one reload; any restart request wins over reload.
An unrelated changed resource has no reaction. Apply checks the process state
immediately before its lifecycle task and chooses an already displayed branch:

| Current state | Target | Input reaction | Action |
| --- | --- | --- | --- |
| inactive or failed | running | any or none | start once |
| active | running | restart | restart once |
| active | running | reload | reload once |
| active | running | none | no action |
| active | stopped | any or none | stop once |
| inactive or failed | stopped | any or none | no action |

An unexpected load/activation state fails the task. There is no extra restart
or reload after start. `timeout` must be a positive Go duration and defaults
to **5s**. A lifecycle command and its state verification form one
persisted task. Start/reload/restart must reach active; stop must reach inactive
or failed. Status is polled while waiting. The command and all waiting share
one timeout; timeout and command errors stop that host, without retry or
fallback. A timed-out systemctl client does not roll back a systemd job that
was already accepted. An explicit continuation retries an unfinished task.

Enablement has its own task and saved progress. Changing enabled does not
require starting, stopping, or restarting the process. A stopped process can
remain enabled, and a running process can be disabled. Each enablement command
and its verification also share the process timeout.

## Owned units and drop-ins

Without `unit_file` or `overrides`, Richt uses an existing unit, including one
supplied by a managed RPM. Full units and drop-ins use the same templates,
permissions, sensitivity, ownership, and SELinux rules as ordinary configs:

```yaml
processes:
  main:
    type: systemd
    unit: richt-demo.service
    restart_on: [binary.app]
    reload_on: [config.main]
    overrides:
      20-reload.conf:
        mode: "0644"
        template:
          inline: |
            [Service]
            ExecReload=
            ExecReload=/bin/kill -HUP $MAINPID
```

`unit_file` is a single Config object; its default path is
`/etc/systemd/system/<unit>`. Each override key is a `.conf` basename and its
default path is `/etc/systemd/system/<unit>.d/<key>`. Explicit paths must match
these locations. Templates are resolved relative to the service definition.

Calling `secret` automatically makes a unit or drop-in sensitive, and the whole
file diff is hidden. Real secret changes still schedule daemon reload and the
normal process reaction. Set `mode: "0600"` when its target bytes must be private;
sensitivity alone does not change target permissions. Use `sensitive: true`
explicitly to protect old bytes after removing a secret call or declaring the
service absent. Absent services do not open templates or secret sources. See
[YAML secrets and sensitive files](secrets.md).

Changes to owned units or drop-ins appear in the file diff. Richt schedules
`systemctl daemon-reload` before process tasks, then a restart reaction for the
process whose definition changed (or only start when it is stopped). Managed
config files under systemd's standard unit directories also schedule a daemon
reload. Package installation with processes schedules daemon-reload so a
previously missing package unit can be discovered without relying on RPM
scriptlets.

A declared `reload_on` must be supported during preparation, even when its input
does not currently change. Existing units use their effective unit fragment and
drop-ins; prepared replacements are merged in systemd filename order using the
maintained `coreos/go-systemd` parser. `ExecReload` resets are respected, as is
`Type=notify-reload`. Reload is never silently converted to restart.

When the RPM and its unit do not exist yet, diff cannot inspect that future
unit without downloading it from a repository that may not exist yet. A first
installation with `reload_on` must therefore provide an owned full unit or
drop-in declaring a supported `ExecReload` (as in the example). Otherwise
preparation reports that reload capability cannot be verified. A first install
using only restart reactions can use the package's unit directly.

## Removal and continuation

For a service assignment with `state: absent`, processes are stopped and
verified, then disabled, before owned units/drop-ins and declared configs are
removed. Processes are stopped in reverse dependency order. Declared RPM
packages are removed after the managed files, and a final daemon reload follows
unit/drop-in or package changes. An already missing unit is accepted during removal. An external unit that remains
installed is still stopped and disabled.

Across services, removal follows ascending service-definition order, just like
`present` assignments. There is no automatic reverse service removal order.

DNF may remove dependent packages in its removal transaction. RPM itself
controls the treatment of its package-owned config files, including possible
`.rpmsave` files. Richt removes only explicitly declared config/unit/drop-in
files; it does not purge application data, state directories, unrelated files,
or `.rpmsave` files. Removing a tag or service assignment from inventory does not mean
`absent`; declare `absent` to request deletion.

A failed task blocks later tasks on that host. Successful config writes and
other task progress remain saved when applying a JSON file. A pending process
reaction remains in that file, so continuation starts with the failed process
task and does not re-render or rewrite completed configs. There is no automatic
rollback, retry, autosaved plan, or run history.

## Ready-package walkthrough

For the current full local example, use [ready vmango RPMs](../examples/vmango/README.md)
on a disposable DNF 4/systemd target. It covers repository configuration,
package/config/process preparation, and explicit removal without creating a
custom RPM. See [validation](validation.md) for the results actually recorded
for that scenario.

## Developer fixture regression suite

The following harness is a separate developer regression tool. Its custom RPM
build prerequisites are not required by the ready-vmango walkthrough or current
package acceptance.

Use a disposable AlmaLinux machine with root privileges. The fixture only
manages resources named `richt-*`; the harness deliberately installs/removes
its test package and changes its test units. Do not run it on a user host.

```sh
dnf install -y rpm-build createrepo_c python3
# Build Richt on the target, or copy a Linux binary built on another machine:
# CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o richt .
python3 examples/rpm-systemd/verify.py ./richt /root/richt-service-test
```

The harness builds real versions `1.0-1` and `2.0-1` of a minimal noarch RPM,
creates a file repository, and runs the CLI against it. It checks first install
with a previously absent repo config, exact update/downgrade, no-op diff,
unversioned policy, merged reactions, no extra restart after start, independent
enablement and stopped state, multiple processes with `after`, command failure,
a shared command/wait timeout, unsupported reload, persisted process resume,
YUM 4 exact install/removal, and complete declared-resource removal. It also
checks initial secret rendering and secret rotation, actual reload reactions,
redacted file display, and frozen apply after deleting the secret source.
The recorded fixture run passed all 33 checks on 2026-11-01 on the environment
listed above; this is evidence for that fixture suite, not a verification claim
for the ready-vmango example. Plans, displayed diffs, apply output,
and `acceptance.json` remain in the work directory. Application event traces
are deliberately retained. Fixture scripts require only standard Python and
shell facilities in addition to the listed RPM build tools.

For a manual walkthrough:

```sh
sh examples/rpm-systemd/build-repository.sh
./richt diff -i examples/rpm-systemd/inventory.yaml -o /root/demo-plan.json
./richt apply -y /root/demo-plan.json
./richt diff -i examples/rpm-systemd/inventory.yaml
```

Change `version` between `0:1.0-1.noarch` and `0:2.0-1.noarch`, or change the
config inline content, to inspect the resulting exact package and process
branches. Set the demo assignment to `absent` to remove it.

DNF's exact installation behavior is documented in the
[DNF command reference](https://dnf.readthedocs.io/en/stable/command_ref.html).
Unit states and systemctl operations follow the
[systemd 257 systemctl manual](https://www.freedesktop.org/software/systemd/man/257/systemctl.html).
