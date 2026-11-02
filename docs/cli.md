---
title: "CLI reference"
description: "Commands, flags, selection, confirmation, concurrency, and exit status."
weight: 30
---

Run `richt --help` or `richt COMMAND --help` for generated help. Examples below
use `richt` on `PATH`; a source build is available as `./bin/richt`.
With no command, Richt prints help. `richt --version` prints the build version.

| Command | Behavior |
| --- | --- |
| `richt inventory validate [selection]` | Load and validate YAML, references, merged variables, selected assignments, and resource ownership. |
| `richt inventory show [selection]` | Perform the same validation and print resolved inventory as indented JSON. |
| `richt diff [selection] [-o PLAN.json]` | Prepare and display changes by observing targets; optionally save the executable JSON plan. |
| `richt apply [selection] [-y] [--parallelism N]` | Build, display, confirm, and apply a new plan; progress lives only in memory. |
| `richt apply PLAN.json [-y] [--parallelism N]` | Display, confirm, execute, and update a saved plan; continue unfinished tasks on a later explicit invocation. |

Inventory inspection never contacts hosts, opens templates, or reads secret
sources. Diff can contact targets and read local templates/secrets; it does not
modify managed resources. `-o` writes the plan file as an explicit output.

## Inventory and selection flags

| Flag | Meaning |
| --- | --- |
| `-i PATH`, `--inventory PATH` | YAML inventory; defaults to `inventory.yaml` in the current directory. |
| `--host NAME` | Select named hosts; repeat or comma-separate names. |
| `--tag NAME` | Select hosts carrying a tag; repeat or comma-separate names. |
| `--service NAME` | Select already assigned services; repeat or comma-separate names. |

Host names and tags form a union, repeated services form another union, and the
two groups intersect. An unspecified group selects everything. Matching is
exact and case sensitive; unknown names fail. Service selection does not add
assignments or pull in prerequisite services. Full schema validation still
includes unselected definitions and unused tag assignments. Every selected host
uses all its tags; duplicate assignments, service-order collisions, ownership
and variable conflicts cannot be hidden by a service filter. See [inventory](inventory.md).

```sh
richt inventory show -i fleet.yaml --host app-a --tag staging --service web
richt diff -i fleet.yaml --host app-a --service repository --service web -o plan.json
richt apply -i fleet.yaml --tag staging --parallelism 2
```

`-i`, `--host`, `--tag`, and `--service` are argument errors with a saved plan.
A saved plan already fixes its hosts and tasks. Build a new plan if you need a
different selection.

## Apply flags and results

`-y` or `--yes` skips Richt's confirmation prompt. Otherwise apply requires a
terminal and explicit `y` or `yes`; empty/negative responses cancel. Piped
input is not terminal agreement. The plan is displayed in both modes. This flag
does not change privileges, SSH authentication, or compatibility/result checks.

`--parallelism N` accepts a positive integer and defaults to 20 simultaneous
hosts. `1` runs hosts sequentially. The flag applies to both new and saved plans;
it is execution configuration and does not rebuild tasks or change progress.
Services/tasks remain sequential within a host. A failed task blocks that
host's remaining tasks while other hosts continue. See [SSH and parallel
apply](ssh.md) for persistence and cancellation details.

The final report includes per-host completed, previously completed, failed, and
blocked tasks, plus the saved path and manual continuation instruction when
applicable. Forced termination can prevent a final report.

Successful commands exit 0. Kong argument-parsing errors exit 2; execution,
validation, compatibility, cancellation, and task failures exit nonzero
(currently 1). Do not infer success from a partial apply display: check the
process exit status and final results.

```sh
richt apply -y --parallelism 2 plan.json
# After fixing the cause of a failure, explicitly continue the same plan:
richt apply -y --parallelism 1 plan.json
```

There is no automatic saved plan for `apply -i`, separate progress store, retry,
rollback, or run history. Use `diff -o` before applying when persistent
[continuation](continuation.md) is required.
