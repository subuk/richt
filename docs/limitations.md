---
title: "Limitations and plan boundaries"
linkTitle: "Limitations"
description: "Understand supported backends, frozen-input boundaries, and deferred capabilities."
weight: 110
---

Richt currently implements local/SSH transport, explicit noninteractive sudo,
strict local YAML inventory, RPM through DNF 4/YUM 4, systemd and oneshot
processes, templates, plain files/directories, RAID1, linear LVM, XFS and
persistent mounts, and local YAML secret sources. See [installation](installation.md) and
[validation](validation.md) for requirements and verified platforms. YUM 3,
DNF 5, APT, non-systemd managers, URL inventory/templates, direct binary
URL downloads, and external secret stores are unsupported.

## Executable plans are prepared inputs

A [saved plan](files.md) fixes selected hosts, explicit connection inputs,
task order, operations, file bytes/patches/attributes, package requests, allowed
systemd process branches, and oneshot argv/timeouts/changed triggers. Apply does
not reread inventory/templates/secrets, compute a new diff,
or perform a general drift comparison. A template file changed after diff can be overwritten
by the prepared write. Completed tasks are skipped without rechecking them on
continuation. Reprepare and review when you want current observations.

These inputs retain defined execution-time dependencies:

- OpenSSH reads current client configuration and credentials. An alias may now
  resolve to a different address; the plan display states this policy.
- Owner/group names are resolved again on the target, and their numeric IDs may
  have changed.
- DNF resolves repository metadata, dependencies, and the transaction at apply.
  A pinned NEVRA fixes the explicit package request, not RPM checksums,
  scriptlets, every dependency, or repository trust configuration. RPM artifacts
  are not embedded in the plan.
- Systemd process tasks read current state to select a prepared branch and verify the
  result. Running means systemd active, not application readiness.
- Oneshot executables and their dependencies must exist at apply time. Commands
  run only for prepared changed inputs, with no runtime-state convergence check.
- Typed storage and mount operations verify prepared identities and safety
  preconditions at apply. They reject substituted resources, conflicting
  signatures and unrelated consumers. Fstab edits require the frozen before
  or after contents; they do not overwrite intervening changes.

Template operations follow target and parent symlinks; removal unlinks the
named entry. Templates have no special inode identity contract. Plain file and
directory operations freeze the identity of an existing target and reject its
replacement. Recursive directory removal refuses active mount boundaries.
Named users/groups must exist. File parents are created as needed, but their
permissions are not independently declared resources.

## Failure and removal

There are no automatic retries, rollback, autosaved plans, separate progress
stores, or run/attempt history. An interrupted operation can have taken effect
before its success was saved, and explicit continuation can execute it again.
A failed host blocks its remaining tasks while other hosts continue. See
[continuation](continuation.md).

Systemd command and verification polling share a positive duration, default
**5s**. Timeouts fail the task; an accepted systemd job may continue after the
client times out. Oneshot commands have their own positive timeout, default 5s;
an SSH timeout does not guarantee remote termination. Unsupported reload is an
error and never falls back to restart. Services follow service-definition order, and components follow their
list declaration order within each phase. There is no dependency graph or
automatic dependency selection.

`state: absent` removes explicitly owned resources in the
[service lifecycle order](services/lifecycle.md).
Deleting an assignment alone does not remove anything. DNF can remove dependent
packages and RPM can retain `.rpmsave` files. RPM removal does not purge unrelated
application state. Explicit directory, LV, VG and array removal has the
destructive scope described by the [config types](services/configs/_index.md),
including data on adopted resources. Optional template validators check a
prepared candidate before writing; there is no multi-file validation transaction.

Storage supports RAID1 metadata 1.2, ordinary linear LVM, and XFS only. There is
no partitioning, resize, reshape, thin provisioning, snapshot management or
secure erasure. Raw initialization requires a stable hardware or loop backing
identity. Existing inactive resources must be activated before preparing their
filesystem state; existing unmounted filesystems must be mounted before
preparing their file contents. See [storage](services/configs/storage.md).

## Secrets and future scope

Human-readable sensitive displays hide entire old/new file diffs and context;
raw JSON retains real rendered bytes and patches, including old secrets.
Plans and locks use 0600; target file permissions are separately configured.
Use `mode: "0600"` for private targets and `sensitive: true` when protecting old
content during removal or after removing a `secret` call. No persistent secret
history exists. See [secrets](secrets.md).

NetBox inventory and rendering integration, agents, central run history,
fleet rollout orchestration, additional package/process backends, multi-file
configuration validation, and external secret stores remain later scope. They
are not prerequisites or shipped CLI features.
