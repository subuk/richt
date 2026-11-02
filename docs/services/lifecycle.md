---
title: "Lifecycle and ordering"
description: "Understand ordered component lists, service phases, removal, and failure."
weight: 10
---

Each assigned service runs in ascending service-definition `order`, including
`absent` assignments. Orders must be unique among effective services on each
host; services never combined on a host may share an order. Explicit zero is
valid. Tag `order` separately controls variable merging and does not order
service execution. See [inventory assignments](../inventory.md#schema-and-file-references).

Services remain sequential within a host. Different hosts may run concurrently;
see [SSH and parallel apply](../ssh.md). There are no cross-service dependency
links, automatic dependency inclusion, or reverse service deletion graph.
Selecting a service does not select its prerequisites.

## Present services

A `present` assignment prepares changes in these phases:

1. **Binaries:** apply changed package requests in list declaration order.
2. **Unmask, when requested:** remove persistent and runtime masks before
   writing an owned full unit or operating on the process.
3. **Configs:** write changed configs in list declaration order, then owned
   units and drop-ins with their prepared contents and attributes.
4. **Daemon reload, when required:** make changed systemd definitions visible.
5. **Processes:** execute triggered oneshot commands and apply systemd
   enablement, lifecycle, and mask tasks in process list declaration order. For
   a present systemd process, its enablement task
   precedes its lifecycle task; a requested persistent mask follows both. Unmask tasks run
   earlier so a full-unit file write cannot follow a mask link into `/dev/null`.

Only changed resources need write or package tasks. Oneshot commands need an
actual linked change; each has one task in its process-list position.
Systemd lifecycle tasks contain prepared branches, selected using runtime state during apply; a reaction can
therefore become a start when the process is stopped. See [change
reactions](reactions.md) for the branch table.

Each collection is an ordered list. The first item executes before the second,
regardless of their names; unnamed items participate in the same order. To run
an API before a worker, declare it first. Components have no dependency graph.
This orders Richt's tasks without creating systemd unit
dependencies or checking application readiness.

```yaml
processes:
  - name: api
    type: systemd
    unit: example-api.service
  - name: worker
    type: systemd
    unit: example-worker.service
```

Changes to owned units/drop-ins schedule daemon reload and a restart reaction
for their process. Changed ordinary configs under `/etc/systemd/system/`,
`/usr/lib/systemd/system/`, or `/run/systemd/system/` also schedule daemon reload.
A changed package request in a service with systemd processes schedules it too, allowing
new package units to be discovered without relying on RPM scriptlets.
Daemon reload precedes process tasks for present services. See
[systemd definitions](processes/systemd.md#owned-units-and-drop-ins) for file
paths and reload capability verification.

## Absent services

Request removal through the tag assignment:

```yaml
tags:
  example:
    services:
      app: {state: absent}
```

Within that service, Richt traverses the process list in reverse declaration
order, stopping and verifying each systemd process before disabling it.
Oneshot processes schedule no command during absence. These process
tasks precede removal of owned units/drop-ins and configs. Configs are removed
in reverse config-list order, then packages in reverse binary-list order,
followed by any required daemon reload. Thus absence reverses each collection separately and visits processes,
configs, then binaries. An already missing unit is accepted; an external
unit that remains installed is still stopped and disabled.
The declared mask target is ignored for absence, preserving existing masks.
An owned full-unit path that is currently masked must be explicitly unmasked
through a present assignment before removal can be prepared. See
[systemd masks](processes/systemd.md#masks).

Removal still follows ascending service order across services. An assignment
removed from inventory is no longer managed; it does not imply `absent`.
Absent services do not open templates or secret sources.

Richt removes explicitly declared files and requests package removal. The
[RPM reference](binaries/rpm.md#removal) explains dependency removal and package
config handling. Application data, state directories, unrelated files, and
`.rpmsave` files are not purged.

## Failure and continuation

A failed task blocks later tasks on its host, including later services. Other
hosts continue. Lifecycle commands succeed only after their result verification;
[systemd timeouts](processes/systemd.md#states-enablement-and-timeouts) include
both command and waiting time. There are no automatic retries or rollback.

With a saved plan, earlier successes remain recorded and pending reactions
remain executable. Explicitly applying the same file again skips successful
tasks and retries unfinished ones, without rewriting completed configs or
rerendering templates. Interrupted tasks may already have taken effect and can
execute again. Without a saved file, progress exists only for that invocation.
See [saved plans and apply](../files.md) for persistence and locking and
[manual continuation](../continuation.md) for the full failure contract.
