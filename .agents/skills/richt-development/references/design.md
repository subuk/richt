# Richt design and roadmap

This is the repository's consolidated accepted design context. Use the current
request with the relevant sections here and the focused guides linked below.
The guides define implemented syntax and behavior; this reference also records
accepted work that is not implemented yet. Update both when a task intentionally
changes a contract. Do not infer that an upcoming capability already works, or
that a later extension is permanently forbidden.

Contents: [architecture](#architecture-and-domain),
[shipped baseline](#shipped-baseline), [plans](#executable-plans-and-progress),
[SSH and concurrency](#ssh-sudo-and-host-concurrency),
[secrets](#upcoming-secret-source-execution),
[release documentation](#upcoming-first-version-documentation), and
[later scope](#later-scope).

## Architecture and domain

Richt is a declarative Linux host configuration tool implemented as a standalone
Go CLI. It prepares desired configuration, observes resources to calculate a
diff, displays an executable plan, and explicitly applies that plan. NetBox,
Argo CD, and Ansible are not required dependencies of the local core.

A host has a stable name, tags, ordered service assignments, and connection
settings. A service is a reusable logical unit containing zero or more named
binary, config, and process components; at least one set must be nonempty.
Config-only services can establish repositories before later package services;
binary-only and process-only services are equally valid. A service and an
individual managed process are different concepts.

Assignments specify only the service, unique numeric order, and target state
(`present` by default, or explicit `absent`). Component parameters, units,
overrides, and timeouts belong to service definitions, with no per-assignment
overrides. Content parameterization happens in templates using the host's full
merged variables. Tags supply variables and never assign services.

Keep domain rules independent of YAML, CLI/TTY, filesystem, RPM, systemd, and
transport implementations. Use meaningful domain or technology names rather
than architectural labels such as DTO or Repository. Existing public packages
are the starting point; no `internal/`, empty package trees, duplicate models,
or interfaces for hypothetical extensions. Add boundaries when implemented
dependencies need them. Kong is the CLI parser; prefer the standard library
and maintained dependencies already in `go.mod`.

Inventory source and host transport are independent. Transports provide the
needed file get/put, package, process, and observation capabilities; arbitrary
shell execution is not a universal domain requirement. Local and SSH backends
may use OS commands, while a future API transport may expose only resource
operations. Only required unsupported capabilities should reject preparation;
an unused capability should not block a host. An unsupported reload never
silently becomes restart.

Template acquisition, rendering, comparison, and writing are separate concerns.
The local renderer is Go `text/template`; a future source may supply already
rendered content. Extensibility does not require dynamic Go plugins or separate
extension processes.

## Shipped baseline

The working CLI includes build/install/help/version, strict YAML inventory,
side-effect-free inventory inspection, files and saved plans, DNF4/YUM4 plus systemd services, local/SSH transports,
noninteractive sudo, and bounded host concurrency. Secret execution remains
explicitly unsupported. First-version completion
still includes the upcoming work below. The verified Linux platform and real
acceptance harnesses are recorded in the guides and
[Linux validation skill](../../richt-linux-validation/SKILL.md).

Read these contracts instead of reconstructing syntax from older proposals:

- [Inventory](../../../../docs/inventory.md) owns schema, references, source and
  template path resolution, merge, selection, and resource ownership.
- [Files and saved plans](../../../../docs/files.md) owns templates, file
  attributes/SELinux, confirmation, executable JSON, locking, and continuation.
- [Services](../../../../docs/services.md) owns exact RPM requests, systemd
  capabilities, lifecycle branches, reactions, unit/drop-in handling, and removal.

Important invariants across those contracts:

- Inventory inspection never contacts hosts, opens templates, or reads secret
  values. Structural validation includes definitions outside selected scope.
  Weights increase in priority: scalars and lists replace; maps merge recursively.
  Equal-weight differing values or incompatible types are errors before effects,
  even if a higher weight would overwrite them. Tag listing order breaks no ties.
- Each selected host has one owner per explicitly declared package, normalized
  file path, and systemd unit, including managed units/drop-ins. Validate all its
  assignments before service filtering; identical settings or opposing target
  states do not permit sharing. Factor shared resources into a separate service.
- Host names and tags form one selection group, services another; groups
  intersect, repeated choices within a group form unions. Selecting a service
  neither assigns it nor automatically includes other services. Filters apply
  only during plan construction and are argument errors for saved-plan apply.
- Services run in assignment order; present phases are binary, config, process.
  `restart_on`/`reload_on` refer to `binary.<name>` or `config.<name>` in the same
  service; `after` orders its processes. Unknown/cyclic references fail validation.
  There are no cross-service links or arbitrary service dependency graphs.
- Actual linked changes combine into one lifecycle reaction per process;
  restart wins over reload. A stopped process targeting running gets only start;
  an active one gets the combined reaction or no action. Target stopped never
  permits start. Enablement is separate and does not itself restart a process.
- A lifecycle command and verified target state are one task. Start/reload/restart
  must reach running, stop must reach stopped. Command and polling share the
  process timeout, a positive Go duration defaulting to 5s. Running is not an
  application readiness check. Unexpected states, command failures, and timeouts
  fail the host; no retry or fallback. A timed-out client may leave a systemd job
  running. Unit changes visibly appear in diff and schedule daemon-reload.
- Without an RPM version, keep an installed package and install only if absent.
  Exact `epoch:version-release.arch` requests allow upgrade and downgrade. The
  plan fixes the package request, not repository metadata, dependencies, complete
  transaction, scriptlet effects, or RPM checksums; target DNF resolves those at
  apply. RPMs are fetched then and are not embedded as large plan artifacts.
- Files create necessary parents, require existing named owners/groups on the
  target, and support owner/group/mode and full SELinux labels. Ordinary file
  operations follow target and parent symlinks; removal unlinks the named entry.
  New plans retain owner/group names and resolve them again on apply; numeric
  IDs may change between preparation and execution. No special inode identity
  or symlink protection contract is imposed. Missing template variables fail preparation. Application config
  validation before writing remains later scope.
- `absent` stops and verifies processes in reverse dependency order, disables
  them before removing owned units/drop-ins and configs, then removes packages.
  Other services retain assignment order; no special cross-service deletion
  graph. External units are still stopped/disabled. DNF may remove dependencies;
  RPM handles package configs and possible `.rpmsave`. Richt does not purge
  application data, state, unrelated files, or `.rpmsave`. Removing an assignment
  alone does not request deletion.

## Executable plans and progress

Preparation and diff do not change managed resources. A plan fixes host targets,
task order, operations, ready file contents/patches and attributes, explicit
inventory connection inputs, and allowed process branches. Show real actions, parameters,
targets, order, checks, and branches through the same deterministic resolution
contract used for execution; a separate serialized copy of resolved commands is
not required. A future transport without shell shows its actual API operations.
Secrets must be hidden in every Richt-controlled display.

Apply executes prepared inputs without rereading inventory/templates/secrets,
rendering again, recalculating diff, or checking resources for drift. Rendered content and task composition remain fixed. Current system SSH
configuration may change between preparation and apply, and named ownership is
resolved again on the target. These are explicit exceptions to frozen inputs,
not reasons to recalculate tasks. Other incompatible backend semantics fail. Process state reads for planned branches and
result verification remain execution operations. Successful tasks are not
rechecked during continuation.

The current saved format is strict, indented JSON schema version `1.1` (with legacy local
`1.0` plans still accepted) with
validation of operations and progress. Its compatibility rules, task shapes,
atomic writes, same-plan locking, and restrictive plan/lock permissions are
implemented contracts, not undecided design. Extend them deliberately when new
capabilities require additional executable inputs.

The CLI displays saved and newly built plans before applying. Both require
explicit terminal agreement unless `-y` is supplied; missing TTY without `-y`
fails without changes. Piped input is not terminal agreement. `-y` skips only
confirmation; it does not suppress result or compatibility checks. The domain
executor is independent of confirmation and TTY.

Progress is per task: record running before execution, then success only after
result verification, or failure. With a file, atomically update that same file
and preserve the stable adjacent lock; a new explicit apply skips successful
tasks and retries unfinished ones. Interrupted tasks may already have taken
effect and may execute again: exactly-once execution is not guaranteed. A
progress save failure stops further work. Without a file, plan and progress live
only in memory and disappear when the run ends; the next invocation builds a
new plan. No autosave, separate progress store, run/attempt history, automatic
retry, rollback, or automatic replay of completed tasks.

A task failure blocks the remainder of its host, including later services.
Other hosts continue. On normal completion show all host results, successful and
previously completed tasks, failures, blocked tasks and reasons; include the
saved path and manual continuation instruction when applicable. Any failures
produce a nonzero exit. Forced termination may prevent a final report.

## SSH, sudo, and host concurrency

These are implemented first-version contracts:

- Reuse the existing domain operations and planner/executor for SSH; support
  mixed local/SSH plans without a second remote execution model.
- Preserve normal SSH-client behavior: `ssh_config`, aliases, connection
  settings, keys, SSH agent, and normal host-key/`known_hosts` verification.
  Users should not have to copy established SSH settings into Richt.
- Use the system OpenSSH client with current `ssh_config`, keys, agent, aliases,
  proxy settings, and normal host-key verification. Explicit inventory host,
  user, and port override corresponding client defaults; unspecified values use
  client configuration. Effective SSH configuration is not frozen in plans and
  may change between preparation and apply.
- Honor the existing host connection `sudo` flag; never escalate implicitly.
  Both privileged commands and file writes work for non-root users. SSH always
  uses batch mode without a TTY; sudo always uses `-n`. Password/passphrase and
  host-key confirmation prompts are not supported. Set up keys/agent, host trust,
  and noninteractive sudo before running Richt. `-y` skips only plan confirmation.
- Saved plans retain explicit connection inputs and prepared file bytes, not SSH
  credentials or snapshots of client configuration. Display the selected alias,
  explicit overrides, privilege mode, and current-client-config policy. Applying
  a saved plan does not reload inventory or rerender content.
- Bound simultaneous hosts to 20 by default, with a per-apply override including
  saved plans; 1 means sequential. The limit is executor configuration, never a
  selector or a reason to rebuild/change hosts, tasks, inputs, or progress.
- Preserve sequential services and process `after` within each host. A failing
  host must not stop other active or queued hosts. Await all hosts before the
  final summary. Synchronize shared progress and atomic persistence so parallel
  updates cannot lose another host's results; persistence failure still stops
  further work globally. No automatic retry.

Acceptance needs at least two isolated SSH targets with package/file/systemd
operations and a non-root sudo scenario. Verify client aliases, agent, host-key
checks, authentication/unreachable failures, default/override limits, within-host
order, and race-free progress. Fail one host deliberately and confirm queued
hosts finish; reapply its saved plan to skip successes and retry unfinished work.
Use Go race checking for concurrency/progress changes and authorize test hosts
according to the current task.

## Upcoming secret-source execution

Inventory already declares named `secret_sources` with `{type: yaml, file: ...}`;
paths resolve relative to the inventory file. Preserve that implemented syntax
and inventory-only behavior. Actual fetching and template evaluation remain
upcoming work:

- Use an extensible secret-source boundary. First implementation reads local
  YAML on the machine running diff; multiple sources of the same backend may
  read different files. Environment variables and external secret stores are
  later extensions.
- `secret` takes a key and optional source. Omitted source means the named
  `default`; an explicit source selects only that source. No merging, search, or
  fallback; identical keys in different sources are independent. Missing chosen
  source/key or read failure fails preparation. `default` is required only for
  calls omitting source, not when all calls select existing explicit sources.
- Fetch necessary values during preparation/diff. Retain real rendered bytes and
  patches, including secret values, in executable JSON. Apply must work after
  source files are changed or deleted, without lookup or rerendering. Do not
  replace executable secret inputs with display placeholders.
- A file is automatically sensitive when its preparation uses `secret`, OR when
  explicitly `sensitive: true`. Omission/false cannot cancel automatic marking.
  Persist sensitivity, including for units/drop-ins. Explicit true also protects
  old content when a new template no longer calls `secret`; no persistent secret
  history is required.
- Hide both old and new sides and all context of a sensitive file diff. Show
  only its path and fact of change. Compare actual bytes so secret changes still
  trigger intended process reactions. Partial value masking of file diffs is
  not the design.
- No secret values may escape through plan displays, commands, render/command
  errors, logs, summaries, or diagnostics, including multiline values and
  supported transformations. Define allowed functions/transformations and safe
  error handling; do not build tracking of every transformation for partial
  diffs. Existing explicit-sensitive redaction must continue working.
- JSON necessarily contains secrets. Preserve existing restrictive plan/lock
  permissions and choose any additional protection for plans and temporary files
  containing prepared secrets. Keep real secrets out of repository fixtures and
  test reports.

Remaining choices are the source value schema/nested key access, exact template
function calling convention within the accepted key/optional-source semantics,
allowed transformations and safe diagnostics, and additional protection of
secret-bearing files. Declaration fields and relative paths are already fixed
locally; do not reopen them as missing decisions.

Acceptance covers two sources with the same key, default/explicit selection and
errors, frozen saved bytes after source changes/deletion, real change reactions,
old/new/multiline/transformed-value redaction in every output path, and explicit
sensitivity when removing a secret from a template. Verify local and SSH/sudo
execution and saved-plan continuation.

## Upcoming first-version documentation

Each implementation ships a reproducible example and short usage/verification
instructions, plus relevant tests. Final documentation consolidates verified
build/install/privilege requirements, local/SSH use, final schema, selections,
templates/paths, processes/timeouts/removal, plan confirmation/progress and
continuation, concurrency, secrets, and limitations. Keep help, examples, and
guides synchronized. Include tested config-only and full local examples, two
SSH hosts, failure/continuation, and two YAML secret sources. Use fictional
addresses and dummy values; distinguish verified scenarios from illustrations.
Validate quickstart on a clean disposable environment with no hidden steps.
Do not defer implementation testing to a documentation-only completion step.

## Later scope

NetBox inventory/plugin/rendering, URL inventory and template sources, direct
URL binary downloads/integrity/unpacking, other package managers, script-based
process backends, external secret stores, application config validation, agents,
and fleet rollout orchestration are later work. Preserve useful domain boundaries
without building empty layers or advertising unsupported implementations.

Future NetBox inventory supplies the complete host/tag/assignment inventory;
mixing local and remote inventory is not the accepted model. Service definitions
and template sources are independent concerns. Mapping NetBox models remains
open until that integration is implemented.

The longer-term direction is a Linux agent that obtains desired configuration
through HTTPS, submits host facts/diffs, and receives permission to apply a
specific plan. A NetBox plugin could show host state, diffs, history and apply
controls for a host/group. Agents use APIs rather than direct Redis/PostgreSQL
access. A packaged plugin with idempotent bootstrap of schemas, templates, and
permissions should make setup straightforward. This direction does not add
agent execution, history, or mandatory central services to the current CLI.
