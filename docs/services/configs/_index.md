---
title: "Configs"
description: "Render and manage the files a service owns."
weight: 40
---

Configs describe service-owned configuration in an ordered `configs` list.
An optional `name` label allows [process reactions](../reactions.md) such as
`config.main` to reference an item. A config-only service can establish a
repository before a later service installs its package.

The current implementation is [files and templates](files.md). Config entries
use file fields directly; `type` defaults to `template`, and explicit
`type: template` is also supported. The reference covers paths,
templates, permissions, SELinux, sensitivity, preparation, and removal.
[Systemd-owned units and drop-ins](../processes/systemd.md#owned-units-and-drop-ins)
use these same config fields.

Configs run in declaration order after binaries and before processes for
present services. See [lifecycle and ordering](../lifecycle.md) for execution and removal phases.
