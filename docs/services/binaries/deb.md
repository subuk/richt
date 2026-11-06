---
title: "DEB packages"
description: "Manage installed Debian packages and exact versions through APT."
weight: 15
---

```yaml
binaries:
  - name: package
    type: deb
    package: tree
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `name` | no | Nonempty reference label, unique within the binary list |
| `type` | yes | `deb` |
| `manager` | no | `apt` (the only supported DEB manager) |
| `package` | yes | Native Debian binary package name; no architecture selector or virtual package |
| `version` | no | Exact Debian version, including any epoch and revision; omission preserves an installed version |

Quote version values in YAML. A Debian version is an exact request; there is no
RPM-style upstream-only version matching. Explicit versions permit upgrades and
downgrades. Names and versions are validated as individual command arguments.
APT resolves repositories and dependencies on the target during apply; the
request does not freeze a complete transaction or package bytes.

## Preparation and no-op behavior

Inventory inspection validates syntax without contacting hosts. Diff checks
required backend capabilities and queries installed dpkg state. It does not
update metadata, query repositories, fetch packages, or check whether a requested
version is available. A preceding config-only service can therefore create an
APT source file that does not yet exist at diff time.

With no version, an installed package is a no-op, even if repositories offer a
newer version. With a version, Debian version comparison determines whether the
installed version matches. An already absent package is a no-op for removal.
No-op package requests do not trigger a process reaction.

## Conditional metadata refresh at apply

Each install or version-change action checks the target's APT metadata immediately
before its transaction. It refreshes metadata when there is no successful shared
refresh stamp, the stamp is older than 48 hours, APT source/configuration
inputs (including signing keys) have changed, or package list files have changed
or been deleted. The protected
stamp is shared across packages, services and Richt runs on that target.
A source change made by an earlier service is seen by the next package action.

This conditional check and possible `apt-get update` belong to the reviewed
package action; there is no separate inventory cache resource. A failed refresh
fails the task and blocks later work on that host. Removal and no-op diffs do
not refresh metadata. Concurrent Richt actions serialize the shared check and
refresh. A protected root-owned directory at `/var/lib/richt/apt` contains the
shared lock and successful-refresh stamp; the directory uses mode `0700`, and
its lock/stamp files use `0600`. The directory remains after package removal.
External package managers use their own native locks. Richt waits for those
locks, then lets APT acquire them for its transaction; an external operation
that races this handoff can still fail the task. There is no automatic retry.

The transaction runs `apt-get` noninteractively with the frozen package/version.
Prepared requests retain fixed policies: a 48-hour cache age, 60-second lock
wait, 15-minute total timeout, and `keep` conffile policy that preserves local
edits. These policies are executable plan fields, not inventory options.
Repository signature and trust policy remain enforced by APT. Richt does not
request purge, autoremove, a global upgrade, held-package overrides, or
untrusted-package overrides. APT dependencies and maintainer scripts still have
their normal target-side effects.

## Lifecycle, saved plans and removal

DEB requests share declaration ordering and `binary.<name>` reactions with RPM
and artifact binaries. A changed package request can supply a missing systemd
unit and schedules daemon reload when the service manages systemd processes.
A `reload_on` declaration still needs verifiable reload capability before the
package is installed; supply an owned unit or drop-in when necessary. See
[lifecycle](../lifecycle.md) and [reactions](../reactions.md).

A saved plan freezes the request and policies. Apply uses those fields without
rereading inventory; repository/source state and dependency resolution remain
execution-time inputs. Explicit continuation skips successful package tasks and
retries unfinished ones. See [saved plans and apply](../../files.md).

An `absent` service removes the named package after its processes and configs,
in reverse binary declaration order. Removal does not purge conffiles or run
autoremove. Application data and unrelated files remain outside the request.

## Target layout and execution limits

Targets need Linux, APT 2.2+, `apt-cache`, `apt-config`, dpkg/dpkg-query and
Python 3.8+; only services selecting DEB binaries need these capabilities.
Apply requires root and the standard APT/dpkg directories, including `/etc/apt`,
`/var/lib/apt/lists`, `/var/lib/dpkg/status`, and `/var/cache/apt/archives`.
Redirected APT directory settings and the `APT_CONFIG`, `DPKG_ROOT`, or
`DPKG_ADMINDIR` environment variables are unsupported. Configuration and signing
keys must be root controlled; symlinks are accepted only through protected
root-controlled paths. An insecure or unsupported layout fails explicitly.

Configuration/key fingerprinting is bounded to 64 MiB and 8,192 entries, with a
maximum traversal depth of 32. Captured machine metadata is bounded to 32 MiB.
Inputs exceeding these limits fail instead of being truncated or ignored.
Package output is discarded; errors expose fixed categories without repository
credentials or command diagnostics.

The timeout covers helper work, lock waits, conditional refresh, transaction and
verification. The running helper terminates package command groups on timeout.
An SSH disconnect or controller cancellation cannot guarantee remote termination;
inspect the target before explicitly continuing an interrupted package task.

See [installation](../../installation.md)
and [validation](../../validation.md) for runtime requirements and recorded
acceptance scope. The [repository-package example](../../../examples/deb/README.md)
uses a published Debian package rather than a custom fixture.
