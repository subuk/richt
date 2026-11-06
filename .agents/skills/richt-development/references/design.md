# Richt design and roadmap

This is the repository's consolidated accepted design context. Use the current
request with the relevant sections here and the focused guides linked below.
The guides define implemented syntax and behavior; this reference records the
completed first-version baseline and later scope. Update both when a task
intentionally changes a contract. Later extensions are not shipped capabilities
or unfinished first-version requirements, nor are they permanently forbidden.

Contents: [architecture](#architecture-and-domain),
[shipped baseline](#shipped-baseline), [plans](#executable-plans-and-progress),
[SSH and concurrency](#ssh-sudo-and-host-concurrency),
[oneshot processes](#oneshot-processes), [secrets](#secret-source-execution),
[first-version completion](#first-version-completion-and-documentation), and
[later scope](#later-scope).

## Architecture and domain

Richt is a declarative Linux host configuration tool implemented as a standalone
Go CLI. It prepares desired configuration, observes resources to calculate a
diff, displays an executable plan, and explicitly applies that plan. NetBox,
Argo CD, and Ansible are not required dependencies of the local core.

A host has a stable name, tags, and connection
settings. A service is a reusable logical unit containing flat ordered lists of
binary, config, and process components; at least one list must be nonempty.
Items have optional nonempty string `name` labels, unique within each
collection; only explicitly named binaries/configs can be referenced. Names
do not determine execution order or task identifiers.
Config-only services can establish repositories before later package services;
binary-only and process-only services are equally valid. A service and an
individual managed process are different concepts.

Tags assign services through mappings keyed by service name. Assignment values
specify only target state (`present` by default, or explicit `absent`). Hosts
receive all their tags' assignments; duplicate assignments are errors even with
identical settings. Each service definition requires an integer `order`
alongside its components. Inline definitions declare it in inventory; a `file`
reference loads it from the referenced service file and forbids `order` beside
`file`. Explicit zero is valid.
Effective services execute in ascending order, with per-host order uniqueness;
unassigned or separately assigned services may share an order. Component parameters, units,
overrides, and timeouts belong to service definitions, with no per-assignment
overrides. Content parameterization happens in templates using the host's full
merged variables. Tags also supply variables, processed in ascending tag `order`
(default zero). Tag order and service execution order are independent. This is
the accepted KRG-91 contract from 2026-11-02, with service-file ordering amended
on 2026-11-05.

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
side-effect-free inventory inspection, files and saved plans, DNF4/YUM4 plus
systemd and change-triggered oneshot processes, local/SSH transports, noninteractive sudo, bounded host
concurrency, and local YAML secret sources. The agreed first-version scope is
complete: KRG-69 through KRG-75 are Done in Linear as of 2026-11-01, including
documentation, executable examples, and the published documentation site.
The verified Linux platform and real acceptance harnesses are recorded in the guides and
[Linux validation skill](../../richt-linux-validation/SKILL.md).

Read these contracts instead of reconstructing syntax from older proposals:

- [Inventory](../../../../docs/inventory.md) owns schema, references, source and
  template path resolution, merge, selection, and resource ownership.
- [Services](../../../../docs/services/_index.md) owns composition, lifecycle
  order, reactions, and component references: exact RPM requests, file templates
  and attributes/SELinux, systemd capabilities, unit/drop-in handling, and removal.
- [Saved plans and apply](../../../../docs/files.md) owns preparation,
  confirmation, executable JSON, locking, and continuation.
- [Secrets](../../../../docs/secrets.md) owns YAML source values, template lookup,
  sensitivity, safe diagnostics, and secret-bearing plan handling.

Important invariants across those contracts:

- Inventory inspection never contacts hosts, opens templates, or reads secret
  values. Structural validation includes definitions outside selected scope.
  Tag orders increase in priority: scalars and lists replace; maps merge recursively.
  Equal-order differing values or incompatible types are errors before effects,
  even if a higher order would overwrite them. Tag listing order breaks no ties.
  All tag assignment references and service orders are checked globally;
  duplicate assignments, service-order collisions and resource ownership are
  checked on selected hosts before service filtering. `--tag` selects hosts,
  each of which still uses all its tags. Inspection retains flattened effective
  services with their order and state.
- Each selected host has one owner per explicitly declared package, normalized
  file path, and systemd unit, including managed units/drop-ins. Validate all its
  assignments before service filtering; identical settings or opposing target
  states do not permit sharing. Factor shared resources into a separate service.
- Host names and tags form one selection group, services another; groups
  intersect, repeated choices within a group form unions. Selecting a service
  neither assigns it nor automatically includes other services. Filters apply
  only during plan construction and are argument errors for saved-plan apply.
- Services run in service-definition order; present phases are binary, config, process.
  Each collection executes in declaration order. `restart_on`/`reload_on`/`run_on` refer
  to explicitly named `binary.<name>` or `config.<name>` in the same service.
  Unknown/duplicate references fail validation. There is no component dependency
  graph or cross-service link.
- Actual linked changes combine into one lifecycle reaction per process;
  restart wins over reload. A stopped process targeting running gets only start;
  an active one gets the combined reaction or no action. Target stopped never
  permits start. Enablement is separate and does not itself restart a process.
- Systemd `masked: true` requests a persistent mask; false removes persistent
  and runtime masks, and omission preserves masking. Masking requires stopped
  and cannot enable a unit or coexist with an owned full unit at the mask path.
  Explicit unmask precedes owned file writes, enablement and lifecycle; masking
  follows requested stop/disable. Absent assignments preserve mask state.
- Systemd processes accept `.service` and `.socket` units. Socket `running`
  means ActiveState=active, not an active application service. Socket operations
  do not infer or manage the activated service; dependencies remain systemd's
  responsibility. Socket units support restart reactions but not reload, and
  service-only ExecReload/notify-reload settings cannot prove socket capability.
- A lifecycle command and verified target state are one task. Start/reload/restart
  must reach running, stop must reach stopped. Command and polling share the
  process timeout, a positive Go duration defaulting to 5s. Running is not an
  application readiness check. Unexpected states, command failures, and timeouts
  fail the host; no retry or fallback. A timed-out client may leave a systemd job
  running. Unit changes visibly appear in diff and schedule daemon-reload.
- Without an RPM version, keep an installed package and install only if absent.
  Short upstream VERSION requests match installed RPM VERSION independently of
  epoch, release and architecture. Apply passes `NAME-VERSION` to ordinary
  DNF/YUM install; the package manager resolves omitted epoch, release and
  architecture using its normal policy. Exact `epoch:version-release.arch`
  requests allow upgrade and downgrade. The plan fixes the package request, not repository metadata, dependencies, complete
  transaction, scriptlet effects, or RPM checksums; target DNF resolves those at
  apply. RPMs are fetched then and are not embedded as large plan artifacts.
- Files create necessary parents, require existing named owners/groups on the
  target, and support owner/group/mode and full SELinux labels. Template writes
  replace the final entry without dereferencing symlinks, including dangling
  links; removal unlinks only that entry. Atomic replacement changes only the
  declared hardlink entry. Parent symlinks remain followed. Plans retain
  owner/group names and resolve them again on apply; numeric IDs may change. Missing template variables fail
  preparation. Optional config validators run at apply against private
  target-side candidate files before writing; command arguments and timeout are prepared inputs.
  Validation and writing share one task, and failure preserves the destination
  and blocks later reactions. Matching content, attribute-only changes, and
  removal do not run validation. Validator output is never included in diagnostics.
- `absent` skips oneshots and traverses systemd processes in reverse declaration order, stopping and
  verifying each before disabling it. These tasks precede removal of owned
  units/drop-ins and configs. Configs use reverse config-list order, then
  packages use reverse binary-list order.
  Other services retain service-definition order; no special cross-service deletion
  graph. External units are still stopped/disabled. DNF may remove dependencies;
  RPM handles package configs and possible `.rpmsave`; package removal does not
  purge unrelated application state. Explicit typed-directory and block-resource
  absence deletes their contents and data as described below. Removing an assignment
  alone does not request deletion.

## Local user and group configs

The accepted account extension adds ordered `user` and `group` configs, using
local GNU getent observations and shadow-utils commands through local/SSH
transports. See [local accounts](../../../../docs/services/configs/accounts.md)
for fields, defaults, membership, capability checks and removal semantics.
Names and explicit numeric IDs have one owner per selected host, checked before
service filtering. Whole-file ownership of account databases conflicts with
account configs. UID/GID migration and renaming remain unsupported.

Preceding groups and users are projected during preparation so ordinary file,
directory, template and owned systemd-file configs can use accounts created by
the same plan. Config order remains explicit. Owned process definitions are
removed before reversed configs, allowing their owners to be removed afterwards.
User removal preserves home data and same-name groups through a private mount
namespace with a private login.defs policy; host policy is unchanged. This
requires the documented namespace capability. Changing a user's primary group
retains shadow-utils' documented home-file group ownership side effect.

KRG-105 adds optional `append_groups` to user configs alongside
`supplementary_groups`. True ensures the listed local supplementary memberships
while preserving all others, including additions after preparation. False or
omission retains exact-list behavior. An explicit flag requires a list; an
empty list clears memberships in exact mode and is a membership no-op in append
mode. Omitted membership settings remain unmanaged. Primary membership does
not implicitly satisfy a supplementary request. Existing local/NSS checks and
preceding-group projection also apply to additive requests. Plans freeze the
list and mode; append execution and unfinished retries verify a subset instead
of an exact set. Service recipes remain responsible for restarting processes
to acquire changed credentials.

The account operation was introduced in version 1.6 and is retained in 1.8. It fixes creation,
update or deletion inputs, existing numeric identities and group dependencies.
Execution checks those preconditions and verifies results; an unfinished task
may accept its already matching result. Automatic IDs are allocated at apply,
so creation retries cannot distinguish an independently created account that
satisfies the same requested attributes and membership mode. Passwords, external directory services,
SSH keys, account expiry and login policy remain outside this extension.

## Executable plans and progress

RPM requests optionally retain `disable_excludes`, a single DNF repository ID
or the reserved `main`/`all` scopes. The validated `--disableexcludes=VALUE`
argument applies to each requested install or removal transaction through DNF4
or YUM4 without editing repository policy. It disables both exclusion and
inclusion filters in that scope. Omission preserves backend defaults; changing
only the override never changes installed-package matching or triggers a
reinstall/reaction. Prepared plans display and freeze the value. This additive
optional input was introduced in saved format 1.5 and remains supported in the
current 1.8 schema.

Preparation and diff do not change managed resources. A plan fixes host targets,
task order, operations, ready file contents/patches and attributes, explicit
inventory connection inputs, and allowed process branches. Show real actions, parameters,
targets, order, checks, and branches through the same deterministic resolution
contract used for execution; a separate serialized copy of resolved commands is
not required. A future transport without shell shows its actual API operations.
Secrets must be hidden in every Richt-controlled display.

Apply executes prepared inputs without rereading inventory/templates/secrets,
rendering again, recalculating diff, or performing a general drift comparison. Prepared storage and mount identity,
signature, consumer, and fstab preconditions are checked at execution. Template
operations also verify frozen prior contents/presence/type or symlink text. Rendered content and task composition remain fixed. Current system SSH
configuration may change between preparation and apply, and named ownership is
resolved again on the target. These are explicit exceptions to frozen inputs,
not reasons to recalculate tasks. Other incompatible backend semantics fail. Process state reads for planned branches and
result verification remain execution operations. Successful tasks are not
rechecked during continuation.

The current saved format is strict, indented JSON schema version `1.8`.
This is a breaking change: reject every older version before execution, with a
clear instruction to prepare a new plan. There is no migration or legacy
execution path. Task IDs use original zero-based list positions and operations,
never optional name labels, and remain stable within a saved plan rather than
across inventory reordering. Same-plan locking and restrictive plan/lock
permissions remain implemented contracts.

Template configs and owned systemd units/drop-ins default to
`atomic_writes: true` and `backup: false`; other config types reject these fields.
Each prepared file operation fixes a unique 32-character lowercase hex ID,
explicit prior and desired entry states, prior metadata, and an adjacent
`<path>.<UTC timestamp>.<operation ID>~` backup path when applicable. Regular
prior hashes come from the exact diff bytes; symlinks retain link text without
reading their referent. Expected metadata includes numeric UID/GID, mode and
base64-valued xattrs. Desired regular states contain the prepared bytes' digest.
Changed contents/presence/type/link text conflict. Identical bytes in a new
inode and attribute-only external edits are accepted. Checks apply to both
write modes and removals, independently of backups, and are not compare-and-swap:
external writers can race after verification.

Atomic publication validates private original prepared bytes, stages on the
destination filesystem, preserves/applies current intended ownership, mode,
ACLs, xattrs and SELinux metadata from current regular files, syncs the staged
regular file and publishes
with one rename. No pre-publication failure changes the original destination;
there is no fallback to in-place writes. Explicit non-atomic mode permits
partial/truncated regular contents and hardlink shared-inode effects on failure;
it still replaces final symlinks rather than following them. A regular file
replacing a symlink uses new-file defaults (0644, executing identity, destination
default SELinux label) unless explicit attributes are requested; link metadata
is retained only in its backup. An unfinished write repeats validation and
publication, including if desired bytes are already installed. The Python 3
standard-library helper is shared by local and SSH/sudo execution; Unix
filesystem support is required, with Linux needed for SELinux.

Opt-in backups are independent regular copies or copies of the symlink itself,
preserving prior metadata without broadening access. Only replacement/removal
of an existing entry needs a backup; unchanged/attribute-only operations and
creation need none. Make backups durable before mutation and sync affected
directory entries afterwards. A private adjacent `<backup path>.richt.json`
receipt records operation ID and actual backup state/metadata, allowing live
attribute preservation while retaining frozen content preconditions. Never
overwrite collisions; continuation verifies and reuses the same backup and
receipt. Unfinished tasks can recognize desired contents/type or absence, but
must still satisfy backup, attributes and durability requirements. Missing
required old backups cannot be recreated from desired bytes. Post-publication
durability/progress failure may mean the desired state is already installed.
Backups live on the target and paths appear even for unsaved runs. Keep the
plan, backup and receipt for recovery or future reversal; there is no rollback
command, rotation, cleanup or history service, and file restoration alone does
not restore application runtime state.

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
- Preserve sequential services and component list declaration order within each
  host. A failing host must not stop other active or queued hosts. Await all hosts before the
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

## Oneshot processes

A `type: oneshot` process executes a fixed nonempty `command` argument vector
without implicit shell interpretation. Its nonempty `run_on` list references
explicitly named binaries/configs in the same service. Creating a config,
changing its content or attributes, or changing a referenced binary schedules
one execution per process; multiple changed references coalesce. Unchanged
inputs do not schedule a command. Oneshots have no systemd state, enablement,
mask, unit files, or restart/reload operations, and reject those fields.

Mixed systemd/oneshot processes run in list order in the present process phase,
after binaries and configs. Subscriptions do not establish order. Absence skips
oneshots, with no removal hooks. Oneshot execution requires no systemd backend;
package changes only schedule systemd daemon reload when systemd processes are
present (changed systemd definition files retain their existing reload rule).

Preparation never executes the command. Plans freeze its argv, positive timeout
(default 5s), changed trigger references, position-based identity, and task order.
Display the actual command, triggering components, and exit-zero success check.
Apply uses the target's current executable; executable bytes are not embedded.
Local and SSH transports retain explicit noninteractive sudo behavior. Stdout
and stderr are discarded for all oneshots; errors retain safe categories and
exit status. Timeout fails the task and does not promise remote termination.

Successful commands are skipped on saved-plan continuation; unfinished commands
are retried and may repeat effects after interruption. If a config succeeded
before its oneshot failed, continue that saved plan to retry the command. A new
diff may see an unchanged config and schedule nothing. There is no persistent
trigger state, autosave, automatic retry, rollback, or exactly-once guarantee.
The [oneshot reference](../../../../docs/services/processes/oneshot.md) and
[sysctl example](../../../../examples/oneshot/README.md) own usage details.

## Typed configurations

KRG-96 extends flat ordered configs with `file`, `directory`, `mdadm`, `lvm-vg`,
`lvm-lv`, and `mount`; omitted type remains `template`. A config is not necessarily
a rendered file. The optional `name` only labels reactions. Resource fields are
`array/level/metadata/members`, `vg/devices`, `vg/lv/size`, and
`source/target/filesystem/options`. Strict per-type validation rejects unrelated
fields; systemd-owned files remain restricted to template fields.

Plain files preserve existing contents and create missing files empty.
Directories preserve children while present and manage only their own explicit
attributes. Defaults are 0644 for new files and 0755 for new directories;
parents are created as necessary. Explicit absence removes files or recursively
removes directory contents, without crossing active mount boundaries.

Initial storage support is RAID1 with metadata 1.2, ordinary linear LVM, and
XFS. Optional string `filesystem: xfs` on arrays/LVs requests safe creation or
verification. Omission leaves filesystem state unmanaged. VGs initialize their
backing PVs. Existing compatible resources are adopted; conflicting signatures,
unsupported layout changes and substituted devices fail. No partitioning,
resize, reshape, implicit reformatting, or secure erasure is provided.

Preparation follows declared sequence and projects outputs of earlier configs,
including devices not yet created and earlier explicit deletions. This is not a
dependency scheduler. Inventory inspection stays structural. Apply executes
fixed prepared operations and safety checks without reloading inventory or
recomputing changes. Resource UUIDs and creation tags identify interrupted
operations; successful tasks remain skipped. Local and SSH execution share the
same resource commands and explicit noninteractive sudo boundary.

Mount configs require both an active mount and an fstab entry. They persist
filesystem UUIDs, preserve unrelated fstab contents, and compose frozen edits
in list order. Mounts never format their source. Preparation must not inspect
hidden mount-point contents as the intended filesystem; only a freshly created
filesystem is known empty. Existing unmounted filesystems must be mounted before
preparing their contents. Existing inactive storage must be activated before
preparing managed filesystem state. Boot activation depends on Linux mdadm,
LVM and systemd configuration; loop attachment for acceptance is test scaffolding.

Absence stops application processes first, then traverses configs in reverse:
unmount, LV removal, empty-only VG removal and explicit PV cleanup, then MD stop
and member metadata removal. Remaining declared or undeclared LVs block VG
removal. Busy resources and unrelated consumers fail rather than being forced.
Removal includes data on adopted resources, and the plan displays its scope.
Unfinished destructive operations verify identities to reject replacements.
Service order remains ascending; removing an assignment alone removes nothing.
The config references and executable-plan guide describe the supported checks.

## Secret-source execution

Inventory declares named `secret_sources` with `{type: yaml, file: ...}`;
paths resolve relative to the inventory file. Inventory inspection never opens
sources. Preparation implements these contracts:

- The extensible secret-source boundary reads local
  YAML on the machine running diff; multiple sources of the same backend may
  read different files. Environment variables and external secret stores are
  later extensions.
- `{{ secret "database.password" }}` takes a key and optional source, as in
  `{{ secret "database.password" "production" }}`. Omitted source means the named
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
  supported transformations. Standard Go text/template functions remain
  available; parse and execution errors use fixed safe messages rather than
  quoting source, arguments, or partial results. No transformation tracking or
  partial masking is introduced. Existing explicit sensitivity remains supported.
- JSON necessarily contains secrets. Plan, lock, and atomic replacement files
  are created with mode 0600. Temporary plan files are removed on completion or
  handled errors; forced termination can leave a private temporary file behind.
  Target content travels through stdin without command arguments. Optional
  validators use private temporary candidate files on the target; those are
  removed on normal success/failure/timeout, though abrupt termination may leave
  private temporary files behind. Target permissions remain explicitly
  controlled by `mode`.
  Atomic writes stage private bytes and apply intended metadata before
  publication. Explicit in-place sensitive writes with a requested mode restrict
  the inode to 0600 before writing, then apply final attributes. Backups retain
  prior permissions and may contain secrets; their receipts are private.
  Without an explicit mode, existing permissions and the new-file default remain.
  Keep real secrets out of repository fixtures and test reports.

YAML sources contain one mapping, with nested mappings and string leaves.
Keys are nonempty strings without dots; dot-separated lookup selects nested
values. Lists, nulls, non-string values/keys, duplicate keys, custom tags,
aliases/merge keys, and extra YAML documents are rejected. One resolver lazily
loads each selected source once per diff and caches its values or failure for
that preparation only. No values are fetched for unused sources or absent
files. Use explicit `sensitive: true` to hide old content during removal, or
when a new template no longer invokes `secret`.

Acceptance covers two sources with the same key, default/explicit selection and
errors, frozen saved bytes after source changes/deletion, real change reactions,
old/new/multiline/transformed-value redaction in every output path, and explicit
sensitivity when removing a secret from a template. Verify local and SSH/sudo
execution and saved-plan continuation.

## First-version completion and documentation

The agreed first version was completed on 2026-11-01. KRG-69 through KRG-74
delivered the CLI, inventory, files and plans, RPM/systemd, SSH/sudo and host
concurrency, and YAML secret sources. KRG-75 completed documentation and verified
usage scenarios in commit `d33accc`. The [documentation index](../../../../docs/_index.md)
covers build/install/privilege requirements, local/SSH use, schema, selections,
templates/paths, processes/timeouts/removal, plan confirmation/progress and
continuation, concurrency, secrets, and limitations. The
[documentation site](https://richt.dev/) is published. The custom domain was
initially deferred from the first version; subsequent deployment uses GitHub
Actions to build, validate, and publish the site from `main` to GitHub Pages.

The [validation report](../../../../docs/validation.md) records passed checks on
two disposable AlmaLinux 10.2 hosts, including files/SELinux, published Vmango
RPMs, systemd, SSH/sudo, secrets, and failure/continuation. The accepted final
validation scope used published RPMs only; custom fixture RPM suites were
excluded by explicit scope decision. The report's coverage limits describe
what was verified, not outstanding first-version tasks.

Inventory and Services are the central documentation concepts. Inventory
describes hosts, tags, assignments, variables, and selection; Services explains
reusable component composition, execution order, and reactions before the
implementation references. RPM implements binary components; systemd and oneshot implement process
components. Configs use templated files with an optional
`type: template` discriminator, which is also the default when omitted. New
implementations extend their component reference lists; the documentation must not advertise them before they are supported.

For subsequent changes, keep help, examples, and guides synchronized. Ship
reproducible examples, short usage/verification instructions, and relevant tests
with each implementation. Use fictional addresses and dummy values, distinguish
verified scenarios from illustrations, and validate walkthroughs on a clean
disposable environment without hidden steps. Do not defer implementation testing
to documentation work.

## Downloaded binary artifacts

KRG-82 and KRG-83 add `download` files and `archive` trees to ordered binaries.
Both require a public HTTPS URL, SHA-256 and absolute destination. Inventory
inspection stays structural; diff observes actual target bytes and attributes
without fetching artifacts. Apply downloads on the target using the frozen
request, checks the checksum, and publishes verified staging contents atomically.
Artifact bytes are not embedded in JSON. This is an explicit execution-time
dependency. Same-checksum mirror changes alone do not change installed state.

The target requires Linux, Python 3.8+ in isolated mode, and filesystem support
for `renameat2` no-replace/exchange. The helper is a bounded command, not an
installed agent. System TLS trust/proxy settings apply; no authentication or
verification bypass options exist. Source URLs reject credentials, queries and
fragments; HTTPS redirects may carry temporary signed queries that are never
persisted or displayed. Bounds cover redirects, time, downloaded/decompressed
bytes, entries, paths and tar metadata. The component references own exact limits.

`download` owns one regular file, default mode 0755. `archive` owns its entire
directory, supports only `tar.gz` and optional `strip_components`, installs
directories as 0755 and files as 0644/0755 according to executable bits. Safe
relative symlinks are allowed; hardlinks, sparse files and special entries are
rejected. Archive ownership IDs and extended attributes are not trusted.
Owner/group names or IDs are explicit; omission uses the execution identity.

Neither type adopts an existing unowned destination. A protected sibling
metadata directory holds a stable resource lock, current manifest and temporary
publication journal. Root identity/generation preconditions prevent replacement
of another installation. Same-type tracked child edits are managed drift;
unknown entries or changed types block archive replacement/removal. Metadata
paths and archive subtrees are exclusive across binaries, configs and units.
Target alias checks enforce this before observation; artifacts inside an
unmounted declared filesystem require a separate mount apply first.

Whole-resource publication and its journal permit manual continuation after
interruption, including completion of old-content cleanup after publication.
They do not add global rollback, automatic retries or run history. Successful
tasks remain skipped. The archive tree must not contain application state or
independently managed configs. Absent follows normal process/config/binary
ordering and retains the stable metadata lock. Actual artifact changes feed
the existing restart/reload/oneshot reactions; only RPM changes imply package
unit discovery or package-triggered daemon reload.

See [downloaded binaries](../../../../docs/services/binaries/download.md) and
[archive binaries](../../../../docs/services/binaries/archive.md).

## Later scope

NetBox inventory/plugin/rendering, URL inventory and template sources,
authenticated artifact sources and other archive formats, other package managers, additional persistent
process managers, external secret stores, multi-file configuration validation
transactions, agents, and fleet rollout orchestration are later work. Preserve useful domain boundaries
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
