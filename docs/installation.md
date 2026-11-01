---
title: "Installation and requirements"
linkTitle: "Installation"
description: "Build Richt, choose a controller and target platform, and configure privileges."
weight: 20
---

## Build from source

Obtain a checkout with Git, then run the build commands from its root:

```sh
git clone https://github.com/subuk/richt.git
cd richt
```

Building requires **Go 1.27.3 or newer** and Make. Go downloads the dependencies
pinned in `go.mod` and verified by `go.sum` when they are not already cached.

```sh
make build
./bin/richt --help
./bin/richt --version
make install
```

`make build` writes `bin/richt`. `make install` writes to `GOBIN`, or to
`$(go env GOPATH)/bin` when `GOBIN` is unset; put that directory on `PATH`. To keep
an installation in the checkout:

```sh
mkdir -p bin
GOBIN="$PWD/bin" make install
```

Development builds print `richt dev`. Set a label with
`make build VERSION=0.1.0`; a label does not change plan compatibility.
To build a static binary for a Linux amd64 target from another controller:

```sh
make linux-amd64
# Output: bin/richt-linux-amd64
```

These are source-build instructions. The repository does not require a Richt
agent, central server, NetBox, or Argo CD.

## Controllers and targets

| Role | Requirements and verified scope |
| --- | --- |
| Inventory inspection | The Richt binary and local inventory/service YAML. It does not contact hosts or open templates or secret sources. |
| Local file execution | Filesystem access to the declared targets; named owners/groups must already exist. Linux acceptance covers ownership, permissions, symlinks, and SELinux. |
| SSH controller | Richt and system OpenSSH `ssh` in `PATH`; local template and secret files for preparation. macOS has been used as a controller. |
| SSH target | Linux shell and standard GNU coreutils/findutils, `getent`, plus the utilities needed by selected resources. No Go toolchain or Richt binary is needed there. |
| RPM resources | DNF 4, or YUM 4 backed by DNF 4, and an RPM database. Repository/network access is needed when DNF executes the transaction. |
| Processes | systemd with `systemctl`; declared units or packages that supply them. |
| SELinux attributes | An SELinux-enabled Linux target whose kernel accepts the requested full label. Requests fail explicitly on other systems. |

The recorded full-stack platform is AlmaLinux 10.2 x86_64, DNF 4.20.0,
YUM 4 compatibility, systemd 257, with file/label acceptance on SELinux Enforcing.
Other distributions, versions, architectures, and package managers are not
established by that result. A Linux cross-build proves compilation only. See
[validation](validation.md) for the exact scenarios and evidence.

Only selected resources need their backend capabilities. File-only use does
not require DNF or systemd. Python 3 is used by repository example verifiers and
acceptance harnesses; it is not required by the Richt CLI.

## Privileges

Run inventory commands as an ordinary user. Diff observes targets, so it needs
read and observation access; it may fail on protected resources without suitable
privileges. Apply needs permission to create parents, write/remove the managed
files, set requested attributes, and run selected package/systemd operations.
System configuration and RPM transactions normally require root.

Choose privileges explicitly in inventory:

```yaml
transport: {type: local}              # current user's privileges
# Or:
transport: {type: local, sudo: true}  # execute through sudo -n
# Or on a remote target:
transport: {type: ssh, host: app-a, user: deploy, sudo: true}
```

Richt never enables sudo implicitly. `sudo: true` uses noninteractive `sudo -n`
for commands and file operations; configure authorization beforehand. SSH also
runs without a terminal and cannot prompt for passwords, key passphrases, or
host-key agreement. Set up keys/agent and host trust using your normal SSH
workflow. `apply -y` skips only Richt's plan confirmation. See the
[SSH guide](ssh.md) for client aliases, overrides, and authentication behavior.

Use a disposable Linux target for package/process examples and acceptance. The
[quickstart](quickstart.md) confines its config-only targets to a newly created
private directory and needs no root privileges.
