---
title: "YAML inventory"
description: "Define services and hosts, merge tag variables, resolve paths, and select assignments."
weight: 40
---

Inventory describes hosts and their connection settings, tags, variables, and
assignments of reusable [services](services/_index.md). Tags select each
service's target state; service definitions contain its ordered components and
execution order.

Validate and view the supplied two-host example without network access:

```sh
go run . inventory validate -i examples/inventory/richt.yaml
go run . inventory show -i examples/inventory/richt.yaml
go run . inventory show -i examples/inventory/richt.yaml --tag hypervisor --service vmango
```

`validate` resolves the selected scope and reports errors. `show` performs the
same checks and writes the resolved inventory as indented JSON. Neither command
contacts hosts, opens templates, reads secret source values, or changes host
state. The example describes a local host and an illustrative SSH host; execution
supports both transport descriptions. See [SSH and parallel apply](ssh.md) for
connection settings and noninteractive authentication.

The example resolves `gena.vmango.cloud_name` to `Home` and
`lanst.vmango.cloud_name` to `Net`. Both retain `listen = 127.0.0.1:8080` and
`libvirt_uri = qemu:///system`. Each host's services are ordered
`vmango-repository` (10), then `vmango` (20), independent of YAML listing order.

## Schema and file references

Inventory contains `tags`, `services`, `hosts`, and optional `secret_sources`.
Names of hosts, tags, services and explicitly named components start with a
letter or number and contain only letters, numbers, hyphens and underscores.
Component names are optional reference labels, independent of resource paths,
package names and unit names.
Unknown fields, duplicate YAML keys, non-string mapping keys, and multiple YAML
documents are errors. All definitions and references are validated, including
definitions outside the selected scope.

```yaml
tags:
  shared:
    order: 10
    services:
      app: {}
    vars:
      app:
        listen: "127.0.0.1:8080"
services:
  app:
    file: services/app.yaml
hosts:
  - name: example
    tags: [shared]
    transport: {type: local}
secret_sources:
  default: {type: yaml, file: secrets/default.yaml}
  production: {type: yaml, file: secrets/production.yaml}
```

A service requires an integer `order`, including when it is not assigned to any
host. Explicit zero is valid; omission is an error. Components can be defined
inline or by `file`, exclusively. Inline definitions put `order` beside their
components. A `file` reference reads the complete definition, including `order`,
from that file; `order` beside `file` is an error, even if the values match.
Referenced files contain one service definition (`order`, `binaries`, `configs`,
`processes`), without another `services` wrapper or recursive file references.
For inventories using the earlier reference syntax, move `order` from beside
`file` into the referenced service file.

Service file paths are relative to the inventory file. Template paths are
relative to the service file declaring them; inline services use the inventory
file as their base. Absolute
local file paths are also accepted. URL sources are unsupported. Resolved JSON
includes absolute source/template paths and normalized absolute target paths.

A service must contain at least one component; binary-only, config-only and
process-only services are accepted. Tags assign services through a mapping keyed
by service name. Each value contains only optional `state: present|absent`;
`{}` defaults to `present`. Hosts receive services from all their tags and do
not declare assignments themselves. A host without assignments has an empty
service list. Component parameters belong to the service definition, with no
assignment or host overrides.

Each service must occur at most once on a host, even when two tags assign it
with identical settings. Errors identify the host, service and both source
tags. Different effective services on a host must have different service
orders. Tags or services that are never combined on a host may reuse assignments
or order values. Services execute in ascending service order, including
`absent` assignments; tag order does not set execution order. Removing a tag or
assignment does not request resource removal: use explicit `state: absent`.

## Components

A service definition contains flat ordered lists under `binaries`, `configs`,
and `processes`. List declaration order controls execution within each
collection; component names do not sort it. Each item can omit `name`, but an
explicit name must be a nonempty string and unique within its collection. The
same name may appear in different collections. Only explicitly named binaries
and configs can be referenced by `restart_on`, `reload_on`, or `run_on`; unnamed items
still execute normally.

This complete definition can be stored in the `services/app.yaml` referenced above:

```yaml
order: 10
binaries:
  - name: package
    type: rpm
    package: app
configs:
  - name: main
    path: /etc/app.conf
    mode: "0644"
    template: {file: templates/app.gotmpl}
processes:
  - name: web
    type: systemd
    unit: app.service
    restart_on: [binary.package]
    reload_on: [config.main]
```

The example assumes an existing unit with reload support. Binary-only,
config-only, and process-only definitions are equally valid. Templates receive
the host's complete merged variables. Inventory inspection validates structure
and resolves paths; rendering, target ownership and permissions, and backend
capabilities are checked during preparation or execution.

[Services](services/_index.md) owns component behavior and the present/absent
lifecycle. See [RPM binaries](services/binaries/rpm.md) for package requests,
[downloads](services/binaries/download.md) and [archives](services/binaries/archive.md)
for checksum-pinned HTTPS artifacts,
[typed configs](services/configs/_index.md) for paths, storage, mounts, and
[file templates](services/configs/files.md) for attributes, sensitivity,
SELinux, and optional `validate: {args: [...], timeout: 5s}`, and [systemd processes](services/processes/systemd.md) for units,
drop-ins, enablement, optional `masked: true|false`, and timeouts.
[Oneshot processes](services/processes/oneshot.md) declare a direct `command` argv,
nonempty `run_on` references, and an optional timeout. Validator
executables are not opened or run by inventory inspection; short and exact RPM
version syntax and optional `disable_excludes` scope are checked without
contacting hosts or repositories. Artifact URLs, checksums and per-type fields
are structurally checked without contacting download servers. [Reactions](services/reactions.md) defines
`restart_on`, `reload_on`, and `run_on` references and their validation.
[Lifecycle and ordering](services/lifecycle.md) defines forward declaration
order for present services and reverse traversal of each collection for absence.
Components have no dependency graph.

[Local user and group configs](services/configs/accounts.md) accept `type: user`
with required `user` and primary `group`, or `type: group` with required `group`.
Optional `uid`/`gid`, `system`, home/shell/comment and supplementary
membership have strict per-type fields. Omitted memberships preserve existing
state. `supplementary_groups` manages an exact list by default;
`append_groups: true` adds the listed memberships while preserving others. An explicit flag
requires the list. `supplementary_groups: []` clears local memberships in exact
mode and leaves them unchanged in append mode. Inventory
inspection checks structure only and never resolves target accounts.

## Variable merging

Tags are processed by ascending `order`, which defaults to zero when omitted.
All tags of an order are checked as a group before that group is merged with
earlier orders. Host tag listing order has no effect.

- Scalars and lists replace the lower-order value completely.
- Maps merge recursively; a map and a non-map replace each other at higher order.
- At equal order, different keys and compatible nested maps merge. Identical
  values are accepted. Different scalar/list values or incompatible types on
  the same path fail, even when a higher-order tag would replace that path.
- Errors identify the host, variable path, both tag names and common order.

For a reproducible conflict, add `home` to `lanst.tags` in a copy of the example:
`home` and `net` both set `vmango.cloud_name` at order 20. Validation fails at
`vars.vmango.cloud_name`, including when `--service vmango-repository` is used.

## Resource ownership

Before service filtering, every assignment of each selected host is checked.
Different services cannot own the same explicitly declared RPM package,
normalized absolute file path, or systemd unit, even with identical settings or
different `present`/`absent` states. DNF and YUM share the RPM package namespace.
Managed unit files and drop-ins also claim their unit, including ordinary
configs under standard systemd system directories. Exact duplicate file/package
components within one service and two processes managing the same unit are
also rejected. A process may own its unit file and multiple drop-ins.

Download binaries claim their normalized file path. Archive binaries own their
whole normalized destination tree, excluding any other declared path at or
below it, including another archive. Artifact sibling metadata paths are reserved
and cannot overlap managed paths. These rules apply across present and absent
assignments before service filtering. Existing artifact destinations are never
adopted; target observation checks protected ownership evidence.

Typed configs also claim their normalized target paths, arrays, VG/LV identities,
and mount targets. Device consumer declarations cannot share a member or PV
device. Storage outputs may be inputs of later configs; this declares sequence,
without adding dependencies. Two mount entries may manage different targets in
`/etc/fstab`; whole-file ownership of `/etc/fstab` conflicts with entry management.
Target-side preparation also checks aliases that resolve to the same resource.
Inventory inspection performs only structural checks and never probes devices.
An absent directory cannot contain a declared path owned by another assigned
service; service filtering does not waive that deletion-scope conflict.

User and group configs claim their respective account names and explicit
numeric UID/GID values. Duplicate claims are rejected across all assignments
on each selected host before service filtering, including absent assignments.
User and group namespaces remain independent; a user's primary `group` is a
reference rather than a group ownership claim.
Account management conflicts with whole-file ownership of `/etc/passwd`,
`/etc/shadow`, `/etc/group`, or `/etc/gshadow` on the same selected host.


For a reproducible file conflict, change the repository config target to
`/etc/vmango.conf` in a copy of the example. Validation identifies the host and
both `vmango-repository/config.repository` and `vmango/config.main`, including
with `--service vmango`.

## Selection

`--host`, `--tag`, and `--service` can each repeat. Matching is exact and case
sensitive. Unknown names produce errors; repeated selectors are harmless.

- Within the host group, host names and tags form a union: `--host gena --tag net`
  selects `gena` plus every host tagged `net`.
- Repeated service names form a union of already assigned services.
- The host group and service group intersect. With no selectors in a group,
  that group selects everything.
- Selecting a service never assigns it to new hosts or adds dependencies.
  Selected hosts with no matching assignments remain visible with an empty
  services list.
- Hosts are displayed by name; their assignments are displayed by increasing
  service order. `--tag` selects hosts, and every selected host still receives
  services and variables from all its tags. Duplicate assignment, service-order,
  ownership and variable conflicts on unselected hosts are outside the scope;
  service selection cannot hide those conflicts on selected hosts. Definitions
  and references, including assignments in unused tags, are always validated.

```sh
# All services on one host
go run . inventory show -i examples/inventory/richt.yaml --host gena
# One service on every host
go run . inventory show -i examples/inventory/richt.yaml --service vmango
# A union of hosts, intersected with a union of services
go run . inventory show -i examples/inventory/richt.yaml \
  --host gena --tag net --service vmango-repository --service vmango
```

## Secret declarations

Named sources currently accept only `{type: yaml, file: <local path>}`. Paths
are relative to the inventory file. Multiple independent YAML sources are
allowed. A `default` source is required only when a template calls `secret`
without naming a source. Inventory commands show
declarations and never read the files or their values, even if a file exists;
missing source files therefore do not fail inventory validation. Secret lookup
and template `secret` evaluation belong to plan preparation. Sources are read
locally on the machine running diff, including for SSH targets. An absent
service does not render templates or read secret sources. See
[YAML secrets and sensitive files](secrets.md) for the value schema, function
arguments, caching, diagnostics, and saved-plan behavior.
