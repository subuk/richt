# Richt

Richt is a Go CLI that prepares, displays, and applies an executable host
configuration plan. Services combine RPM packages, templated files, and systemd
processes. YAML inventory assigns services to hosts and resolves variables from
weighted tags.

Local and SSH execution support DNF4/YUM4, systemd, file ownership and
permissions, and SELinux contexts. SSH uses the system client and current
`ssh_config`; explicit sudo uses noninteractive authentication. Integration checks run on AlmaLinux 10.2 amd64 with SELinux
Enforcing. Inventory inspection and SSH control also run on macOS. Secret backends remain
deferred: templates calling `secret` fail during preparation.

## Build and install

Use Go 1.27.3 or newer and Make. Dependencies are pinned in `go.mod` and verified
by `go.sum`.

```sh
make build
./bin/richt --help
./bin/richt --version
make install
```

`make install` writes `richt` into `GOBIN`, or `$(go env GOPATH)/bin` when `GOBIN`
is unset. Add that directory to `PATH`. For a workspace-local installation, use
`GOBIN="$PWD/bin" make install` after creating the `bin` directory.

Development builds report `richt dev`. Set a build label with
`make build VERSION=0.1.0`. `make linux-amd64` builds a static Linux amd64 binary
at `bin/richt-linux-amd64`.

## Use

Inspect the supplied inventory without contacting hosts or reading templates:

```sh
./bin/richt inventory validate -i examples/inventory/richt.yaml
./bin/richt inventory show -i examples/inventory/richt.yaml
./bin/richt inventory show -i examples/inventory/richt.yaml --tag hypervisor --service vmango
```

On a disposable Linux machine, try the local file example. Its managed target is
`/var/tmp/richt-example/greeting.conf`:

```sh
./bin/richt diff -i examples/files/inventory.yaml
./bin/richt diff -i examples/files/inventory.yaml -o /var/tmp/richt-example-plan.json
./bin/richt apply /var/tmp/richt-example-plan.json
./bin/richt apply -y -i examples/files/inventory.yaml
```

`diff` displays changes without modifying managed resources. A saved JSON plan
contains prepared contents, patches, operations, and task progress. Applying it
does not reread inventory or templates, recompute changes, or check whether file
contents changed since diff. Review the displayed operations before confirming.
Without `-y`, apply requires a terminal and explicit agreement.

Apply handles up to 20 hosts concurrently; `--parallelism N` overrides the limit
for the current run, including saved plans. A value of 1 runs sequentially.
A new explicit apply of a saved plan retries unfinished tasks and skips
successful ones. A failure stops that host; other hosts continue. Progress is
saved atomically under a same-plan lock. A progress save failure stops further
actions; in-flight operations are awaited. Applying without a plan file keeps progress only in memory. Host, tag,
and service filters are available when building plans and rejected for saved
plans. `sensitive: true` hides the entire file diff and file error details, while
the executable JSON retains the real content.

See the focused guides and examples:

- [Inventory schema, variables, validation, and selection](docs/inventory.md)
  — [two-host inventory example](examples/inventory/richt.yaml).
- [File templates, SELinux, confirmation, and saved plans](docs/files.md)
  — [local file example](examples/files/inventory.yaml).
- [SSH, sudo, and parallel apply](docs/ssh.md)
  — [two-host SSH example](examples/ssh/inventory.yaml).
- [RPM packages, systemd reactions, and service removal](docs/services.md)
  — [local package and service example](examples/rpm-systemd/inventory.yaml).

## Verify

```sh
make check
```

This checks formatting without modifying files, runs `go vet` and all Go tests,
exercises the CLI, and cross-builds Linux amd64. GitHub Actions runs the same
checks for pushes and pull requests. The checks do not contact integration
hosts, publish artifacts, or deploy changes.

The file acceptance suite requires root on a disposable Linux machine with
SELinux Enforcing:

```sh
python3 tests/integration/files.py --binary ./bin/richt --work-root /root --target-root /var/lib
```

It allocates unique directories below the supplied roots, verifies file and
terminal behavior, and removes its own directories afterwards. Add `--keep` to
retain them. Package and systemd integration instructions are in the
[service guide](docs/services.md); use a disposable VM with a snapshot for those
stateful scenarios.

## Code organization

The CLI uses Kong for arguments and owns confirmation and output. Public Go
packages separate inventory rules, YAML loading, executable plans, file
operations, RPM operations, systemd operations, and service orchestration.
Packages contain implemented behavior; the repository has no `internal`
directories or empty future implementation layers.

Contributor and agent guidance is in [AGENTS.md](AGENTS.md). Repository-local
skills cover [feature and bugfix development](.agents/skills/richt-development/SKILL.md)
and [Linux acceptance validation](.agents/skills/richt-linux-validation/SKILL.md).
The [design and roadmap](.agents/skills/richt-development/references/design.md)
records accepted architecture, upcoming first-version requirements, and later
scope without requiring external project context.
