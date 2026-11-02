---
name: richt-development
description: Implement or fix Richt inventory, plan, CLI, and host-resource behavior with the relevant contracts, tests, and documentation.
---

# Richt development

Read [AGENTS.md](../../../AGENTS.md) first for project design, language, and
coordination preferences. Use this workflow for Richt feature and bugfix work;
Linux acceptance details belong to [richt-linux-validation](../richt-linux-validation/SKILL.md).

## Establish the contract

Use the current request and the local [design and roadmap](references/design.md).
Read its relevant sections for accepted domain rules, SSH/sudo and host
concurrency contracts, secret-source semantics, or later extension scope. Distinguish
shipped behavior from accepted upcoming requirements and open implementation
choices. No external project lookup is required. An explicit task or a newer
accepted decision can extend these contracts; record intentional changes locally.

Read the guide that owns the changed contract:

- [Inventory](../../../docs/inventory.md): strict YAML, assignments, variables,
  selectors, references, paths, and resource ownership.
- [Saved plans and apply](../../../docs/files.md): preparation, executable JSON,
  progress, continuation, locking, confirmation, and sensitive output.
- [SSH](../../../docs/ssh.md): system-client configuration, noninteractive
  authentication, sudo, target observations, and parallel host execution.
- [Services](../../../docs/services/_index.md): composition,
  [lifecycle](../../../docs/services/lifecycle.md),
  [reactions](../../../docs/services/reactions.md), and component references for
  [RPM](../../../docs/services/binaries/rpm.md),
  [files](../../../docs/services/configs/files.md),
  [systemd](../../../docs/services/processes/systemd.md), and
  [oneshot](../../../docs/services/processes/oneshot.md).
- [Secrets](../../../docs/secrets.md): YAML source lookup, template sensitivity,
  safe diagnostics, and protection of prepared secret-bearing files.

Trace the behavior through existing packages before choosing an extension:
`inventory` owns domain validation/resolution, `inventoryyaml` loads YAML,
`plan` owns executable operations and persisted progress, `config`, `rpm`,
`systemd`, and `oneshot` implement resources, `engine` composes services, and `cli` owns Kong
arguments, confirmation, and output. Keep changes at the boundary that owns the
rule. Add an interface only when an implemented dependency needs one.

## Implement and prove the change

For behavior changes, identify the concrete input, intended result, and failure
case. If work is delegated, agree shared types and semantics before dependent
edits and give each worker clear file ownership. Preserve unrelated changes.

Choose tests that prove the relevant invariant. Examples include validation
before side effects, strict schema rejection, preparation versus apply behavior,
explicit continuation after failure, or deterministic ordering. Extend existing
package/CLI tests rather than creating a parallel test framework. Use backend
fakes for domain orchestration and the existing Linux harnesses for actual
filesystem, RPM, systemd, terminal, and SELinux behavior.

Run focused Go tests during iteration, then `make check` for code changes. Add
race checking for changed concurrency, locking, or progress paths. Follow the
Linux validation skill when platform behavior needs verification; a successful
cross-build is only a compilation result.

Update affected examples and guides when schema, commands, supported capability,
or observable behavior changes. Report the result, verification, and remaining
limits. Keep external actions within the current request's authorization.
