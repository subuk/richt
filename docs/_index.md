---
title: "Documentation"
description: "Prepare, review, and explicitly apply Linux host configuration with Richt."
weight: 10
---

Richt is a standalone Go CLI for executable host configuration plans. Describe
hosts and reusable services in YAML, inspect the resolved inventory, prepare
changes against the targets, then review and explicitly apply them. Services
combine RPM packages, templated files, and systemd processes. Config-only,
package-only, and process-only services are also supported.

Start with the [config-only quickstart](quickstart.md), then use
[installation and platform requirements](installation.md) and the
[CLI reference](cli.md) for your environment. The [examples](examples.md) include
ready-package vmango, SSH, local secrets, and deliberate failure/continuation
scenarios. [Validation](validation.md) separates recorded Linux acceptance from
illustrative configurations.

The focused contracts are:

- [Inventory](inventory.md): strict YAML, tag assignments and variable order, paths, ordering,
  ownership, and filters.
- [Files and saved plans](files.md): templates, attributes, SELinux, confirmation,
  executable JSON, and locking.
- [RPM and systemd](services.md): exact versions, reactions, timeouts, unit files,
  and removal.
- [SSH and sudo](ssh.md): client configuration, authentication, privileges, and
  parallel hosts.
- [Secrets](secrets.md): source lookup, sensitive display, and real saved bytes.
- [Continuation](continuation.md): task progress, partial failure, and explicit
  retry using the same plan.

Read [limitations](limitations.md) before using a saved plan as an operational
artifact. Plans contain prepared executable inputs, potentially including
secrets; apply does not recompute changes or check targets for drift.
