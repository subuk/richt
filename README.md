# Richt

Richt is a standalone Go CLI that prepares, displays, and explicitly applies
executable Linux host configuration plans. YAML inventory assigns reusable
services containing RPM packages, templated files, and systemd processes.
Weighted tags supply host variables. Local and SSH execution support explicit
noninteractive sudo, bounded host concurrency, and local YAML secret sources.

## Start here

Build from a checkout with Go 1.27.3 or newer and Make:

```sh
make build
./bin/richt --help
./bin/richt --version
```

Follow the [config-only quickstart](docs/quickstart.md) to create, review, apply,
update, and remove a file in a private temporary directory without root. See
[installation](docs/installation.md) for `make install`, cross-builds, controller/
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

- [CLI commands and flags](docs/cli.md), [inventory schema and selection](docs/inventory.md).
- [Files and saved plans](docs/files.md), [manual continuation](docs/continuation.md).
- [RPM/systemd lifecycle and removal](docs/services.md), [SSH/sudo and concurrency](docs/ssh.md).
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
These commands build or serve local files; they do not publish a site.

## Development

```sh
make check
```

This checks formatting, runs vet and all Go tests, exercises CLI smoke behavior,
and cross-builds Linux amd64. It does not access hosts or prove runtime RPM,
systemd, root, or SELinux behavior. [Linux validation](docs/validation.md) describes
the separate disposable-host acceptance workflows.

Public Go packages separate inventory/domain validation, YAML loading, executable
plans, files, RPM, systemd, transport, and orchestration. The CLI uses Kong.
Contributor guidance is in [AGENTS.md](AGENTS.md); the local
[development workflow](.agents/skills/richt-development/SKILL.md) and
[design and roadmap](.agents/skills/richt-development/references/design.md)
record implementation rules and later scope.
