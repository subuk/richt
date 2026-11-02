---
title: "Links and reactions"
description: "Link actual component changes to one process lifecycle action."
weight: 20
---

A process's `restart_on` and `reload_on` list `binary.<name>` or `config.<name>`
references in the same service. Component names are inventory keys, independent
of package names and file paths. Unknown or duplicate references fail validation;
cross-service references are unsupported.

```yaml
processes:
  web:
    type: systemd
    unit: example.service
    restart_on: [binary.package]
    reload_on: [config.main]
```

Only linked components whose diff actually changes contribute a reaction,
including config attribute changes. Unrelated changes do not trigger one.
Multiple reload inputs merge into one reload; any restart input wins over reload.
Changes to a process's owned unit or drop-ins also request its restart. Sensitive
files compare real bytes, so hiding their display does not suppress reactions.

`after` is a separate ordering rule between processes. It neither subscribes to
changes nor implies a reaction. See [lifecycle and ordering](lifecycle.md).

## Prepared lifecycle branches

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

## Reload capability

For a present assignment, declaring `reload_on` requires supported reload
capability during preparation, even if none of its inputs currently changes or
a restart would win. Absent assignments do not require reload capability. Unsupported
reload fails explicitly and never falls back to restart.

The [systemd capability reference](processes/systemd.md#reload-capability) explains
how existing and prepared unit definitions are checked, `ExecReload` resets,
`Type=notify-reload`, and the requirements for a first package installation.
