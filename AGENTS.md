# Repository guidance

Richt is a Go CLI for preparing, displaying, and applying executable host
configuration plans. Keep authored documentation, comments, and user-facing
output in English.

## Design and scope

- Start from the current task and the repository's
  [design and roadmap](.agents/skills/richt-development/references/design.md).
  It records accepted rules, the shipped baseline, upcoming first-version work,
  and later scope. Use the focused guides below for implemented syntax and
  behavior. These local sources are sufficient for development; no external
  project context is required. A newer accepted decision or explicit task can
  extend the baseline; do not treat deferred features as permanent prohibitions.
- Use **Kong** for CLI parsing. Prefer the standard library and maintained
  dependencies already in `go.mod`; justify additions with a concrete need.
- Keep domain rules separate from YAML, CLI, filesystem, RPM, and systemd
  implementations. Use the existing public packages. Do not add `internal/`,
  empty future layers, or speculative interfaces. Add abstractions when an
  implemented boundary needs them.
- Current execution supports local/SSH transports, explicit noninteractive sudo,
  bounded host parallelism, DNF4/YUM4, files, systemd, and local YAML secret
  sources. Read [SSH rules](docs/ssh.md) for live client settings and authentication
  and [secret rules](docs/secrets.md) for lookup, sensitivity, and safe diagnostics.
  External secret stores remain later scope.

## Behavioral contracts

- Read [inventory rules](docs/inventory.md) for schema, variable merging,
  selection, resource ownership, and path resolution. Inventory inspection must
  not contact hosts, open templates, or fetch secrets.
- Read [files and saved plans](docs/files.md) before changing preparation,
  execution, persistence, confirmation, or sensitive output. Saved JSON plans
  contain executable prepared inputs and progress. Apply uses those inputs
  without rereading inventory/templates or recomputing changes. Explicit
  continuation skips successful tasks and retries unfinished tasks; a failure
  blocks later tasks on that host. Preserve atomic progress writes and locking.
- Read [service behavior](docs/services.md) before changing package/process
  ordering, reactions, removal, timeouts, or reload capability checks. Plans fix
  package requests, while target DNF resolves repository transactions at apply.
- Keep schema, examples, guides, and observable CLI behavior consistent when a
  task intentionally changes a contract. Do not silently introduce automatic
  retries, rollback, autosaved plans, or run history.

## Workflow and validation

- The project preference is a coordinator that delegates implementation to
  **GPT-6.1 Sol with High reasoning**, when available. Assign clear file ownership
  and agree shared interfaces and dependency order before parallel edits. The
  coordinator reviews, integrates, and verifies the result. If a requested
  delegation cannot use that capability, report the limitation and agree an
  alternative; ordinary work that needs no delegation can continue.
- Add targeted tests for changed behavior and failures at the relevant package
  boundary. Run focused tests while iterating, then `make check` for code changes;
  it checks formatting, vet, all Go tests, CLI smoke behavior, and Linux amd64
  cross-compilation. Use `go test -race` for relevant concurrency, progress, or
  locking changes. Report checks that could not run and why.
- Linux acceptance uses the existing harnesses on an authorized disposable host;
  see [Linux validation](.agents/skills/richt-linux-validation/SKILL.md). A local
  cross-build alone does not verify RPM, systemd, root, or SELinux behavior.
- Use the [development workflow](.agents/skills/richt-development/SKILL.md) for
  feature and bugfix work. Commit, push, host access, and other external actions
  follow the current task's authorization; repository guidance does not grant
  permanent access or require redundant confirmation of an authorized action.
