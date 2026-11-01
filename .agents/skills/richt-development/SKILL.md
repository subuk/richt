---
name: richt-development
description: Implement or fix Richt inventory, plan, CLI, and host-resource behavior with the relevant contracts, tests, and documentation.
---

# Richt development

Read [AGENTS.md](../../../AGENTS.md) first for project design, language, and
coordination preferences. Use this workflow for Richt feature and bugfix work;
Linux acceptance details belong to [richt-linux-validation](../richt-linux-validation/SKILL.md).

## Establish the contract

Use the current request and relevant accepted ADR or issue when supplied or
needed to resolve design. Fully specified local tasks do not require a Linear
lookup. Distinguish implemented behavior from a proposal; ADR-0007 and ADR-0008
override older overlapping proposals, while newer accepted decisions can extend
them.

Read the guide that owns the changed contract:

- [Inventory](../../../docs/inventory.md): strict YAML, assignments, variables,
  selectors, references, paths, and resource ownership.
- [Files and saved plans](../../../docs/files.md): preparation, executable JSON,
  progress, continuation, locking, confirmation, and sensitive output.
- [Services](../../../docs/services.md): RPM requests, process reactions,
  dependency order, enablement, removal, and shared command/wait timeouts.

Trace the behavior through existing packages before choosing an extension:
`inventory` owns domain validation/resolution, `inventoryyaml` loads YAML,
`plan` owns executable operations and persisted progress, `config`, `rpm`, and
`systemd` implement resources, `engine` composes services, and `cli` owns Kong
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
