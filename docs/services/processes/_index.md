---
title: "Processes"
description: "Manage runtime state and reactions independently of software and files."
weight: 50
---

Processes describe managed runtime instances in an ordered `processes` list
within a service. Each component may have a `name` label, independent of its
backend unit name.
A process-only service can manage a unit supplied outside Richt. A Richt service
may also contain several processes; it is not synonymous with one daemon.

The current implementation is [systemd](systemd.md), selected with
`type: systemd`. Its reference owns state, enablement, timeout, full-unit and
drop-in fields, reload capability, and backend limits.

[Change reactions](../reactions.md) link processes to changed binaries and configs.
[Lifecycle and ordering](../lifecycle.md) explains declaration order, execution
phases, and reverse process order during removal.
