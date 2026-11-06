---
title: "Local users and groups"
description: "Manage local account identities, attributes, and exact or additive supplementary memberships."
weight: 30
---

`user` and `group` configs manage Linux local accounts in the same ordered
`configs` list as files and storage. They use the service assignment's
`present` or `absent` state. A component `name` is only a reaction label;
`user` and `group` identify accounts independently.

```yaml
configs:
  - name: primary-group
    type: group
    group: example-app
    system: true
  - name: identity
    type: user
    user: example-app
    group: example-app
    system: true
    home: /nonexistent
    shell: /usr/sbin/nologin
    comment: "Example application"
    supplementary_groups: []
  - type: directory
    path: /var/lib/example-app
    owner: example-app
    group: example-app
    mode: "0750"
```

## Fields and existing accounts

| Field | Type | Meaning |
| --- | --- | --- |
| `group` | Both, required | Group to manage for `group`; explicit primary group for `user` |
| `user` | User, required | User to manage |
| `gid` | Group | Optional numeric GID; allocation is automatic when creating without it |
| `uid` | User | Optional numeric UID; allocation is automatic when creating without it |
| `system` | Both | Optional boolean selecting the system ID allocation range during creation |
| `home` | User | Optional absolute home path recorded in the account database |
| `shell` | User | Optional absolute login-shell path |
| `comment` | User | Optional comment; `""` explicitly clears it |
| `supplementary_groups` | User | Optional list of local supplementary groups; exact unless `append_groups: true` |
| `append_groups` | User | Optional boolean; true adds the listed memberships while preserving others; false or omission manages the exact list |

Omitted home, shell, comment, and supplementary groups preserve existing
attributes. New users default to home `/nonexistent`, shell
`/usr/sbin/nologin`, an empty comment, and no supplementary memberships.
`system` affects only creation; it does not convert existing accounts or impose
an ID range on an explicit UID/GID. Existing UID/GID changes and account renames
are unsupported and fail preparation. Numeric IDs range from 1 through
4294967294. Richt does not manage `root`, users observed with UID zero, or
groups observed with GID zero.

Names are at most 32 bytes: start with an ASCII letter or underscore, followed
by letters, digits, underscores, dots or hyphens, optionally ending in `$`.
Numeric names and leading hyphens are rejected. Home and shell are clean
absolute non-root paths; colons and control characters are prohibited in paths
and comments. Explicit empty home/shell, null values, duplicate supplementary
groups, and fields belonging to another config type are errors. Group configs
have no `members` field. Declare membership through user configs.

Richt does not request home creation, movement, copying or deletion. A home path
is an account attribute; declare a separate directory config when needed.
Changing an existing user's primary group uses `usermod --gid`; standard shadow
utilities may update group ownership of files inside that user's home as a
side effect. Files elsewhere are not recursively repaired.
Passwords, password hashes, SSH keys, account expiry and login policy are outside
this resource. New users have no configured usable password; this is not a
general access-policy mechanism. Mail-spool and subordinate-ID allocation follow
the target shadow-utils defaults. User deletion does not request mail-spool
purging.

## Local scope and ordering

Observation uses `getent -s files passwd` and `getent -s files group` to select
the local databases, plus normal NSS lookups to detect name or numeric-ID
conflicts with directory-backed identities. Richt cannot adopt or modify an
NSS-only account. The target must provide GNU/glibc-compatible `getent` with
`-s files` and the shadow utilities `useradd`, `usermod`, `userdel`, `groupadd`,
and `groupdel`. Local and SSH transports use the same account behavior and
explicit [noninteractive sudo](../../ssh.md) boundary.

The primary group and every declared supplementary group must be local groups
or outputs of preceding group configs. Supplementary membership manages only
the local group database and never edits directory-service groups. Omission
preserves supplementary memberships; a supplied list replaces them exactly
unless `append_groups: true` is set. An explicit `append_groups` value requires
`supplementary_groups`, even when the flag is false. An empty list clears local
supplementary memberships in exact mode and leaves them unchanged in append
mode. Primary group membership is separate and does not implicitly satisfy a
requested supplementary membership.

For example, grant a service account access to an existing local `libvirt`
group while retaining memberships assigned by administrators or other software:

```yaml
configs:
  - type: user
    user: vmango
    group: vmango
    supplementary_groups: [libvirt]
    append_groups: true
```

The primary `vmango` group and supplementary `libvirt` group must exist locally
or be created by earlier configs. For a new user, the listed memberships are
set during creation. For an existing user, append mode uses
`usermod --append --groups` and verifies that all requested memberships exist;
additional memberships are allowed. A fresh diff is empty once the requested
attributes and memberships match. Restarting a running service to acquire its
new credentials belongs to the service recipe; named account configs can use
the usual process reactions.

Declare groups before users, then files/directories owned by those users and
groups. Preparation projects preceding account changes, so a file can refer to
a user whose creation will run earlier in the plan. Missing prerequisites and
invalid order fail rather than introducing dependencies or reordering configs.
Each selected host has one owner per managed user name and group name, and
explicit UID/GID claims cannot be duplicated within their respective namespaces.
These checks include all assigned services before service filtering, including
absent assignments. A user and group may share a name or numeric ID.
Whole-file ownership of `/etc/passwd`, `/etc/shadow`, `/etc/group`, or
`/etc/gshadow` conflicts with declaring account configs on the same host.

Binaries execute before configs in a service. To create an account before an
RPM transaction that needs it, put the account in an earlier service and select
both services. Download/archive ownership observation requires accounts to
exist on the target; apply account creation separately before preparing those
artifacts. Config declaration order cannot move an account ahead of that
service's binaries. Named account changes can drive the usual
[config reactions](../reactions.md).

## Removal and saved plans

Absence traverses configs in reverse declaration order. Declare a group before
its users so those users are removed first. User removal never requests home
purging or implicitly deletes a same-name group. Only a separately declared
group config requests group removal. A group still used as a primary group is
not removed; no forced removal is attempted. Account removal leaves files
owned by the old numeric identity unchanged.

User deletion runs `userdel` in a private mount namespace with a private
`login.defs` view that disables implicit user-group deletion. It needs util-linux
`unshare` and `mount` plus permission to create that namespace; absence
preparation probes the private bind mount. Container root can lack the required
mount namespace capability (`CAP_SYS_ADMIN`). The host's login policy remains
unchanged.

Preparation observes local state and freezes the requested account operation,
identity, attributes, membership intent, and safety preconditions. Apply does
not reload inventory or prepare a new diff. It checks the saved preconditions
and NSS conflicts before mutation, then verifies the resulting local account
and explicitly managed attributes. Append mode freezes the requested additions,
not a copy of the user's observed group set, so unrelated memberships added
between preparation and apply survive. Its result check and unfinished-task
continuation allow additional memberships. Unexpected replacement accounts or changed
identities fail safely. An unfinished task can recognize its requested result
after interruption; automatic ID allocation still occurs on the target.
A creation retry accepts an existing account that satisfies the frozen
requested attributes and membership mode. It cannot distinguish its own interrupted creation from
an independently created matching account, especially when the numeric ID was
allocated automatically.

Successful tasks are skipped during explicit saved-plan continuation.
Unfinished tasks may execute again after an interrupted command or progress
write. Account tools and their supporting databases must remain available;
there are no automatic retries, rollback, password management, or exactly-once
guarantees. See [saved plans and apply](../../files.md) and the
[account example](../../../examples/accounts/README.md).
