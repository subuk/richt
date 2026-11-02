---
title: "Configs"
description: "Render and manage the files a service owns."
weight: 40
---

Configs describe service-owned configuration. Each component has a stable name
under `configs`, used by [process reactions](../reactions.md) such as
`config.main`. A config-only service can establish a repository before a later
service installs its package.

The current implementation is [files and templates](files.md). Config entries
use file fields directly; there is no `type` field. The reference covers paths,
templates, permissions, SELinux, sensitivity, preparation, and removal.
[Systemd-owned units and drop-ins](../processes/systemd.md#owned-units-and-drop-ins)
use these same config fields.

Configs run after binaries and before processes for present services. See
[lifecycle and ordering](../lifecycle.md) for execution and removal phases.
