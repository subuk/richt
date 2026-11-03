---
title: "Configs"
description: "Manage persistent resource configuration in declaration order."
weight: 40
---

Configs describe service-owned configuration in an ordered `configs` list.
An optional `name` label allows [process reactions](../reactions.md) such as
`config.main` to reference an item. A config-only service can establish a
repository before a later service installs its package.

| Type | Managed resource |
| --- | --- |
| `template` (default) | [Rendered files](files.md), validators, attributes, SELinux, and sensitivity |
| `file` | [Regular-file existence](paths.md) and optional attributes, preserving existing contents |
| `directory` | [Directory existence](paths.md) and its own optional attributes |
| `mdadm` | [RAID1 array](storage.md) with explicit members and optional XFS |
| `lvm-vg` | [Volume group](storage.md) and backing physical volumes |
| `lvm-lv` | [Linear logical volume](storage.md) with explicit size and optional XFS |
| `mount` | [Active mount and persistent fstab entry](mounts.md) |

Each type accepts only its own fields. Omitting `type` selects `template` and
requires a template source. A component `name` is a reaction label; `path`,
`array`, `vg`, `lv`, and `target` identify the managed resource independently.
[Systemd-owned units and drop-ins](../processes/systemd.md#owned-units-and-drop-ins)
accept only template fields.

Configs run in declaration order after binaries and before processes for
present services. See [lifecycle and ordering](../lifecycle.md) for execution and removal phases.
