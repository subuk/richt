---
title: "Installation and requirements"
linkTitle: "Installation"
description: "Install a release archive or build Richt, choose controller and target platforms, and configure privileges."
weight: 20
---

## Install a release archive

When a release is available, download its archive and `SHA256SUMS` from
[GitHub releases](https://github.com/subuk/richt/releases). The workflow produces
these controller binaries:

| Controller | Archive target |
| --- | --- |
| Linux x86_64 | `linux-amd64` |
| Linux arm64 | `linux-arm64` |
| macOS Intel | `darwin-amd64` |
| macOS Apple Silicon | `darwin-arm64` |

The commands below use `X.Y.Z` as a placeholder; replace it with the published
version, without the tag's leading `v`, and choose your archive target. They do
not imply that a particular version has already been released.

```sh
VERSION=X.Y.Z
TARGET=linux-amd64
ARCHIVE="richt-$VERSION-$TARGET.tar.gz"
curl -fLO "https://github.com/subuk/richt/releases/download/v$VERSION/$ARCHIVE"
curl -fLO "https://github.com/subuk/richt/releases/download/v$VERSION/SHA256SUMS"
awk -v archive="$ARCHIVE" '$2 == archive { print }' SHA256SUMS > "$ARCHIVE.sha256"
test -s "$ARCHIVE.sha256"
```

Verify the selected archive before extracting. On Linux:

```sh
sha256sum -c "$ARCHIVE.sha256"
```

On macOS:

```sh
shasum -a 256 -c "$ARCHIVE.sha256"
```

Stop if verification fails. Each archive contains a directory matching its
filename without `.tar.gz`, with the `richt` executable and `README.md`:

```sh
tar -xzf "$ARCHIVE"
mkdir -p "$HOME/.local/bin"
install -m 0755 "richt-$VERSION-$TARGET/richt" "$HOME/.local/bin/richt"
export PATH="$HOME/.local/bin:$PATH"
richt --version
richt --help
```

Add that `PATH` setting to your shell configuration to keep it for future
sessions. Release binaries need no Go toolchain. SSH execution still requires
the system OpenSSH `ssh` client in `PATH` and configured authentication and host
trust; see the [SSH guide](ssh.md). Controller binaries do not expand supported
target resources: package and systemd operations still require a suitable Linux
target. Windows controller support is deferred to
[KRG-92](https://linear.app/krglv/issue/KRG-92/support-windows-as-an-ssh-only-controller).
This release workflow does not publish DEB, RPM, or Docker artifacts.

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
| Inventory inspection | The Richt binary and local or HTTPS inventory/service YAML. HTTPS needs controller network access and trusted CA roots; optional explicit client settings supply extra trust/authentication. It does not contact hosts or open templates or secret values. |
| Local file execution | Python 3 standard library and Unix filesystem support for template observation/writes/backups; Linux for SELinux metadata. Filesystem access to the declared targets; named owners/groups must exist or be created by preceding account configs. Linux acceptance covers ownership, permissions, symlinks, and SELinux. |
| SSH controller | Richt and system OpenSSH `ssh` in `PATH`; local or pinned HTTPS templates and local secret bindings for preparation. HTTPS source acquisition runs on the controller. macOS has been used as a controller. |
| SSH target | Linux shell and standard GNU coreutils/findutils, `getent`, Python 3 standard library for template files, plus the utilities needed by selected resources. No Go toolchain or Richt binary is needed there. |
| RPM resources | DNF 4, or YUM 4 backed by DNF 4, and an RPM database. Repository/network access is needed when DNF executes the transaction. |
| DEB resources | Linux, APT 2.2+ with apt-cache/apt-config, dpkg/dpkg-query and Python 3.8+ standard library; standard APT/dpkg paths and protected root-owned configuration. Repository/network access is needed for conditional metadata refresh and installation at apply. |
| Download/archive binaries | Linux, Python 3.8+ standard library, GNU `readlink`, util-linux `findmnt`, target HTTPS connectivity and trusted CA certificates; filesystem support for atomic `renameat2` publication. No daemon or installed Richt binary on SSH targets. |
| Systemd processes | systemd with `systemctl`; declared units or packages that supply them. |
| Oneshot processes | The declared executable and its dependencies on the target; no systemd requirement. |
| Local user/group configs | Linux, GNU/glibc-compatible `getent -s files`, shadow `useradd`, `usermod`, `userdel`, `groupadd`, `groupdel`, and privileges to update local accounts. User deletion additionally needs util-linux `unshare`/`mount` and mount namespace privilege to preserve same-name groups. |
| SELinux attributes | An SELinux-enabled Linux target whose kernel accepts the requested full label. Requests fail explicitly on other systems. |

The recorded full-stack platform is AlmaLinux 10.2 x86_64, DNF 4.20.0,
YUM 4 compatibility, systemd 257, with file/label acceptance on SELinux Enforcing.
DEB/APT acceptance additionally covers Ubuntu 24.04.5 x86_64 userspace in a
systemd container, locally and through SSH/sudo, with APT 2.8.4 and dpkg 1.22.6.
Other distributions, versions, architectures, and package managers are not
established by those results. CI cross-compiles all four controller binaries on
one Linux runner with `CGO_ENABLED=0` and smoke-tests only the unpacked native
Linux amd64 archive. This does not establish macOS runtime behavior or full
Linux host acceptance. See [CI and releases](ci.md) for build coverage and
[validation](validation.md) for the exact acceptance scenarios and evidence.

Only selected resources need their backend capabilities. File-only use does
not require DNF or systemd. Python 3.8+ is a target runtime requirement for
download/archive and DEB binaries. Other resources do not acquire that requirement;
Python 3 is also used by repository verifiers and acceptance harnesses.

## Privileges

Run inventory commands as an ordinary user. Diff observes targets, so it needs
read and observation access; it may fail on protected resources without suitable
privileges. Apply needs permission to create parents, write/remove the managed
files, set requested attributes, and run selected package and process operations.
System configuration and package transactions normally require root.

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
