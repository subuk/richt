---
title: "Limitations and plan boundaries"
linkTitle: "Limitations"
description: "Understand supported backends, frozen-input boundaries, and deferred capabilities."
weight: 110
---

Richt currently implements [cached Linux host facts](facts.md), local/SSH transport, explicit noninteractive sudo,
strict local or HTTPS YAML inventory, pinned HTTPS services and templates,
RPM through DNF 4/YUM 4, DEB through APT, checksum-pinned public
HTTPS downloads and tar.gz archive trees, systemd and oneshot
processes, templates, plain files/directories, local users/groups, RAID1, linear LVM, XFS and
persistent mounts, and local YAML secret sources. See [installation](installation.md) and
[validation](validation.md) for requirements and verified platforms. YUM 3,
DNF 5, additional persistent process managers, private authenticated
artifact URLs, other archive formats, and external secret stores are unsupported.

[HTTPS preparation sources](sources.md) support scoped bearer/basic credentials
and an additional CA file through explicit local client settings. Remote
documents cannot choose local file references, client settings, or secret
bindings. URL queries, fragments, userinfo, HTTP, and recursive service
references are unsupported. Inspection can fetch inventory/service documents;
templates and secret values are read only for selected present preparation.

## Executable plans are prepared inputs

A [saved plan](files.md) fixes selected hosts, explicit connection inputs,
task order, operations, file bytes/patches/attributes, package requests, allowed
systemd process branches, and oneshot argv/timeouts/changed triggers. Apply does
not reread inventory/templates/secrets, compute a new diff,
or perform a general drift comparison. Template writes and removals verify
frozen prior contents/presence/type or recognize their own unfinished desired
result. Intervening content edits fail with a conflict; required old-state
backups must remain valid. Completed tasks are skipped without rechecking them on
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
- APT reads installed dpkg state at diff; repositories and dependency resolution
  are apply-time inputs. Install/version actions conditionally refresh shared
  metadata with the fixed 48-hour freshness policy. Native concrete package
  names and exact Debian versions are supported; virtual packages, architecture
  selectors, purge, autoremove, global upgrades and held/untrusted overrides
  are unsupported. See [DEB packages](services/binaries/deb.md).
- Download/archive apply requires target Python 3.8+, verified TLS and bytes
  matching the frozen SHA-256. Artifact destinations are exclusively managed;
  pre-existing resources are not adopted. Replaced artifact roots, unknown
  archive entries and changed entry types block update/deletion; tracked
  same-type child changes remain repairable and removable. Linux atomic rename support is required. See
  [downloads](services/binaries/download.md) and [archives](services/binaries/archive.md).
- Systemd process tasks read current state to select a prepared branch and verify the
  result. Running means systemd active, not application readiness.
- Oneshot executables and their dependencies must exist at apply time. Commands
  run only for prepared changed inputs, with no runtime-state convergence check.
- Typed storage and mount operations verify prepared identities and safety
  preconditions at apply. They reject substituted resources, conflicting
  signatures and unrelated consumers. Fstab edits require the frozen before
  or after contents; they do not overwrite intervening changes.

Template writes replace the final symlink with a regular file without changing
its referent; removals unlink the named entry. Atomic writes are the default,
with explicit `atomic_writes: false` retaining weaker in-place regular-file
writes. Atomic replacement changes only the named hardlink entry. Parent
symlinks remain followed. Content checks are not strict inode checks or
compare-and-swap; external writers can still race after verification. Opt-in
`backup: true` keeps independent prior-object copies and private receipts on
the target, with no automatic cleanup or rollback. See [file configs](services/configs/files.md). Plain file and
directory operations freeze the identity of an existing target and reject its
replacement. Recursive directory removal refuses active mount boundaries.
Named users/groups must exist or be created by preceding account configs.
File parents are created as needed, but their
permissions are not independently declared resources.

Account configs manage only local passwd/group identities; directory-backed NSS
accounts are conflict checks rather than editable resources. Renaming, existing
UID/GID changes, password/expiry management, and home-directory creation or
purging are unsupported. Supplementary groups are exact only when explicitly
listed; omission preserves them. User deletion preserves same-name groups using
a private mount namespace and requires that target capability. See
[local accounts](services/configs/accounts.md).

Facts cover OS, kernel, architecture, hostname, online logical CPUs, host-visible
RAM, interfaces/addresses and IPv4/IPv6 main-table defaults. They do not infer a
primary IP, perform DNS FQDN lookup, resolve policy routing/VRFs, inventory disks,
packages or services, run custom executable facts, or harvest environment/secrets.
Cache hits use configured identity; alias retargeting and reinstallation require
explicit refresh. Fact values affect preparation only.

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
application state. DEB removal retains conffiles and does not run autoremove.
Explicit directory, LV, VG and array removal has the
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
