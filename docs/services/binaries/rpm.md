---
title: "RPM packages"
description: "Manage exact or unversioned RPM requests through DNF 4 and YUM 4."
weight: 10
---

```yaml
binaries:
  package:
    type: rpm
    manager: dnf
    package: example-app
    version: "0:1.2-3.noarch"
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `type` | yes | `rpm` |
| `manager` | no | `dnf` (default), or `yum` backed by DNF 4 |
| `package` | yes | RPM NAME, without an architecture selector |
| `version` | no | Exact `epoch:version-release.arch`; omission keeps any installed version |

The supported backend is DNF 4, including its YUM 4 compatibility command.
YUM 3, DNF 5, and other package managers are unsupported. See
[installation and platform requirements](../../installation.md) and the
[validation report](../../validation.md) for verified environments.

## Requests and preparation

With no `version`, Richt installs only when the named package is absent. It
leaves an installed version alone even when repositories contain a newer one.
An explicit version requires epoch, version, release, and architecture; write
`0` for a package without an epoch. An exact installed match needs no task;
a different version requests the exact NEVRA and permits upgrade or downgrade.

Preparation checks `manager --version` and queries the installed RPM database.
It does not consult repository metadata or download RPMs. A preceding
config-only service can therefore create a repository file that does not exist
at diff time. See [lifecycle and ordering](../lifecycle.md) for service order.

## Apply and frozen inputs

The prepared request resolves to these commands (`yum` may replace `dnf`):

| Request | Command |
| --- | --- |
| Unversioned install | `dnf -y install-n -- NAME` |
| Exact install | `dnf -y install-nevra -- NAME-EPOCH:VERSION-RELEASE.ARCH` |
| Removal | `dnf -y remove -- NAME` |

Names and versions are validated as individual arguments. Local execution
passes arguments directly; SSH safely quotes them for the target shell.

The plan fixes the requested package and exact NEVRA when specified. It does
not freeze repository metadata, dependency resolution, checksums, scriptlet
side effects, or every package in the transaction. The target's DNF resolves
these when applying the request and downloads RPMs then. Repository trust and
signature checks follow the host's repository configuration. RPM artifacts are
not embedded in JSON. See [saved plans and apply](../../files.md) for the shared
frozen-input contract.

## Removal

An `absent` service requests removal by package name, after its processes and
managed files. DNF may remove dependent packages. RPM controls package-owned
config handling, including possible `.rpmsave` files. Richt does not purge
application state, unrelated files, or `.rpmsave` files. See [absent
services](../lifecycle.md#absent-services) for ordering and assignment semantics.

The [ready vmango walkthrough](../../../examples/vmango/README.md) demonstrates
repository configuration, exact upgrade/downgrade, unversioned preservation,
and removal with published RPMs.
