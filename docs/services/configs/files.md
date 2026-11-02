---
title: "Files and templates"
description: "Prepare file contents, permissions, ownership, SELinux labels, and sensitive output."
weight: 10
---

```yaml
configs:
  main:
    path: /etc/example-app.conf
    owner: root
    group: root
    mode: "0644"
    selinux: system_u:object_r:etc_t:s0
    sensitive: false
    template:
      inline: "listen = {{ .app.listen }}\n"
```

| Field | Required | Meaning and default |
| --- | --- | --- |
| `path` | yes | Absolute target file path, normalized during resolution |
| `template` | yes | Exactly one of `file` or `inline`; an empty inline string is valid |
| `owner` | no | Target user name; omission preserves existing ownership |
| `group` | no | Target group name; omission preserves existing ownership |
| `mode` | no | Quoted octal string such as `"0644"`; omission preserves existing permissions; new files default to 0644 |
| `selinux` | no | Full SELinux label; omission leaves the label unmanaged |
| `sensitive` | no | Defaults to false; template secret lookup automatically makes it true |

These fields also describe [systemd-owned units and
drop-ins](../processes/systemd.md#owned-units-and-drop-ins), whose paths can be
derived from the process. Ordinary configs require `path`. Configs have no
`type` field or separate per-file target state; they use the service assignment's
`present` or `absent` state.

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
Application-specific config validation before writing is not implemented.

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
[isolated secrets example](../../../examples/secrets/README.md).
