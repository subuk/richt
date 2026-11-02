# Richt

Richt is a standalone Go CLI for Linux host configuration. An
[inventory](docs/inventory.md) describes hosts, tags, variables, and service
assignments. A [service](docs/services/_index.md) is a reusable unit made of
named binaries, configs, and processes in any nonempty combination. Tags assign
service target states to hosts; service definitions set execution order.

Inspect the inventory, prepare changes with `diff`, review the executable plan,
and explicitly `apply` it. Current backends provide RPM packages, templated
files, and systemd processes over local or SSH transport, with explicit
noninteractive sudo, bounded host concurrency, and local YAML secret sources.

## Start here

If you are reading this README in an unpacked release archive, run
`./richt --help` or `./richt --version`. Follow the
[online installation guide](https://richt.dev/docs/installation/)
and [quickstart](https://richt.dev/docs/quickstart/) for setup and
usage; SSH execution also needs the system OpenSSH client.

Use a [release archive](docs/installation.md#install-a-release-archive) for Linux
or macOS on amd64 or arm64 when a release is available. To build from a checkout,
install Go 1.27.3 or newer and Make:

```sh
make build
./bin/richt --help
./bin/richt --version
```

Follow the [config-only quickstart](docs/quickstart.md) to create, review, apply,
update, and remove a file in a private temporary directory without root. See
[installation](docs/installation.md) for downloads, `make install`, cross-builds, controller/
target requirements, and privileges. Full-stack Linux acceptance is recorded on
AlmaLinux 10.2 x86_64 with DNF 4/YUM 4, systemd, and SELinux Enforcing; see
[validation](docs/validation.md) for scope and evidence.

Inspect the illustrative two-host inventory without contacting hosts or reading
templates or secret sources:

```sh
./bin/richt inventory validate -i examples/inventory/richt.yaml
./bin/richt inventory show -i examples/inventory/richt.yaml --tag hypervisor --service vmango
```

For persistent progress, prepare a plan with `diff -o PLAN.json`, then run
`apply PLAN.json`. Apply displays the plan and requires terminal agreement
unless `-y` is supplied. Saved plans contain real executable inputs, potentially
including secrets. Apply does not reread sources, recalculate changes, or check
for drift. Explicit continuation skips successful tasks and retries unfinished
ones; a failure blocks that host while other hosts continue. There is no
autosave, automatic retry, rollback, or run history.

## Documentation and examples

The [documentation index](docs/_index.md) links the complete first-version guide:

- [Inventory](docs/inventory.md): hosts, tags, assignments, variables, and selection.
- [Services](docs/services/_index.md): [binaries](docs/services/binaries/_index.md),
  [configs](docs/services/configs/_index.md), and [processes](docs/services/processes/_index.md).
- [Saved plans and apply](docs/files.md), [manual continuation](docs/continuation.md).
- [CLI commands and flags](docs/cli.md), [SSH/sudo and concurrency](docs/ssh.md).
- [Secrets and sensitive output](docs/secrets.md), [limitations](docs/limitations.md).
- [Examples](docs/examples.md): [ready vmango RPMs](examples/vmango/README.md),
  [two YAML secret sources](examples/secrets/README.md), and
  [deliberate failure/continuation](examples/continuation/README.md).

To preview the Hugo/Docsy documentation site locally, install Node.js 22 or
newer with npm, then run:

```sh
npm ci --prefix website
make docs-serve
# Build or check without serving:
make docs-build
make docs-check
```

Site dependencies and setup are documented in [website/README.md](website/README.md).
The [documentation site](https://richt.dev/) publishes automatically through
GitHub Actions after a validated push to `main`. Pull requests validate the
production build without deploying it. Local build and preview commands do not
publish a site.

## Development

```sh
make check
go test -race ./...
make release-check
```

`make check` checks formatting, runs vet and all Go tests, exercises CLI smoke
behavior, and cross-builds Linux amd64. The additional commands check races and
release tooling. [CI and releases](docs/ci.md) describes the shared build for
pull requests, pushes to `main`, and manual releases, including four-platform
archives and smoke testing the unpacked Linux amd64 archive. These checks do not
prove runtime RPM, systemd, root, or SELinux behavior. [Linux validation](docs/validation.md)
describes the separate disposable-host acceptance workflows.

Public Go packages separate inventory/domain validation, YAML loading, executable
plans, files, RPM, systemd, transport, and orchestration. The CLI uses Kong.
Contributor guidance is in [AGENTS.md](AGENTS.md); the local
[development workflow](.agents/skills/richt-development/SKILL.md) and
[design and roadmap](.agents/skills/richt-development/references/design.md)
record implementation rules and later scope.
