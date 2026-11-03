---
title: "Examples and walkthroughs"
linkTitle: "Examples"
description: "Choose a config-only, ready-package, SSH, secrets, or continuation scenario."
weight: 100
---

Run source examples from the repository root after `make build`. The commands
and requirements in each example's README are part of the walkthrough. Use
fictional values only; replace illustrative addresses and credentials before
execution. Recorded results and their limits live in [validation](validation.md).

| Scenario | Source | Scope and prerequisites |
| --- | --- | --- |
| First local file | [Quickstart](quickstart.md) | A fresh temporary directory; no root, RPM, or systemd. Create/update/no changes/explicit removal. |
| Split inventory and service files | [File inventory](../examples/files/inventory.yaml) and [service](../examples/files/service.yaml) | Local `/var/tmp/richt-example/greeting.conf`; template path relative to the service file. |
| Plain files and directories | [Typed config paths](../examples/typed-configs/README.md) | Local existence and attribute management, preserved unmanaged contents, and explicit recursive removal. No block devices. |
| Weighted variables and selection | [Two-host inventory](../examples/inventory/richt.yaml) | Safe to inspect with `inventory validate/show`; one local and one illustrative SSH host. Execution changes host configuration. |
| Full package/config/process lifecycle | [Ready vmango package](../examples/vmango/README.md) | Disposable DNF 4/systemd Linux target, ready repository RPMs, required privileges. No custom RPM build. |
| Repository exclusion override | [Kubernetes packages](../examples/kubernetes/README.md) | DNF 4 target with root privileges; repository setup followed by pinned kubelet/kubeadm requests. Package setup only. |
| Failure and manual continuation | [Continuation example](../examples/continuation/README.md) | Config-only deliberate partial failure, repair, and saved-plan resume. |
| Two independent secret sources | [Secrets example](../examples/secrets/README.md) | Python 3 verifier and local Richt; dummy values, private temporary targets, frozen saved bytes, and automatic cleanup. |
| Change-triggered command | [Oneshot sysctl](../examples/oneshot/README.md) | Disposable Linux target with sysctl and root privileges; writes one sysctl config and applies it without a systemd unit. |
| Two SSH hosts | [SSH walkthrough](../examples/ssh/README.md) and [inventory](../examples/ssh/inventory.yaml) | Ready vmango RPMs on two disposable Linux targets; replace illustrative client aliases and configure noninteractive sudo. |

## Choose the appropriate workflow

Inventory commands are suitable for inspecting the supplied two-host example
before any SSH or host setup:

```sh
./bin/richt inventory validate -i examples/inventory/richt.yaml
./bin/richt inventory show -i examples/inventory/richt.yaml --tag hypervisor --service vmango
```

They validate definitions and resolve merged variables without opening
referenced templates or secret source files. Inspecting successfully does not
prove target privileges, repository availability, application configuration, or
reload capability.

Use [Inventory](inventory.md) for assignments and selection, and
[Services](services/_index.md) for component composition and lifecycle.
For a persistent reviewed run, use `diff -o` followed by `apply` on the JSON
path. For a one-off run with no saved progress, `apply -i` prepares and applies
in memory. See [saved plans and apply](files.md), [CLI](cli.md), and
[continuation](continuation.md).

The [RPM/systemd fixture harness](../examples/rpm-systemd/verify.py) and
[SSH harness](../tests/integration/ssh.py) remain developer regression tools.
They create custom fixture RPMs and require the isolated setup documented in
[fixture regression](validation.md#rpm-and-systemd-fixture-regression) and
[SSH acceptance](ssh.md#developer-fixture-acceptance);
they are separate from the ready-vmango walkthrough and current package
acceptance. Do not install build tools or create a fixture repository to follow
the vmango example.
