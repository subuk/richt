---
title: "Secrets and sensitive files"
description: "Resolve local secret sources during preparation and protect executable saved inputs."
weight: 80
---

Local inventories declare YAML and environment secret sources directly. For
remote inventory and service documents, the operator provides named local
bindings through an explicit `--source-config` file. Remote documents cannot
declare secret sources or select controller environment variables. The same
file can configure HTTPS authentication, whose credentials are separate from
template secret values. Client bindings also work with local
inventories; duplicate source names are rejected. See [HTTPS sources](sources.md#explicit-client-settings).

Secret values are fetched during plan preparation on the machine running
`diff` or `apply -i`. Inventory inspection does not open secret sources, and
applying a saved plan never fetches them again. Local files, SSH/sudo files,
systemd units, and drop-ins use the same template and sensitivity rules. See
[file configs](services/configs/files.md) for templates and attributes,
[reactions](services/reactions.md) for change-triggered process behavior, and
[saved plans and apply](files.md) for progress and locking.

## Sources and lookup

Declare independent named sources in inventory:

```yaml
secret_sources:
  default: {type: yaml, file: secrets/default.yaml}
  production: {type: yaml, file: secrets/production.yaml}
```

Paths are relative to the inventory file, regardless of the target transport.
YAML files and explicitly mapped environment variables are supported; external
secret stores remain outside the implementation. Inventory commands show
declarations without reading files or environment values.

A YAML source contains one YAML mapping. Nested mappings provide dot-separated
key access. Mapping keys must be nonempty strings without dots. Leaf values must
be strings, including empty strings and multiline strings; quote numeric,
boolean, or date-like values when they should be strings. Empty mappings are
accepted but do not resolve to string values. For example, these values are
fictional:

```yaml
database:
  password: "fictional-example-password"
  numeric-token: "123"
  optional: ""
message: |
  fictional first line
  fictional second line
```

Lists, numbers, booleans, dates, nulls, duplicate keys, aliases, YAML merges,
custom tags, malformed YAML, and multiple documents are rejected. The entire
selected source is validated on first use, including keys not requested by the
template. Sources not used by the selected preparation remain unopened.

## Environment sources

Map full secret keys to controller environment variable names:

```yaml
secret_sources:
  default:
    type: env
    env:
      database.password: APP_DB_PASSWORD
      api.token: APP_API_TOKEN
  production: {type: yaml, file: secrets/production.yaml}
```

The `env` mapping is flat: `database.password` is the complete lookup key, not
an instruction to traverse nested YAML. Keys follow the same nonempty
dot-separated segment rules as `secret` lookup. Variable names must match
`[A-Za-z_][A-Za-z0-9_]*`. Both keys and variable names must be YAML strings;
there is no prefix discovery, interpolation, or implicit environment lookup.
An empty mapping is valid but supplies no keys. `type: env` requires `env`
and forbids `file`; `type: yaml` requires `file` and forbids `env`, including
explicitly empty or null incompatible fields.

At the first lookup, Richt reads all variables declared in that source from
its own process environment and caches the values for that preparation. A
missing variable makes the entire source fail, even if the template requested
a different key. A present empty string is valid. Spaces, multiline values,
and trailing line endings are preserved exactly; values are not parsed as YAML
or trimmed. Values must be valid UTF-8 text, so saved JSON retains their bytes;
invalid UTF-8 fails preparation instead of silently replacing bytes. Failed loads are cached too. Unused sources are never read; a new
diff gets fresh values. Concurrent lookups share the same source cache.

For SSH targets, these are still controller values, not target environment
variables. With local `sudo: true`, they come from the Richt process before
its target commands invoke sudo. If Richt itself is started through sudo,
its environment is whatever sudo supplies to that process.

The same declarations work in an explicit local `--source-config` file, also
for HTTPS inventories. Remote documents cannot declare `secret_sources`;
only local operator bindings select controller variables. HTTP authentication
credentials have their own rules and are separate from template secrets.

## Template lookup

```gotemplate
password={{ secret "database.password" }}
production={{ secret "database.password" "production" }}
quoted={{ printf "%q" (secret "database.password" "production") }}
```

`secret` accepts one string key and an optional string source name. An omitted
source selects the name `default`; an explicit source selects only that source.
There is no merging, search, or fallback. Identical keys in different sources
are independent. A missing selected source, invalid key, missing leaf key,
unreadable source, or invalid source fails preparation. `default` need not be
declared when every call selects another existing source explicitly. Lookup
keys cannot contain empty segments, so `.password`, `database.`, and `a..b`
are invalid.

Each selected source is loaded once per diff. Its values or load failure remain
cached for that preparation across files and hosts; a new diff loads afresh.
An absent service does not render templates or read secret sources.

## Templates and diagnostics

Templates retain the standard Go `text/template` built-ins and control flow.
For example, `printf` can format a secret, `index` and `slice` can select part of
a string, and `html`, `js`, or `urlquery` can escape it. No command execution,
environment lookup, or external fetching function is added.

Any executed `secret` call makes the entire file sensitive, even when its value
is transformed, assigned to a template variable, or omitted from output.
`sensitive: false` cannot disable automatic marking. Parse and execution errors
are generic for all templates; Richt does not print source snippets, function
arguments, source names, keys, secret values, or underlying YAML decoder
details from secret lookup failures. This also protects failures raised by
standard built-ins after a secret has been transformed.

## Display, saved inputs, and permissions

A sensitive file display shows its path and fact of change while hiding both
old and new content and all patch context. File operation diagnostics are
generic. Comparison still uses real bytes, so changed secrets trigger configured
process reactions. Ordinary config, owned-unit, and drop-in rules all apply.

Use explicit `sensitive: true` when replacing old secret content with a template
that no longer calls `secret`, or when removing a secret-bearing file with
`state: absent`. There is no persistent secret history. Removal skips template
evaluation, so a secret call in the unused template cannot automatically mark
the removal sensitive.

Saved JSON is executable data and contains real rendered contents and patches,
including old secret bytes. Sensitivity is persisted, but display placeholders
never replace executable inputs. Apply and explicit continuation use the saved
bytes after inventory, templates, source files, and environment values change
or disappear; completed tasks are skipped as usual. Inspect human-readable plan output and protect the
JSON itself.

Plans, locks, and atomic plan replacements use mode 0600. Keep their containing
directory private and do not publish the JSON, logs, or temporary artifacts.
Target permissions follow the file contract: a new file defaults to mode 0644,
and existing regular-file permissions are preserved unless specified. Replacing
a final symlink uses new regular-file defaults; the link metadata belongs to
its optional backup. Set `mode: "0600"`
explicitly for a private target. For SSH/sudo, prepared bytes travel through
stdin instead of command arguments; sources remain on the controller.

Atomic template writes stage bytes privately and apply intended metadata before
publication. Explicit `atomic_writes: false` selects weaker in-place regular-file
writes; a sensitive write with an explicit `mode` first restricts the inode to
0600, and failure may leave that restrictive mode until manual recovery or
continuation. Backups preserve the prior object's permissions and metadata,
including for symlinks, rather than forcing 0600. They may retain old secrets;
protect the target backup and its private receipt as well as the saved plan.
See [file writes and backups](services/configs/files.md).

Optional config validators stage candidate bytes in a private target directory
(mode 0700), with a mode 0600 candidate file. Validator stdout and stderr are
discarded, including for files not marked sensitive. Sensitive plan displays
also hide validator arguments. Normal success, failure, and validator timeout
clean up candidates; abrupt process or host termination can leave a private
temporary directory behind. See [file validation](services/configs/files.md).

Local RPM/systemd/oneshot and SSH/sudo command failures retain safe failure categories
and exit status, but omit command output that could contain secret-bearing
application diagnostics. Temporary plan files are cleaned up on completion or
handled failures; forced termination can leave a private temporary file behind.
Oneshot argv is visible in the plan and its display; command output suppression
does not make arguments sensitive. Keep secret values out of argv.

The [two-source example](../examples/secrets/README.md) verifies lookup,
redaction, private files, and frozen saved inputs without printing their
contents. On 2026-11-01, the expanded suites passed 17 local file, 33
RPM/systemd, and 31 SSH/sudo checks on AlmaLinux 10.2 x86_64 with DNF 4.20 and
systemd 257. Local file checks ran with SELinux Enforcing; two isolated system
containers supplied the SSH targets with non-root logins and noninteractive
sudo. These runs verified real secret-change reloads, frozen multiline bytes,
private file modes, and continuation after source deletion. Containers share
the host kernel; SELinux labels were verified on the enforcing host itself.
