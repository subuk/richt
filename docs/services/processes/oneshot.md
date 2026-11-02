---
title: "oneshot"
description: "Run a prepared command once when linked components change."
weight: 20
---

```yaml
processes:
  - name: apply-sysctl
    type: oneshot
    command: [/usr/sbin/sysctl, -p, /etc/sysctl.d/90-richt-example.conf]
    run_on: [config.sysctl]
    timeout: 5s
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `name` | no | Nonempty optional label, unique within the process list |
| `type` | yes | `oneshot` |
| `command` | yes | Nonempty argv list; the first item is the target executable and cannot begin with `-` |
| `run_on` | yes | Nonempty list of same-service `binary.<name>` or `config.<name>` references |
| `timeout` | no | Positive Go duration; default `5s` |

Only explicitly named binaries and configs can be referenced. Unknown,
duplicate, cross-service, and process references fail validation. Systemd
fields such as `unit`, `state`, `enabled`, `masked`, `restart_on`, `reload_on`,
`unit_file`, and `overrides` are invalid for this backend.

## Changes and ordering

For a present service, diff schedules one command task if at least one linked
component actually changes. Config creation, content changes, and attribute
changes all count. Multiple changed references still produce one execution;
the plan records only those references that changed. Unchanged linked inputs
produce no task, even if an earlier invocation of the command failed.

The task runs after that service's binaries and configs, in the declared
process-list order. It can be mixed with [systemd](systemd.md) processes.
A oneshot needs neither systemd nor a managed unit and does not by itself cause
a daemon reload after a package change. An `absent` service schedules no
oneshot command, including when its linked configs or packages are removed.
See [lifecycle](../lifecycle.md) and [change reactions](../reactions.md).

## Execution and saved plans

Inventory inspection and diff never execute the command or probe its
executable. Apply executes the saved argv directly on the target. There is no
implicit shell: `$HOME`, pipes, redirection, wildcards, and command substitution
are literal arguments. To use shell syntax, explicitly invoke an appropriate
shell in `command`. Use absolute paths for executables and supporting files.
Local commands inherit the controller working directory; SSH commands use the
remote login directory. To execute a file whose basename starts with `-`, use
an explicit path such as `./-name`. The executable and any supporting scripts or files must be
available on the target when apply runs; they are not embedded in the plan.

Exit status zero completes the task. A nonzero exit, missing executable,
transport error, or timeout fails it and blocks later tasks on that host.
Command stdout and stderr are omitted from displays and saved error messages,
including on failure. The command argv itself is reviewable in the plan and
its display; do not put secret values in command arguments.

A saved plan freezes argv, timeout, and changed trigger references. Applying it
does not reread inventory or recheck triggers, so deleting the inventory after
diff does not cancel the command. Successful tasks are skipped during explicit
continuation, including unnamed processes. Failed or interrupted tasks are
retried using the original prepared command. There are no automatic retries or
rollback, and interrupted effects may repeat. A timeout over SSH does not
guarantee that a remote command or its children have terminated.

If a config write succeeds and the command then fails, a fresh diff can be
empty because the config already matches. Continue the original saved plan
after fixing the external cause to retry that pending command. See
[saved plans and apply](../../files.md) and [manual continuation](../../continuation.md).

The [sysctl walkthrough](../../../examples/oneshot/README.md) uses the native
backend without a systemd unit. The [acceptance harness](../../validation.md#oneshot-process-acceptance)
checks triggers, ordering, frozen commands, failure, continuation, and safe
diagnostics on a disposable Linux target.
