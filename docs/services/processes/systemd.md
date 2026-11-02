---
title: "systemd"
description: "Manage service-unit state, enablement, masks, owned definitions, and verified reload capability."
weight: 10
---

```yaml
processes:
  web:
    type: systemd
    unit: example-app.service
    state: running
    enabled: true
    timeout: 5s
    restart_on: [binary.package]
    reload_on: [config.main]
    after: []
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `type` | yes | `systemd` |
| `unit` | yes | A systemd `.service` unit name |
| `state` | no | `running` (default), or `stopped` |
| `enabled` | no | Boolean; omission preserves current enablement |
| `masked` | no | Boolean; `true` requests a persistent mask, `false` removes persistent and runtime masks; omission preserves masks |
| `timeout` | no | Positive Go duration; default `5s` |
| `restart_on` | no | Same-service binary/config references; default empty |
| `reload_on` | no | Same-service binary/config references; default empty; requires reload capability |
| `after` | no | Same-service process names; default empty |
| `unit_file` | no | One owned full-unit config object |
| `overrides` | no | Mapping of `.conf` basenames to owned drop-in config objects |

[Reactions](../reactions.md) owns reference semantics and lifecycle branches;
[lifecycle](../lifecycle.md) owns `after` ordering and service phases. Other
process managers and arbitrary script backends are unsupported. See
[platform requirements](../../installation.md) and [validation](../../validation.md)
for the verified systemd environment.

## States, enablement, and timeouts

Preparation observes the unit's load, activation, and enablement states. Without
owned definitions, Richt uses an existing unit, including one supplied by a
managed RPM. A missing unit may be supplied by a prepared package installation
or an owned full unit; otherwise preparation fails for a present process.
A missing external unit may explicitly target `masked: true`, or target
`masked: false` with `state: stopped`.

During a lifecycle task, `active` is running; `inactive` or `failed` is stopped.
Unexpected load/activation states fail explicitly. Start, reload, and restart
must reach active; stop must reach inactive or failed. Status is polled while
waiting. This verifies systemd state, not application readiness.

A lifecycle command and its result check form one persisted task. The command
and all waiting share the process timeout. Command errors and timeouts fail the
host without retry or fallback. A timed-out systemctl client may leave an
accepted systemd job running; Richt does not roll it back.

Enablement has a separate task and progress. Changing it does not itself start,
stop, or restart a process: a stopped process may remain enabled and a running
process may be disabled. `enabled` and `enabled-runtime` count as enabled. Its
command and verification also share one process timeout, separate from the
lifecycle task's timeout.

## Masks

```yaml
processes:
  unwanted:
    type: systemd
    unit: unwanted.service
    state: stopped
    masked: true
```

Masking and stopping are independent. An already running masked unit still needs
to stop. `masked: true` requires `state: stopped` and forbids `enabled: true`.
It creates a persistent `/etc/systemd/system/<unit>` link to `/dev/null`; a runtime
mask alone is changed to a persistent mask. Existing runtime and persistent masks
remain untouched when `masked` is omitted. A running or enabled target for a
currently masked unit requires explicit `masked: false`.

Enablement uses the observed `UnitFileState`: a masked unit counts as not enabled.
Retaining a mask with `enabled: false` does not remove enablement symlinks hidden
under that mask. This permits matching stopped, disabled-by-observation, masked
targets to remain unchanged. To remove hidden enablement links, explicitly
request `masked: false` with `enabled: false`; the enablement task runs after
unmasking even when the masked observation appeared not enabled.

Mask changes have their own `systemd-mask` task and persisted progress. Masking
runs after requested disablement and stop tasks. Unmasking removes both persistent
and runtime masks before config writes, enablement, start, or reactions. Each
mask task observes the current state, executes its prepared commands only when
needed, and verifies the result within its own process timeout. A failure blocks
later tasks; continuation skips successful unmask, file, and process tasks.

A persistent mask cannot coexist with an owned full unit at the same path.
Declaring both is rejected, including ordinary configs at that unit path. To
replace an existing mask with an owned full unit, explicitly declare
`masked: false`; Richt prepares that file as a new file after unmasking. With
mask omission, preparing an owned full-unit write or removal against a masked
unit fails with an instruction to unmask it first. This prevents following a
mask symlink into `/dev/null`. Owned drop-ins may coexist with a mask and remain
managed normally.

The [mask example](../../../examples/systemd-mask/README.md) demonstrates masking
a missing unit and replacing its mask with an owned unit.

## Owned units and drop-ins

Full units and drop-ins use the [config file fields](../configs/files.md),
including templates, owner/group, mode, SELinux, and sensitivity:

```yaml
processes:
  web:
    type: systemd
    unit: example-app.service
    unit_file:
      mode: "0644"
      template: {file: templates/example-app.service.gotmpl}
    overrides:
      20-reload.conf:
        mode: "0644"
        template:
          inline: |
            [Service]
            ExecReload=
            ExecReload=/bin/kill -HUP $MAINPID
```

The default `unit_file.path` is `/etc/systemd/system/<unit>`. Each override key
must be a plain `.conf` basename; its default path is
`/etc/systemd/system/<unit>.d/<key>`. Explicit paths must match these locations.
Template paths remain relative to the declaring service definition.

Unit/drop-in changes appear as file changes, schedule `systemctl daemon-reload`,
and request their process's restart reaction. A stopped process targeting
running receives only start. Ordinary configs at the matching owned-unit paths
also associate their changes with that process. [Lifecycle](../lifecycle.md#present-services)
covers daemon-reload timing and package/config triggers.

A `secret` call automatically makes the definition sensitive; actual secret
changes still schedule daemon reload and reactions. Set an explicit private
mode when needed. Use `sensitive: true` to protect old bytes after removing a
secret call or requesting absence. [File sensitivity](../configs/files.md#sensitive-files)
and [secrets](../../secrets.md) define the full protection contract.

## Reload capability

For a present assignment, every declared `reload_on` is checked during
preparation, even with no current input changes. Absent assignments skip this
check. Existing units use their effective fragment and drop-ins.
Prepared full units replace the fragment, matching drop-in basenames replace
existing drop-ins, and drop-ins apply in systemd filename order. The maintained
`coreos/go-systemd` parser reads the definitions. Empty `ExecReload=` resets are
respected; supported `ExecReload` or `Type=notify-reload` provides reload
capability. Reload never silently becomes restart. Apply also checks the unit's
current `CanReload` before executing a reload branch.

When an RPM and its unit are both absent, diff cannot inspect that future unit
without downloading it from a repository that may not yet exist. A first
installation declaring `reload_on` must therefore provide an owned full unit
or drop-in that establishes supported reload capability, such as the
`ExecReload` override above. Otherwise preparation reports that it cannot verify
reload. Restart-only reactions can use the package's unit directly.

## Removal and continuation

An `absent` service overrides process targets to stopped and disabled, including
for external units it does not remove. Already missing units are accepted.
It ignores the declared `masked` target and preserves an existing mask. Removing
an owned full-unit path while it is masked fails preparation: first unmask it
through a present assignment, then request removal.
Processes run in reverse dependency order before files and packages are removed;
see [absent services](../lifecycle.md#absent-services).

Failed commands or verification block the remainder of the host. Explicit
[saved-plan continuation](../../continuation.md) skips successful tasks and
retries unfinished lifecycle, enablement, or mask tasks using their prepared inputs.
