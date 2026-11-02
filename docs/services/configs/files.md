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
    validate:
      args: [/usr/sbin/example-check, --config, "{candidate}"]
      timeout: 5s
    template:
      inline: "listen = {{ .app.listen }}\n"
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `name` | no | Nonempty reference label, unique within the config list |
| `type` | no | `template` (default); no other config backend is supported |
| `path` | yes | Absolute target file path, normalized during resolution |
| `template` | yes | Exactly one of `file` or `inline`; an empty inline string is valid |
| `owner` | no | Target user name; omission preserves existing ownership |
| `group` | no | Target group name; omission preserves existing ownership |
| `mode` | no | Quoted octal string such as `"0644"`; omission preserves existing permissions; new files default to 0644 |
| `selinux` | no | Full SELinux label; omission leaves the label unmanaged |
| `sensitive` | no | Defaults to false; template secret lookup automatically makes it true |
| `validate` | no | Target-side validator argv and positive timeout; timeout defaults to `5s` |

The file fields also describe [systemd-owned units and
drop-ins](../processes/systemd.md#owned-units-and-drop-ins), whose paths can be
derived from the process. Ordinary configs require `path`. Config list items
accept `type: template`; omission has the same meaning. Configs have no separate
per-file target state; they use the service assignment's `present` or `absent`
state. `unit_file` remains one object, and `overrides` remains a mapping of
drop-in basenames to file objects. Owned file objects also accept optional
`type: template`. Only config-list entries provide labels for reaction references.

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

Exit status zero permits the existing write and attribute operations to
continue. A nonzero exit, unavailable executable, or timeout fails the file
task before the destination is opened or changed, and blocks later host tasks,
including reloads and restarts. Validator stdout and stderr are always discarded
because even ordinary application diagnostics can reveal file contents. A
positive Go duration bounds the command; target commands use GNU coreutils
`timeout`. Candidate files and directories are removed on success, failure, and
timeout. Abrupt host termination, process termination, or a broken SSH session
can prevent cleanup; remaining candidates retain their private permissions.

Validation runs only when the prepared task writes content. It is skipped for
unchanged content, attribute-only changes, and removals. Validation and writing
share one persisted file task: explicit continuation repeats validation for an
unfinished write and skips an already successful file task. Richt always writes
the original prepared bytes, even if the validator modifies its candidate. An
invalid saved candidate requires a newly prepared plan; editing its source
inventory does not repair frozen saved inputs.

## Apply and attributes

Apply uses the prepared contents and attributes without reopening templates,
reading secrets, or comparing current bytes with the old diff. It creates
necessary parent directories. Existing unspecified ownership and permissions
are preserved; new files default to mode 0644 and the execution user's normal
ownership. Explicit owner/group names must already exist on the target. They
are resolved during observation and again at apply, so numeric IDs may change
between preparation and execution.

Ordinary file access follows target and parent symlinks. Removal unlinks the
named file or symlink. There is no special inode-identity or symlink-protection
contract. Saved plans preserve rendered text, patches, and requested names; see
[saved plans and apply](../../files.md) for persistence and continuation.

A `selinux` request manages the full label, compares it during diff, and applies
it through the open target file descriptor. This requires SELinux-enabled Linux
and a label accepted by the kernel; requests fail explicitly on other systems.

## Sensitive files

A `secret` call or explicit `sensitive: true` hides the entire old/new diff,
including context, and protects file error details. Explicit false cannot cancel
automatic sensitivity. Real bytes still determine changes and reactions. Set
`sensitive: true` to protect old content when a new template no longer calls
`secret` or when removing a service; Richt retains no persistent secret history.

Sensitivity controls display, not final target permissions. Set `mode: "0600"`
for private target files. Sensitive writes with an explicit mode first restrict
the inode to 0600, write content, then apply the requested final attributes.
Without an explicit mode, normal existing/new-file permissions remain.

Saved JSON contains real rendered content and patches, including old secrets.
Protect plans and their containing directories. [Secrets](../../secrets.md)
owns lookup and redaction rules; [saved plans](../../files.md) owns plan/lock
permissions and atomic persistence.

## Removal and examples

An `absent` service removes declared configs without opening templates or secret
sources. The inventory definition still requires valid template declarations.
Only explicitly declared files are removed; application data and unrelated
files remain. See [absent services](../lifecycle.md#absent-services).

Start with the [config-only quickstart](../../quickstart.md), the
[file example](../../../examples/files/README.md), or the
[isolated secrets example](../../../examples/secrets/README.md). The
[validated SSH example](../../../examples/validated-ssh/README.md) demonstrates
`sshd -t` before writing and reloading a disposable target's configuration.
