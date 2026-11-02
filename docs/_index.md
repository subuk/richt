---
title: "Documentation"
description: "Prepare, review, and explicitly apply Linux host configuration with Richt."
weight: 10
---

Richt is a standalone Go CLI for Linux host configuration. Its two core
concepts are [Inventory](inventory.md) and [Services](services/_index.md).
Inventory describes hosts, tags, variables, and assignments. A service groups
ordered lists of binaries, configs, and processes into a reusable unit; any
nonempty combination is valid. Tags assign a service's target state to hosts, while the
service definition sets execution order.

Inspect the resolved inventory without contacting hosts. Then use `diff` to
prepare changes against the targets, review the executable plan, and explicitly
`apply` it. The [config-only quickstart](quickstart.md) walks through that flow
without root privileges. Use [installation and platform requirements](installation.md)
and the [CLI reference](cli.md) for your environment. The [examples](examples.md)
include ready-package vmango, SSH, local secrets, failure/continuation, and
change-triggered [oneshot commands](services/processes/oneshot.md).
[Validation](validation.md) records Linux acceptance and its coverage limits.

The focused guides are:

- [Inventory](inventory.md): strict YAML, tag assignments and variable order,
  paths, service order, resource ownership, and selection.
- [Services](services/_index.md): composition, [lifecycle](services/lifecycle.md),
  and [change reactions](services/reactions.md).
- [Binaries](services/binaries/_index.md), [configs](services/configs/_index.md),
  and [processes](services/processes/_index.md): component concepts and the
  current [RPM](services/binaries/rpm.md), [file](services/configs/files.md),
  [systemd](services/processes/systemd.md), and [oneshot](services/processes/oneshot.md) backends.
- [Saved plans and apply](files.md): frozen executable inputs, review,
  confirmation, protected JSON, and locking.
- [Continuation](continuation.md): task progress, partial failure, and explicit
  retry using the same plan.
- [SSH and sudo](ssh.md): client configuration, authentication, privileges,
  and parallel hosts.
- [Secrets](secrets.md): source lookup, sensitive display, and real saved bytes.

Read [limitations](limitations.md) before using a saved plan as an operational
artifact. Plans contain prepared executable inputs, potentially including
secrets; apply does not recompute changes or check targets for drift.

For contributors, [CI and releases](ci.md) explains the shared checks, binary
archives, manual publication, and required repository protection settings.
