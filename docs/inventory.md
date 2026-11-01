# YAML inventory

Validate and view the supplied two-host example without network access:

```sh
go run . inventory validate -i examples/inventory/richt.yaml
go run . inventory show -i examples/inventory/richt.yaml
go run . inventory show -i examples/inventory/richt.yaml --tag hypervisor --service vmango
```

`validate` resolves the selected scope and reports errors. `show` performs the
same checks and writes the resolved inventory as indented JSON. Neither command
contacts hosts, opens templates, reads secret source values, or changes host
state. The example describes a local host and an illustrative SSH host; inventory
accepts both transport descriptions.

The example resolves `gena.vmango.cloud_name` to `Home` and
`lanst.vmango.cloud_name` to `Net`. Both retain `listen = 127.0.0.1:8080` and
`libvirt_uri = qemu:///system`. Each host's services are ordered
`vmango-repository` (10), then `vmango` (20), independent of YAML listing order.

## Schema and file references

Inventory contains `tags`, `services`, `hosts`, and optional `secret_sources`.
Names of hosts, tags, services and components start with a letter or number and
contain only letters, numbers, hyphens and underscores. Component names are
stable keys, independent of resource paths, package names and unit names.
Unknown fields, duplicate YAML keys, non-string mapping keys, and multiple YAML
documents are errors. All definitions and references are validated, including
definitions outside the selected scope.

```yaml
tags:
  shared:
    weight: 10
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
    services:
      - {name: app, order: 10, state: present}
secret_sources:
  default: {type: yaml, file: secrets/default.yaml}
  production: {type: yaml, file: secrets/production.yaml}
```

A service can be defined inline or by `file`, exclusively. Referenced files
contain one bare service definition (`binaries`, `configs`, `processes`), without
another `services` wrapper or recursive file references. Service file paths are
relative to the inventory file. Template paths are relative to the service file
declaring them; inline services use the inventory file as their base. Absolute
local file paths are also accepted. URL sources are unsupported. Resolved JSON
includes absolute source/template paths and normalized absolute target paths.

A service must contain at least one component; binary-only, config-only and
process-only services are accepted. An assignment has only `name`, `order` and
`state`. State defaults to `present`; `absent` declares removal. Each assigned
service and each order must be unique on its host. A missing assignment does not
request removal. Tags supply variables and never assign services. Component
parameters belong to the service definition, with no assignment overrides.

## Components

```yaml
binaries:
  package:
    type: rpm
    manager: dnf              # dnf (default) or yum
    package: app
    version: "0:1.2-3.noarch" # optional exact epoch:version-release.arch
configs:
  main:
    path: /etc/app.conf
    owner: root
    group: root
    mode: "0644"
    selinux: system_u:object_r:etc_t:s0
    sensitive: false
    template:
      file: templates/app.gotmpl
processes:
  web:
    type: systemd
    unit: app.service
    state: running           # running (default) or stopped
    enabled: true            # optional
    timeout: 5s              # positive Go duration; default 5s
    restart_on: [binary.package]
    reload_on: [config.main]
    after: []
```

Configuration targets must be absolute file paths. `mode` is an octal string.
`template` requires exactly one of `file` or `inline`; an empty inline string is
valid. Templates receive the complete merged host variables. Missing variables,
file permissions, actual owners/groups and SELinux application are checked when
preparing or executing a plan, beyond inventory-only validation.

`restart_on` and `reload_on` reference `binary.<name>` or `config.<name>` in the
same service. `after` references process names in the same service. Unknown,
duplicate or cross-service references and cycles are errors. Processes use
systemd `.service` unit names. Reload support depends on host capabilities and
is checked during plan preparation.

An optional process `unit_file` and named `overrides` use the same fields as
configs. Their target paths may be omitted and are then derived as shown:

```yaml
processes:
  web:
    type: systemd
    unit: app.service
    unit_file:
      template: {file: templates/app.service.gotmpl}
    overrides:
      10-runtime.conf:
        template: {inline: "[Service]\nRestart=always\n"}
```

The unit target is `/etc/systemd/system/app.service`; the override target is
`/etc/systemd/system/app.service.d/10-runtime.conf`. Explicit paths must match
these locations. Override names are plain `.conf` filenames. Template paths
still use the declaring service file as their base.

## Variable merging

Weights increase in priority. All tags of a weight are checked as a group
before that group is merged with earlier weights. YAML tag order has no effect.

- Scalars and lists replace the lower-weight value completely.
- Maps merge recursively; a map and a non-map replace each other at higher weight.
- At equal weight, different keys and compatible nested maps merge. Identical
  values are accepted. Different scalar/list values or incompatible types on
  the same path fail, even when a higher-weight tag would replace that path.
- Errors identify the host, variable path, both tag names and common weight.

For a reproducible conflict, add `home` to `lanst.tags` in a copy of the example:
`home` and `net` both set `vmango.cloud_name` at weight 20. Validation fails at
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
  order. Ownership and tag conflicts on unselected hosts are outside the scope;
  service selection cannot hide those conflicts on selected hosts.

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
allowed. A `default` source is optional at this stage. Inventory commands show
declarations and never read the files or their values, even if a file exists;
missing source files therefore do not fail inventory validation. Secret lookup
and template `secret` evaluation belong to plan preparation.
