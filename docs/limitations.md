---
title: "Limitations and plan boundaries"
linkTitle: "Limitations"
description: "Understand supported backends, frozen-input boundaries, and deferred capabilities."
weight: 110
---

Richt currently implements local/SSH transport, explicit noninteractive sudo,
strict local YAML inventory, RPM through DNF 4/YUM 4, systemd, local templates,
and local YAML secret sources. See [installation](installation.md) and
[validation](validation.md) for requirements and verified platforms. YUM 3,
DNF 5, APT, non-systemd managers, URL inventory/templates, direct binary
URL downloads, and external secret stores are unsupported.

## Executable plans are prepared inputs

A [saved plan](files.md) fixes selected hosts, explicit connection inputs,
task order, operations, file bytes/patches/attributes, package requests, and allowed process
branches. Apply does not reread inventory/templates/secrets, compute a new diff,
or check for drift. A file changed by another actor after diff can be overwritten
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
- Process tasks read current state to select a prepared branch and verify the
  result. Running means systemd active, not application readiness.

Ordinary file operations follow target and parent symlinks; removal unlinks the
named entry. There is no special symlink prohibition or inode identity contract.
Named users/groups must exist. File parents are created as needed, but their
permissions are not independently declared resources.

## Failure and removal

There are no automatic retries, rollback, autosaved plans, separate progress
stores, or run/attempt history. An interrupted operation can have taken effect
before its success was saved, and explicit continuation can execute it again.
A failed host blocks its remaining tasks while other hosts continue. See
[continuation](continuation.md).

Process command and verification polling share a positive duration, default
**5s**. Timeouts fail the task; an accepted systemd job may continue after the
client times out. Unsupported reload is an error and never falls back to
restart. Services follow service-definition order, and components follow their
list declaration order within each phase. There is no dependency graph or
automatic dependency selection.

`state: absent` removes explicitly owned resources in the
[service lifecycle order](services/lifecycle.md).
Deleting an assignment alone does not remove anything. DNF can remove dependent
packages and RPM can retain `.rpmsave` files. Richt does not purge application
data, state directories, unrelated files, or `.rpmsave`. It does not validate an
application's configuration before writing it.

## Secrets and future scope

Human-readable sensitive displays hide entire old/new file diffs and context;
raw JSON retains real rendered bytes and patches, including old secrets.
Plans and locks use 0600; target file permissions are separately configured.
Use `mode: "0600"` for private targets and `sensitive: true` when protecting old
content during removal or after removing a `secret` call. No persistent secret
history exists. See [secrets](secrets.md).

NetBox inventory and rendering integration, agents, central run history,
fleet rollout orchestration, additional package/process backends, application
configuration validation, and external secret stores remain later scope. They
are not prerequisites or shipped CLI features.
