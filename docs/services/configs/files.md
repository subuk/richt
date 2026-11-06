---
title: "Files and templates"
description: "Prepare file contents, permissions, ownership, SELinux labels, and sensitive output."
weight: 10
---

```yaml
configs:
  - name: main
    path: /etc/example-app.conf
    owner: root
    group: root
    mode: "0644"
    selinux: system_u:object_r:etc_t:s0
    sensitive: false
    atomic_writes: true
    backup: false
    validate:
      args: [/usr/sbin/example-check, --config, "{candidate}"]
      timeout: 5s
    template:
      inline: "listen = {{ .app.listen }}\n"
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `name` | no | Nonempty reference label, unique within the config list |
| `type` | no | `template` (default); other types use their own [config fields](_index.md) |
| `path` | yes | Absolute target file path, normalized during resolution |
| `template` | yes | Exactly one of `file` or `inline`; an empty inline string is valid |
| `owner` | no | Target user name; omission preserves existing ownership |
| `group` | no | Target group name; omission preserves existing ownership |
| `mode` | no | Quoted octal string such as `"0644"`; omission preserves existing permissions; new files default to 0644 |
| `selinux` | no | Full SELinux label; omission leaves the label unmanaged |
| `sensitive` | no | Defaults to false; template secret lookup automatically makes it true |
| `validate` | no | Target-side validator argv and positive timeout; timeout defaults to `5s` |
| `atomic_writes` | no | Boolean, defaults to `true`; explicit `false` selects weaker in-place writes for regular files |
| `backup` | no | Boolean, defaults to `false`; `true` preserves the prior object before content replacement or removal |

The file fields also describe [systemd-owned units and
drop-ins](../processes/systemd.md#owned-units-and-drop-ins), whose paths can be
derived from the process. Ordinary configs require `path`. Config list items
accept `type: template`; omission has the same meaning. Configs have no separate
per-file target state; they use the service assignment's `present` or `absent`
state. `unit_file` remains one object, and `overrides` remains a mapping of
drop-in basenames to file objects. Owned file objects also accept optional
`type: template`, `atomic_writes`, and `backup`. Typed `file`, `directory`,
storage, mount, and account configs reject these template write options. Only
config-list entries provide labels for reaction references.

## Templates and preparation

`template.file` reads a local template on the machine preparing the plan,
including for SSH targets. Paths are relative to the declaring service file;
inline services use the inventory file as their base. Absolute local paths are
accepted; URL sources are unsupported. These source paths are independent of
the absolute target `path`. See [inventory file
references](../../inventory.md#schema-and-file-references).

Templates use Go `text/template` with the host's complete merged variables.
Missing variables fail preparation. Standard built-ins such as `printf`, `index`,
and `slice` remain available. For example:

```yaml
template:
  file: templates/example-app.gotmpl
```

```gotemplate
listen = {{ printf "%q" .app.listen }}
password = {{ printf "%q" (secret "database.password" "production") }}
```

The [`secret` function](../../secrets.md) retrieves a string from a declared
local YAML source and automatically marks the file sensitive. Parse and execution
failures use safe generic diagnostics so template text or transformed secret
values cannot escape through errors.

Diff renders contents and compares actual bytes and requested attributes with
the target, without modifying managed resources. A config change can drive a
[process reaction](../reactions.md), including when only attributes changed.
Optional validation runs during apply, before a content write.

## Validation before writing

```yaml
validate:
  args: [/usr/sbin/sshd, -t, -f, "{candidate}"]
  timeout: 5s
```

`validate.args` is a nonempty argument list whose first item is an absolute
executable path. Exactly one later item must be the whole string `{candidate}`;
embedded or repeated placeholders and NUL bytes are rejected. Richt replaces
that argument with a private file containing the prepared bytes. Arguments are
passed literally: there is no shell evaluation, environment expansion, or
template rendering. Use absolute paths for other validator inputs. The working
directory is always `/`, and validator stdin is empty.

Inventory inspection checks only this structure. Diff renders and compares
files without executing the validator or checking whether its executable
exists. A preceding package task can install it. Reviewed plans retain the
argument list and timeout with the rendered bytes; applying a saved plan does
not reload them from inventory. Sensitive displays show the validation and
timeout while hiding its arguments.

The validator runs with the file task's target identity and explicit sudo
setting, locally or over SSH. Its candidate is mode 0600 inside a mode 0700
directory under `/tmp` on that target. Include paths and other application
dependencies still refer to the target's current environment; a private
candidate does not stage a complete application directory tree. A validator
must accept an independently named candidate file.

Exit status zero permits backup preparation and publication to continue. A
nonzero exit, unavailable executable, or timeout fails the file task before the
destination is changed, and blocks later host tasks,
including reloads and restarts. Validator stdout and stderr are always discarded
because even ordinary application diagnostics can reveal file contents. A
positive Go duration bounds the command; target commands use GNU coreutils
`timeout`. Candidate files and directories are removed on success, failure, and
timeout. Abrupt host termination, process termination, or a broken SSH session
can prevent cleanup; remaining candidates retain their private permissions.

Validation runs only when the prepared task writes content. It is skipped for
unchanged content, attribute-only changes, and removals. Validation, backup,
publication, and attributes share one persisted file task.
An unfinished write repeats validation of the prepared bytes and publication,
including when its desired bytes are already installed; explicit continuation
skips an already successful task. Richt always writes
the original prepared bytes, even if the validator modifies its candidate. An
invalid saved candidate requires a newly prepared plan; editing its source
inventory does not repair frozen saved inputs.

## Apply, conflicts, and attributes

Each prepared template operation freezes a unique operation ID, explicit prior
and desired states, and a backup path when needed. For a regular file, the prior
SHA-256 comes from the exact bytes used for the diff. Absence is explicit;
symlinks retain their link text and applicable link metadata without reading
the referent. Saved plans retain observed ownership, mode and extended
attributes for backup verification and future reversal.

Before writing or removing, apply checks the final entry against its prepared
prior contents, presence, type, or symlink text. An intervening content edit,
appearance, disappearance, type change, or changed link text fails with a
conflict and blocks later host tasks and reactions. Identical regular bytes in
a replacement inode are accepted; attribute-only external edits are accepted.
These checks apply with either write mode and with or without backups. They do
not recalculate the diff or reread templates or secrets. A separate writer can
still change the entry between checking it and mutation: checksum checking and
rename are not compare-and-swap and do not eliminate external-writer races.

Apply creates necessary parent directories. Existing unspecified ownership,
permissions, ACLs, applicable extended attributes and SELinux context are
preserved from the current regular entry. New files, including regular files
replacing symlinks, default to mode 0644, the execution user's normal ownership,
and the destination's default SELinux label. A replaced symlink's metadata is
preserved in its backup rather than inherited by the new regular file. Explicit owner/group names must exist on
the target or be created by preceding [local account configs](accounts.md).
They are resolved during observation and again at apply, so numeric IDs may
change. A `selinux` request manages the full label and requires an
SELinux-enabled Linux target whose kernel accepts that label; requests fail
explicitly on other systems.

With the default `atomic_writes: true`, Richt stages a regular file on the
destination filesystem, applies its intended metadata and syncs its contents
before publishing it with one rename over the destination. The old entry stays
in place while validation, staging and backup preparation run. There is no
missing-path window from moving the destination into a backup first. A
pre-publication validator, backup, or precondition failure leaves the destination
unchanged. Atomic publication failures are errors; there is no automatic
fallback to in-place writes. File operations use a target-side Python 3 helper
and its standard library, locally and over SSH with the selected sudo identity;
Unix filesystem support is required, with Linux required for SELinux metadata.

The final symlink is replaced by a regular file at the declared name, including
when dangling. Richt does not manage or modify its referent. Removal unlinks
only the declared entry. Atomic replacement of a hardlinked regular file changes
only that directory entry; its other names retain the old inode. Parent
symlinks continue to be followed; there is no recursive parent-symlink tracking
or general inode-identity protection.

Explicit `atomic_writes: false` permits in-place writes to regular files. Readers
can observe partial content, and interruption can leave truncated or partial
bytes; hardlink aliases share those changes. The final symlink is still replaced
with a regular file rather than followed. Conflict checks, validation, backup
requirements, and task failure behavior still apply.

## Backups and continuation

`backup: true` creates a backup only before a prepared content replacement or
removal of an existing regular file or symlink. Attribute-only changes,
unchanged regular files, and creation at an absent path create no backup.
The prepared adjacent name is
`<path>.<UTC timestamp>.<operation ID>~`, for example
`/etc/example-app.conf.20261106T120000.000000000Z.0123456789abcdef0123456789abcdef~`.
The plan display exposes this frozen path even for an unsaved run, for manual
recovery. Backups live on the target; their bytes are not embedded in the plan.

Regular backups are independent copies, including for hardlinked originals.
Symlink backups preserve the link itself and applicable link metadata, never
its referent. Backups preserve prior ownership, group, permissions, ACLs and
applicable extended attributes/SELinux context without broadening access.
After successful validation, Richt makes the backup durable before destructive
mutation, syncs the new regular file before publication, and syncs affected
directory entries afterwards. A valid backup may remain after a later failure.

An adjacent private receipt, `<backup path>.richt.json`, records the operation
ID and actual backup state and metadata. It allows attribute-only external edits
to remain acceptable while verifying the exact backup created for this
operation. Backup and receipt creation never overwrite an existing entry;
unrelated collisions or damaged/mismatching backups fail closed. Explicit
continuation verifies and reuses the same operation's backup and receipt.
It never replaces the old-state backup with already-installed new contents.

Successful tasks remain skipped. For an unfinished task, the prepared desired
contents/type, or an already-absent removal, can be recognized after an
interrupted publication. Continuation still checks required backups, completes
attributes and durability, and fails if an original required backup is missing.
Matching desired bytes alone cannot replace that backup requirement. A
post-publication durability or progress error can mean the new state is already
installed; explicit continuation handles the saved operation. Richt does not
roll back automatically. Partial in-place writes can require manual recovery
before a fresh plan or continuation can proceed.

Keep the saved plan, referenced backup, and receipt for recovery and potential
future reversal. There is no rollback command, automatic rotation, backup
cleanup, or configuration history service. Restoring a file alone does not
restore application runtime state. See [saved plans and apply](../../files.md).

## Sensitive files

A `secret` call or explicit `sensitive: true` hides the entire old/new diff,
including context, and protects file error details. Explicit false cannot cancel
automatic sensitivity. Real bytes still determine changes and reactions. Set
`sensitive: true` to protect old content when a new template no longer calls
`secret` or when removing a service; Richt retains no persistent secret history.

Sensitivity controls display, not final target or backup permissions. Set
`mode: "0600"` for private target files. Atomic staging keeps candidate bytes
private until the intended metadata is applied and the complete file is
published. Explicit in-place sensitive writes with a requested mode restrict
the inode to 0600 before writing and apply final attributes afterwards; a
failure may leave that restrictive mode in place. Backups retain the prior
object's permissions and may contain real secrets; protect them accordingly.

Saved JSON contains real rendered content and patches, including old secrets.
Protect plans and their containing directories. [Secrets](../../secrets.md)
owns lookup and redaction rules; [saved plans](../../files.md) owns plan/lock
permissions and atomic persistence.

## Removal and examples

An `absent` service removes declared configs without opening templates or secret
sources. The inventory definition still requires valid template declarations.
This template type removes only its declared file. A [directory config](paths.md)
explicitly requests recursive data deletion when absent. See [absent services](../lifecycle.md#absent-services).

Start with the [config-only quickstart](../../quickstart.md), the
[file example](../../../examples/files/README.md), or the
[isolated secrets example](../../../examples/secrets/README.md). The
[validated SSH example](../../../examples/validated-ssh/README.md) demonstrates
`sshd -t` before writing and reloading a disposable target's configuration.
