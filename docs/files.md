---
title: "Saved plans and apply"
description: "Prepare frozen executable inputs, review changes, and apply with protected progress."
weight: 50
---

Use `diff` to prepare changes, review the displayed plan, then explicitly
`apply` it. Saving the plan preserves both executable inputs and task progress
for [manual continuation](continuation.md). [Inventory](inventory.md) defines
hosts and assignments; [services](services/_index.md) define their resources
and lifecycle. File templates and attributes are covered by
[file configs](services/configs/files.md).

## Prepare and review

Build with `make build`. The file example manages
`/var/tmp/richt-example/greeting.conf` on the local machine:

```sh
bin/richt inventory validate -i examples/files/inventory.yaml
bin/richt diff -i examples/files/inventory.yaml
bin/richt diff -i examples/files/inventory.yaml -o /var/tmp/richt-example-plan.json
bin/richt apply /var/tmp/richt-example-plan.json
```

Inventory inspection validates and resolves definitions without contacting
hosts, opening templates, or fetching secrets. Diff renders templates and
observes the targets to prepare operations without changing managed resources.
Missing template variables fail preparation. An `absent` assignment prepares
explicit removal without rendering templates or reading secret sources.

A saved plan fixes selected hosts, explicit connection inputs, task order,
operations, rendered file contents and patches, requested attributes and
owner/group names, file validators and their timeouts, atomic-write and backup
selections, operation IDs, explicit prior/desired file states, frozen backup
paths and prior metadata, package requests (including
their `disable_excludes` override), artifact URLs/checksums/paths and extraction
settings, artifact attributes/timeouts and ownership identities, masking targets, allowed systemd process
branches, and oneshot argv, timeouts, and
changed trigger references. Typed configs also fix path attributes, storage
identities and deletion scope, filesystem creation requests, and mount/fstab
operations. It contains progress for each task. Review its displayed actions, parameters, targets,
checks, and branches, and trust its source before applying it.

Local account operations also freeze the user/group name, explicit IDs and
attributes, membership intent, observed identity, and allowed before/after
states. Omitted optional fields remain unmanaged for existing accounts.

## Frozen inputs and execution boundaries

Apply uses those prepared inputs even after the inventory, templates, or secret
sources change or disappear. It does not reread them, render again, recalculate
changes, or perform a general drift comparison. Template writes and removals
verify frozen prior contents/presence/type or recognize their own unfinished
desired result, and require any prepared backup. Intervening content edits fail
with a conflict. Reprepare and review a new plan when you need
current observations or changed desired inputs.

Some operations deliberately use execution-time information:

- Owner/group names are resolved again on the target; numeric IDs may change.
- The system SSH client reads its current configuration. Explicit saved
  connection inputs remain fixed, but an alias can resolve to a new address.
- DNF resolves repository metadata, dependencies, and the transaction when the
  prepared package request runs. An exact version fixes that request, not the
  complete transaction or RPM checksums; RPM artifacts are not embedded.
- Downloads and archives fetch the frozen public HTTPS URL on the target and
  require its frozen SHA-256 before atomic publication. Artifact bytes are not
  embedded in JSON. Protected manifests and transaction records guard ownership,
  foreign entries and interrupted publication; these checks do not rebuild the plan.
- Template file operations verify the frozen prior contents, presence, type or
  symlink text before mutation. Attribute-only edits or identical bytes in a
  replacement inode are accepted. Atomic writes are the default; an explicit
  non-atomic selection is fixed in the plan. These checks are not compare-and-swap
  and do not prevent a separate writer racing after the check.
- File validators run against the prepared candidate during apply, after earlier
  package tasks and before writing the destination. They use the target's current
  executable and supporting files; those dependencies are not embedded in JSON.
- Systemd process tasks read current state to select an allowed prepared branch
  and verify its result. These checks do not rebuild the plan.
- Oneshot tasks execute their saved argv without rechecking linked changes.
  The target executable and its supporting files are used at apply time;
  executable bytes are not embedded in JSON.
- Plain files preserve existing contents, including a file that appeared after
  preparation. Directory operations manage only the directory's own attributes.
- Storage operations check prepared device/resource identities, signatures and
  consumers immediately before creation or deletion, then verify their results.
  An unfinished creation recognizes its own prepared UUID or operation tag;
  it cannot adopt an unrelated replacement or reformat an existing filesystem.
- Mount operations verify the prepared source identity and allowed active state.
  Their frozen fstab edits compose in declared config order, accepting only the
  expected before/after contents. Unexpected fstab edits fail instead of being
  overwritten. These preconditions do not recompute the diff.
- Local account operations verify local identity and NSS conflicts before
  mutation, then verify the requested result. Unfinished operations can recognize
  their prepared result after interruption. ID allocation when omitted still
  occurs on the target. Shadow utilities and local account databases remain
  execution-time dependencies; these checks do not rebuild the plan.

See [file configs](services/configs/files.md), [RPM binaries](services/binaries/rpm.md),
[process lifecycle](services/lifecycle.md), and [SSH](ssh.md) for these boundaries.
The [storage config reference](services/configs/storage.md) explains device,
filesystem, mount and destructive removal checks.
Unsupported transports and backends fail before changes.

## Confirmation and scope

Every apply displays the plan. Without `-y`, it requires a terminal and explicit
`y` or `yes`; empty or negative answers cancel. Piped input does not count as
terminal agreement. `-y` skips only confirmation, leaving result and
compatibility checks in place:

```sh
bin/richt apply -y /var/tmp/richt-example-plan.json
# Prepare and apply without saving progress:
bin/richt apply -y -i examples/files/inventory.yaml
```

The inventory option `-i` and host, tag, and service selectors are accepted during
plan construction and rejected when applying a saved plan. A saved plan already
fixes its scope.
`--parallelism N` can change the concurrency limit for any apply, including a
saved plan; it does not change its hosts, tasks, inputs, or progress.

## Progress, locking, and continuation

Task progress is recorded as running before execution, then success only after
result verification, or failure. Saved progress is written atomically to the
same JSON file before and after each task. The stable adjacent `.lock` file
prevents simultaneous applies while the JSON file is replaced. Keep the
sidecar; its existence alone does not mean an apply is running. Parent-directory
aliases resolve to the same lock. Hardlink aliases of saved plans are rejected.

A task failure blocks the rest of its host, including later services. Other
active and queued hosts continue. Explicitly apply the same plan again after
fixing the external cause: successful tasks are skipped without rechecking,
and unfinished tasks are retried in their original order. Interrupted tasks
may already have taken effect and can execute again; exactly-once execution is
not guaranteed. There are no automatic retries or rollback.

Hosts execute concurrently with a default limit of 20; `--parallelism 1` runs
sequentially. Tasks within each host remain ordered. A progress write failure
stops admission of further actions globally; already running operations are
awaited and may have taken effect. Normal completion reports completed,
previously completed, failed, and blocked tasks for all hosts, with reasons,
the saved path, and manual continuation instructions when applicable. Any
failure produces a nonzero exit. Forced termination may prevent the report.

Without a plan file, plan and progress live only in memory and disappear when
the invocation ends. A later invocation builds a new plan. There is no autosave,
separate progress store, run/attempt history, or automatic replay of completed
tasks. Follow the [continuation walkthrough](continuation.md) for a runnable
partial-failure example.

## Plan format and security

Saved plans use strict, indented JSON schema version `1.7`. This is a breaking
format change: all older versions, including `1.5` and `1.6`, are rejected before
execution with an instruction to prepare a new plan. There is no migration or
legacy execution path. Unknown fields, incompatible versions, invalid
operations, missing required file inputs, and malformed progress are rejected.

A file payload requires explicit `atomic_writes` and `backup` booleans, a stable
32-character lowercase hexadecimal `operation_id`, and `expected` and `desired`
states. Expected regular files retain a SHA-256 and metadata; expected symlinks
retain link text and metadata; absence uses `kind: absent`. Metadata contains
numeric UID/GID, mode and a map of base64 extended-attribute values. Desired
states contain the regular-file digest or explicit absence. `backup_path` is
required exactly when backup is selected for replacement/removal of an existing
object. [File configs](services/configs/files.md) explains target backups,
private adjacent receipts, continuation, link handling and durability errors.

Component task identifiers use the service, component kind (`binary`, `config`,
or `process`), original zero-based list position, and operation. For example, `app/process/0/enabled` identifies
the first process's enablement task; `app/config/1` identifies the second
config's file task. Names are display and reference labels, not identifier
inputs. Identifiers remain fixed in a saved plan, including during continuation;
reordering inventory items may change identifiers in a newly prepared plan.
Unnamed components display their one-based list position, such as `[1]`.
A oneshot task uses operation `oneshot`; its `oneshot` payload holds `command`,
`timeout`, and `triggers` containing the actual changed references. Services
can also include orchestration tasks such as daemon reload.
Operation `artifact` carries the pinned download/archive request, provenance
token, observed identity and safe publication preconditions. It is supported
in version `1.7`. Artifact bytes and extracted trees are absent from JSON.
Operations `path`, `storage`, and `mount` carry their corresponding typed
payloads. Preparation can account for a device or empty filesystem produced by
an earlier config without probing a resource that does not exist yet. Applying
that plan still follows its original list order and verifies the saved inputs.

Operation `account` carries local user/group requests, observed identity and
before/after preconditions, within the current version `1.7` schema.

Saved JSON contains real rendered text and patches, including old and new
secret bytes. Sensitive display does not remove them from executable inputs.
Plans, adjacent locks, and atomic replacements use mode 0600; restrict the
containing directory and keep plans and command captures private. Temporary
plan files are removed on completion or handled errors; forced termination
can leave a private temporary file behind.

Calling `secret` automatically makes a file sensitive, and `sensitive: false`
cannot cancel this. Use `sensitive: true` to protect old content after removing
a secret call or declaring the service absent. Sensitive displays hide the
entire old/new file diff, including context, and file operation error details;
actual bytes still determine changes and reactions. This also covers managed
units and drop-ins. No persistent secret history is kept.

Sensitivity controls display, while target permissions are separately declared.
New targets default to mode 0644; specify `mode: "0600"` for private files.
See [YAML secrets](secrets.md) and [file configs](services/configs/files.md) for
source lookup, safe diagnostics, target writes, and attributes. The
[isolated secret example](../examples/secrets/README.md) uses fictional values.

The [file acceptance harness](validation.md#file-acceptance-harness) records
commands, checks, and dated Linux evidence for saved inputs, progress, locking,
confirmation, file attributes, and sensitive output.
