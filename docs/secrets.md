---
title: "YAML secrets and sensitive files"
description: "Resolve local secret sources during preparation and protect executable saved inputs."
weight: 80
---

Secret values are fetched during plan preparation on the machine running
`diff` or `apply -i`. Inventory inspection does not open secret sources, and
applying a saved plan never fetches them again. Local files, SSH/sudo files,
systemd units, and drop-ins use the same template and sensitivity rules.

## Sources and lookup

Declare independent named sources in inventory:

```yaml
secret_sources:
  default: {type: yaml, file: secrets/default.yaml}
  production: {type: yaml, file: secrets/production.yaml}
```

Paths are relative to the inventory file, regardless of the target transport.
Only local YAML sources are supported; environment variables and external
secret stores are outside the current implementation. Inventory commands show
declarations without checking whether the declared files exist or are valid.

A source contains one YAML mapping. Nested mappings provide dot-separated key
access. Mapping keys must be nonempty strings without dots. Leaf values must
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
bytes after inventory, templates, and source files change or disappear; completed
tasks are skipped as usual. Inspect human-readable plan output and protect the
JSON itself.

Plans, locks, and atomic plan replacements use mode 0600. Keep their containing
directory private and do not publish the JSON, logs, or temporary artifacts.
Target permissions follow the file contract: a new file defaults to mode 0644,
and existing permissions are preserved unless specified. Set `mode: "0600"`
explicitly for a private target. For SSH/sudo, prepared bytes travel through
stdin instead of command arguments; sources remain on the controller.

When a sensitive file has an explicit `mode`, its write first restricts the
target to owner-only read/write access (0600), before writing content. The
following attribute operation applies the requested final permissions and
ownership. A failed write can leave the target at 0600 until explicit
continuation. This applies to new and existing files, locally and over SSH/sudo,
without replacing the inode. Without an explicit mode, the usual default and
preserved-permission rules still apply.

Local RPM/systemd and SSH/sudo command failures retain safe failure categories
and exit status, but omit command output that could contain secret-bearing
application diagnostics. Temporary plan files are cleaned up on completion or
handled failures; forced termination can leave a private temporary file behind.

The [two-source example](../examples/secrets/README.md) verifies lookup,
redaction, private files, and frozen saved inputs without printing their
contents. On 2026-11-01, the expanded suites passed 17 local file, 33
RPM/systemd, and 31 SSH/sudo checks on AlmaLinux 10.2 x86_64 with DNF 4.20 and
systemd 257. Local file checks ran with SELinux Enforcing; two isolated system
containers supplied the SSH targets with non-root logins and noninteractive
sudo. These runs verified real secret-change reloads, frozen multiline bytes,
private file modes, and continuation after source deletion. Containers share
the host kernel; SELinux labels were verified on the enforcing host itself.
