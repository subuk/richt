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
| `richt facts [host/tag selection]` | Collect or inspect cached Linux host facts as deterministic JSON. |
| `richt diff [selection] [-o PLAN.json]` | Prepare and display changes by observing targets; optionally save the executable JSON plan. |
| `richt apply [selection] [-y] [--parallelism N]` | Build, display, confirm, and apply a new plan; progress lives only in memory. |
| `richt apply PLAN.json [-y] [--parallelism N]` | Display, confirm, execute, and update a saved plan; continue unfinished tasks on a later explicit invocation. |

Inventory inspection never contacts hosts, opens templates, or reads secret
source values. It may fetch HTTPS inventory and service documents. Diff can
contact targets and read local or pinned HTTPS templates and local secrets; it does not
modify managed resources. `-o` writes the plan file as an explicit output.

## Inventory and selection flags

| Flag | Meaning |
| --- | --- |
| `-i PATH_OR_URL`, `--inventory PATH_OR_URL` | Local YAML inventory or absolute HTTPS URL; defaults to `inventory.yaml` in the current directory. |
| `--inventory-sha256 HEX` | Optional expected SHA-256 of an HTTPS root inventory; rejected for local inventory. |
| `--source-config PATH` | Explicit local YAML with extra CA trust, scoped HTTPS credentials, and local YAML secret bindings. See [HTTPS sources](sources.md). |
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

`-i`, `--source-config`, `--inventory-sha256`, `--host`, `--tag`, and `--service`
are argument errors with a saved plan.
A saved plan already fixes its hosts and tasks. Build a new plan if you need a
different selection.

## Facts and preparation cache flags

`facts` accepts host/tag selection without `--service`, including unassigned
hosts, plus positive `--parallelism` (default 20), `--progress`, `--refresh`,
`--cached`, `--facts-cache-dir`, `--facts-max-age` (default 72h), and
`--no-facts-cache`. Cached inspection never contacts targets; stale snapshots
are marked explicitly. A host failure preserves the other host results in JSON
and makes the invocation fail. Cached mode conflicts with refresh and no-cache.

`diff` and inventory-based `apply` accept `--facts-cache-dir`,
`--facts-max-age`, `--refresh-facts`, and `--no-facts-cache`. Negative ages fail;
zero forces collection. These flags are rejected with saved plans, including
explicit values equal to their defaults. See [host facts](facts.md) for schema,
template lookup, cache isolation, failure policy, and target requirements.

## Apply flags and results

`facts`, `diff`, and `apply` accept `--progress=auto|plain|off` (default `auto`). Progress
goes to stderr; the reviewed plan and final report go to stdout. In a terminal,
`auto` maintains a compact display of counts, active host/task labels and elapsed
time, refreshed every second. Completion and failure messages remain in the
scrollback. The display fits the terminal; additional active tasks are counted
even when their rows do not fit.

With redirected stderr, `auto` uses ordinary lines, as does `plain`. Task events
appear immediately, with a reminder every 30 seconds while work is outstanding.
`off` suppresses intermediate progress. Preparation reports rendering,
capability checks and host/service observation, including for `diff`.

Elapsed time indicates that Richt is still waiting for an operation. It does
not prove remote activity or estimate completion time. Counts describe tasks,
not a percentage of work inside a package transaction, download or command.

```sh
richt apply -y --progress=plain plan.json >result.txt 2>progress.log
richt diff --progress=off -i inventory.yaml
```

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

Ctrl+C requests cancellation: Richt stops admitting further actions, waits for
active actions to return, and reports the resulting progress. A remote command
or systemd job may still have taken effect. Continue a saved plan explicitly
after checking the cause; see [continuation](continuation.md). A failure to save
progress is reported separately from execution success and makes the invocation
fail, even when an operation has already succeeded.

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
