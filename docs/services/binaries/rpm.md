---
title: "RPM packages"
description: "Manage upstream, exact, or unversioned RPM requests through DNF 4 and YUM 4."
weight: 10
---

```yaml
binaries:
  - name: package
    type: rpm
    manager: dnf
    package: example-app
    version: "1.34.3"
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `name` | no | Nonempty reference label, unique within the binary list |
| `type` | yes | `rpm` |
| `manager` | no | `dnf` (default), or `yum` backed by DNF 4 |
| `package` | yes | RPM NAME, without an architecture selector |
| `version` | no | Literal upstream VERSION such as `1.34.3`, or exact `epoch:version-release.arch`; omission keeps any installed version |
| `disable_excludes` | no | Disable DNF exclusion/inclusion filters for one repository ID, `main`, or `all`, for this transaction only |

The supported backend is DNF 4, including its YUM 4 compatibility command.
YUM 3, DNF 5, and other package managers are unsupported. See
[installation and platform requirements](../../installation.md) and the
[validation report](../../validation.md) for verified environments.

## Requests and preparation

With no `version`, Richt installs only when the named package is absent. It
leaves an installed version alone even when repositories contain a newer one.
A short version such as `1.34.3` constrains the upstream RPM VERSION only.
An installed package with that literal VERSION needs no task, regardless of
its epoch, release, or architecture. It does not match a prefix such as
`1.34.30`. A missing package or different VERSION prepares an install request
that can upgrade or downgrade.

An exact version includes epoch, version, release, and architecture, for example
`0:1.34.3-1.noarch`; write `0` for a package without an epoch. All components
must match for an exact request to need no task. A different installed version
requests the exact NEVRA and permits upgrade or downgrade. Partial forms such
as `0:1.34.3` or `1.34.3-1.noarch`, whitespace, and user-supplied globs are
rejected. Quote version values in YAML.

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
| Upstream version | `dnf -y install -- NAME-VERSION` |
| Removal | `dnf -y remove -- NAME` |

For a short upstream version, DNF resolves omitted epoch, release, and
architecture from the target's repository configuration when it executes the
versioned install request. Richt passes the literal `NAME-VERSION` without
adding a version-prefix glob. DNF's versioned install permits both upgrades
and downgrades; its package-spec matching and transaction policy apply. See the
[DNF 4 command reference](https://dnf.readthedocs.io/en/stable/command_ref.html#install-command).

Names and versions are validated as individual arguments. Local execution
passes arguments directly; SSH safely quotes them for the target shell.

## Transaction exclusion overrides

Set `disable_excludes: kubernetes` to install a package from a repository that
normally excludes it, without changing the repository file:

```yaml
binaries:
  - name: kubelet
    type: rpm
    package: kubelet
    version: "1.34.3"
    disable_excludes: kubernetes
```

The setting accepts one nonempty string: a repository ID made of ASCII letters,
digits, `-`, `_`, `.`, and `:`, or the reserved scopes `main` and `all`. Lists,
globs, whitespace, null, and explicitly empty values are rejected. Omit the
field to preserve DNF's normal filtering. Repository existence is left to DNF
at apply; inventory inspection and diff do not query repositories.

DNF's [exclusion override](https://dnf.readthedocs.io/en/stable/command_ref.html#options)
disables both `excludepkgs` (also called `exclude`) and `includepkgs` in the
selected scope. A repository ID selects that repository; `main` selects the
`[main]` configuration; `all` selects both main and repository configurations.
The override applies to the entire requested transaction, including dependency
resolution. It does not edit configuration or change later transactions.

Richt adds one argument before the transaction command, for example
`dnf -y --disableexcludes=kubernetes install -- kubelet-1.34.3`.
The same option works with `install-n`, `install-nevra`, and `remove`, and with
the YUM 4 compatibility command. Local and SSH execution use the same validated
argument and existing explicit sudo settings.

The override is not installed package state. A matching installed package stays
a no-op when only `disable_excludes` changes; this does not reinstall it or
trigger linked processes. See the [Kubernetes repository example](../../../examples/kubernetes/README.md)
for repository ordering and pinned package requests.

## Saved requests

The plan fixes the requested package, supplied version string, and
`disable_excludes` value. The reviewed action shows the actual override; saved
apply uses it even after the inventory changes or disappears. For a short
version, the displayed action explicitly defers epoch, release, and architecture
selection until apply; the saved request remains short after execution. An
exact version fixes the NEVRA. The plan does not freeze repository metadata,
dependency resolution, checksums, scriptlet
side effects, or every package in the transaction. The target's DNF resolves
these when applying the request and downloads RPMs then. Repository trust and
signature checks follow the host's repository configuration. RPM artifacts are
not embedded in JSON. See [saved plans and apply](../../files.md) for the shared
frozen-input contract.

## Removal

An `absent` service requests removal by package name, after its processes and
managed files. DNF may remove dependent packages. RPM controls package-owned
config handling, including possible `.rpmsave` files. RPM removal does not purge
application state, unrelated files, or `.rpmsave` files. See [absent
services](../lifecycle.md#absent-services) for ordering and assignment semantics.

When supplied, `disable_excludes` is retained for removal and passed to
`remove` with the same scope as installation. Removal still targets the package
name, regardless of its requested version; an already absent package is a no-op.

The [ready vmango walkthrough](../../../examples/vmango/README.md) demonstrates
repository configuration, exact upgrade/downgrade, unversioned preservation,
and removal with published RPMs. The [upstream-version example](../../../examples/rpm-versions/README.md)
compares short, exact, and omitted requests.
