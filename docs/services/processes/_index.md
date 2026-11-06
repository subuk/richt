---
title: "Processes"
description: "Manage runtime state and reactions independently of software and files."
weight: 50
---

Processes describe managed runtime instances and change-triggered commands in
an ordered `processes` list within a service. Each component may have a `name` label, independent of its
backend unit or executable name.
A process-only service can manage a unit supplied outside Richt. A Richt service
may also contain several processes; it is not synonymous with one daemon.

Select [systemd](systemd.md) with `type: systemd` to manage service and socket unit
state, enablement, masks, full units, drop-ins, and reload capability.
Select [oneshot](oneshot.md) with `type: oneshot` to execute a prepared argv
once when linked binaries or configs change. A oneshot does not need systemd.
Both backends share process declaration order and saved-task progress.

[Change reactions](../reactions.md) link processes to changed binaries and configs.
[Lifecycle and ordering](../lifecycle.md) explains declaration order, execution
phases, and reverse process order during removal.
