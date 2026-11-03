---
title: "Links and reactions"
description: "Link actual component changes to one process lifecycle action."
weight: 20
---

A systemd process's `restart_on` and `reload_on`, or a oneshot process's
`run_on`, list `binary.<name>` or `config.<name>`
references in the same service. These names are explicit optional `name` labels
on binary and config list items, independent of package names and file paths.
Unnamed items cannot be referenced. Names must be unique within their collection;
unknown or duplicate references fail validation. Cross-service references are
unsupported.

```yaml
processes:
  - name: web
    type: systemd
    unit: example.service
    restart_on: [binary.package]
    reload_on: [config.main]
```

Only linked components whose diff actually changes contribute a reaction,
including downloaded-file or archive changes and tracked drift repairs,
attributes, plain-path creation, storage operations, and mount/fstab
changes. Unmanaged plain-file contents and directory children do not cause a
change. Each typed config is referenced through its optional `name`, independently
of its backend resource identity. Unrelated changes do not trigger a reaction.
A oneshot merges all its changed `run_on` inputs into one command task; no
changed linked input means no command. See the [oneshot reference](processes/oneshot.md)
for argv, absence, and continuation semantics.

For systemd, multiple reload inputs merge into one reload; any restart input
wins over reload.
Changes to a process's owned unit or drop-ins also request its restart. Sensitive
files compare real bytes, so hiding their display does not suppress reactions.

Declaration order controls component execution independently of change
subscriptions. A preceding component does not imply a reaction. See
[lifecycle and ordering](lifecycle.md).

## Prepared systemd lifecycle branches

Apply reads the process state immediately before executing a prepared lifecycle
task and chooses an already displayed branch:

| Current state | Target | Combined reaction | Action |
| --- | --- | --- | --- |
| inactive or failed | running | any or none | start once |
| active | running | restart | restart once |
| active | running | reload | reload once |
| active | running | none | no lifecycle action |
| active | stopped | any or none | stop once |
| inactive or failed | stopped | any or none | no lifecycle action |

There is no additional restart or reload after start. A target of `stopped`
never permits start. Enablement is independent and does not itself restart the
process. A plan includes lifecycle tasks needed by the observed state or a
prepared reaction; apply does not add tasks in response to later drift.

The systemd backend rejects unexpected load/activation states. Every lifecycle
command and its result check form one task with a shared timeout. See the
[systemd reference](processes/systemd.md#states-enablement-and-timeouts) for exact
states, missing-unit handling, and verification limits.

## Systemd reload capability

For a present assignment, declaring `reload_on` requires supported reload
capability during preparation, even if none of its inputs currently changes or
a restart would win. Absent assignments do not require reload capability. Unsupported
reload fails explicitly and never falls back to restart.

The [systemd capability reference](processes/systemd.md#reload-capability) explains
how existing and prepared unit definitions are checked, `ExecReload` resets,
`Type=notify-reload`, and the requirements for a first package installation.
