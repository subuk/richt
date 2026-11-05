---
title: "Services"
description: "Compose reusable services from binaries, configs, and processes."
weight: 45
---

A Richt **service** is a reusable named definition of host configuration. It
contains ordered lists of [binaries](binaries/_index.md),
[configs](configs/_index.md), and [processes](processes/_index.md). Any nonempty combination is valid: a service
can manage only a repository file, only a package, only a process, or all three.
An individual process, such as `rsyslog.service`, is one component of a service;
it is not the service definition itself.

[Tags assign services to hosts](../inventory.md#schema-and-file-references).
The assignment requests `present` by default, or explicit `absent`. Component
settings belong to the reusable definition. Templates receive the host's merged
variables. Each service definition requires an integer `order`; effective
services on one host must have different orders. For a service loaded through
`file`, `order` belongs in the referenced file beside its components. The
inventory entry contains only the file reference.

## A complete inventory

This inventory illustrates a package, a rendered config, and a process with
restart reactions. It assumes a DNF 4/systemd target with the `rsyslog` package
available from its configured repositories. Applying it manages the target's
logging service; use a disposable host and run diff/apply as root. The local
transport shown below does not request sudo. For a non-root account, explicitly
configure [noninteractive sudo](../ssh.md).

```yaml
tags:
  logging:
    services:
      system-logging: {}
    vars:
      log_file: /var/log/richt-example.log
services:
  system-logging:
    order: 10
    binaries:
      - name: package
        type: rpm
        package: rsyslog
    configs:
      - name: main
        path: /etc/rsyslog.d/30-richt-example.conf
        owner: root
        group: root
        mode: "0644"
        template:
          inline: "local0.* {{ .log_file }}\n"
    processes:
      - name: daemon
        type: systemd
        unit: rsyslog.service
        enabled: true
        restart_on: [binary.package, config.main]
hosts:
  - name: example
    tags: [logging]
    transport: {type: local}
```

Save it as `inventory.yaml`, build Richt with `make build`, and inspect before
preparing a plan:

```sh
bin/richt inventory validate -i inventory.yaml
bin/richt inventory show -i inventory.yaml
bin/richt diff -i inventory.yaml -o plan.json
bin/richt apply plan.json
bin/richt diff -i inventory.yaml
```

Inventory inspection does not contact hosts, open templates, or fetch secrets.
Diff renders inputs and observes the target without changing managed resources.
Apply displays the prepared plan and requires terminal confirmation unless `-y`
is supplied. [Saved plans and apply](../files.md) explains frozen inputs,
confirmation, and progress; [SSH and sudo](../ssh.md) covers remote targets and
explicit noninteractive privilege escalation.

Read [lifecycle and ordering](lifecycle.md) for execution phases and removal, and
[change reactions](reactions.md) for `restart_on`, `reload_on`, `run_on`, and process
branches. The component references own their fields and backend limits.

## Package requests

[RPM binaries](binaries/rpm.md) support DNF 4 and its YUM 4 compatibility command.
An unversioned package is installed only if absent; an exact request can upgrade
or downgrade. Plans retain the request while the target resolves the transaction
at apply.

## Downloaded artifacts

[Downloads](binaries/download.md) install a SHA-256-pinned public HTTPS file;
[archives](binaries/archive.md) safely extract a `tar.gz` into an exclusively
owned tree. They use the same ordered binary phase and process reactions as
packages. Target-side Python 3 and Linux atomic publication are required;
pre-existing files/trees are not adopted. See the [K3s](../../examples/k3s/README.md)
and [node_exporter](../../examples/node-exporter/README.md) examples.

## Process lifecycle

[Processes](processes/_index.md) manage systemd runtime state and independent
enablement, or run [oneshot commands](processes/oneshot.md) when linked components change.
[Reactions](reactions.md) combine actual component changes into one action;
[systemd](processes/systemd.md) defines state verification and timeouts.

## Owned units and drop-ins

A [systemd process](processes/systemd.md#owned-units-and-drop-ins) may own its full
unit or named drop-ins. These use the same [file and template
fields](configs/files.md) as configs. Definition changes schedule daemon reload
and the owning process's restart reaction.

## Removal and continuation

An explicit `absent` assignment requests [ordered removal](lifecycle.md#absent-services).
Removing an assignment alone leaves its resources in place. A task failure blocks
the rest of that host; [manual continuation](../continuation.md) retries unfinished
saved tasks and skips successes.

## Ready-package walkthrough

Follow the [ready vmango RPM walkthrough](../../examples/vmango/README.md) for a
complete runnable example with repository setup, exact packages, configuration,
process reactions, and removal. It uses published RPMs on a disposable target.
The [validation report](../validation.md) records the environment and verified
scenarios.

## Developer fixture regression suite

The separate [RPM and systemd fixture regression
suite](../validation.md#rpm-and-systemd-fixture-regression) builds custom test
RPMs and exercises backend failure cases. Its build tools are not prerequisites
for the ready-vmango walkthrough.
