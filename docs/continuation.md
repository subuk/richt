---
title: "Progress and manual continuation"
linkTitle: "Continuation"
description: "Understand partial failure and explicitly retry unfinished saved-plan tasks."
weight: 90
---

Save a plan before applying when you need progress to survive the invocation:

```sh
./bin/richt diff -i inventory.yaml -o plan.json
./bin/richt apply plan.json
# Fix the external cause of a failure, then explicitly continue:
./bin/richt apply plan.json
```

The [runnable config-only example](../examples/continuation/README.md) deliberately
fails after an earlier file succeeds, fixes the local filesystem cause, and
continues the same plan. It needs no package installation or process manager.
Follow its commands and cleanup instructions in a fresh temporary directory.

## What is recorded

Each task records running before execution, then success only after its result
checks pass, or failure. Saved progress is atomically written to that same JSON
file. A stable adjacent `.lock` file prevents simultaneous applies of the same
plan; keep it in place. Its mere existence does not mean a process holds the
lock. Plan, lock, and atomic replacement files use mode 0600. Parent-directory
aliases resolve to the same lock; hardlink aliases of plans are rejected.

If a task fails, the rest of that host is blocked, including later services.
Other active and queued hosts continue. A later explicit apply skips successful
tasks and retries unfinished tasks in their original order. A successful config
write is therefore not repeated just to reach a failed process reaction.

An interrupted task may already have changed its target and can run again.
Exactly-once execution is not guaranteed. A failure to save progress stops
admission of further actions globally; in-flight operations are awaited and may
already have taken effect. Forced termination may leave private temporary plan
files behind and may prevent the final report.

## Frozen inputs and checks

Continuation uses the original prepared inputs. It does not reread inventory,
templates, or secret sources, rerender files, recalculate differences, or check
for intervening resource drift. Successful tasks are not rechecked. Editing the
inventory after a failure does not change a saved task. Fix an external
prerequisite and continue, or deliberately prepare a new plan when the desired
inputs have changed; review that new plan separately.

If a oneshot fails after its linked config was written, a fresh diff may have
no changes and therefore no command task. Continue the saved plan to retry the
prepared command. Its successful named and unnamed tasks remain skipped.

Systemd process state reads for a prepared branch and result verification still
happen during execution. Owner/group names are resolved again on the target. SSH uses
the system client's current configuration, including an alias's current address.
The saved explicit connection inputs remain fixed. DNF resolves dependencies
and repository transactions when its prepared package request executes. These
are the documented boundaries of frozen inputs; see [saved plans and apply](files.md),
[service lifecycle](services/lifecycle.md), and [SSH](ssh.md).

## Confirmation and concurrency

Every invocation displays the plan and requires terminal agreement unless
`-y` is supplied. Piping a response is not agreement; `-y` only skips the prompt.
You can change `--parallelism` for continuation, including setting it to 1,
without changing selected hosts, tasks, or saved inputs. Inventory/host/tag/
service filters are rejected with a saved plan.

Without a plan file, `apply -i inventory.yaml` holds plan and progress only in
memory. Nothing is automatically saved; a later invocation prepares a new plan.
Richt has no automatic retries, rollback, separate progress database, attempt
history, or automatic replay of completed tasks.
