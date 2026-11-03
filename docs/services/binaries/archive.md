---
title: "Archive trees"
description: "Install checksum-pinned tar.gz archives as exclusively owned directory trees."
weight: 30
---

A `type: archive` binary owns its entire destination tree. Richt downloads and
verifies the archive at apply, extracts it safely into staging, and publishes
the complete tree atomically on Linux.

```yaml
binaries:
  - name: application
    type: archive
    url: https://downloads.example.org/releases/v1.2.3/example-linux-amd64.tar.gz
    sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    path: /opt/example
    format: tar.gz
    strip_components: 1
    owner: root
    group: root
    timeout: 5m
```

The URL and checksum illustrate the schema. The
[node_exporter walkthrough](../../../examples/node-exporter/README.md) supplies
a pinned published archive and verified checksum.

| Field | Required | Meaning and default |
| --- | --- | --- |
| `name` | no | Nonempty reaction label, unique in the binary list |
| `type` | yes | `archive` |
| `url`, `sha256` | yes | Public HTTPS URL and exactly 64 hexadecimal SHA-256 characters |
| `path` | yes | Clean absolute non-root destination directory, exclusively owned by this binary |
| `format` | yes | `tar.gz`; no format detection or other archive formats |
| `strip_components` | no | Number of leading member path components to discard, integer `0` through `32`, default `0` |
| `owner`, `group` | no | Existing target account/group names or numeric IDs, applied to the whole tree; omitted values use the executing identity |
| `timeout` | no | Whole-operation positive duration, default `5m`, maximum `24h` |

`mode` is forbidden. Directories become `0755`; regular files become `0755` if
any archive execute bit was set, otherwise `0644`. Archive owner/group IDs,
setuid/setgid/sticky bits, and other special permissions are not trusted. The
requested target owner/group is applied across the tree. Required accounts must
exist before diff and apply; Richt has no user-creation resource.

## Extraction safety and limits

Only ordinary files, directories and safe relative symlinks are accepted.
Absolute paths, `..` traversal, absolute or escaping symlink targets, hardlinks,
devices, FIFOs, sockets, duplicate destinations and writes through symlink
parents are rejected. Symlinks must remain within the published tree after
stripping components. Directory members whose entire path is stripped are
skipped; stripping the entire path of a file or symlink is rejected.
Richt extracts entries explicitly rather than invoking an unrestricted tar
extract operation. A checksum match is required before extraction.

Limits are 1 GiB downloaded/compressed bytes and 4 GiB for the complete
decompressed tar stream, including metadata and padding. Archives may contain
at most 100,000 members and 100,000 output entries, including implicit parent
directories. Member paths and symlink targets are limited to 4,096 UTF-8 bytes
and 128 components. Symlink resolution allows at most 128 links and rejects
cycles. Extended tar metadata is limited to 16 MiB in total, at most
32 nested headers, 1 MiB per PAX header, and 4,096 bytes per GNU long-name header.
The protected manifest is limited to 32 MiB and the publication journal to
64 MiB; these sizes are checked before publication. Invalid, oversized or unsafe
archives fail without publishing a partial tree. Unpublished staging is cleaned
after a metadata-size failure, allowing the saved plan to continue after its
cause is resolved. Network, TLS and redirect rules are identical to
[downloads](download.md#fetching-preparation-and-saved-plans).

## Owned trees, drift, and deletion

Start with a missing destination. Existing directories, even empty ones, are
not adopted. The protected sibling manifest and stable lock follow the
[download ownership rules](download.md#ownership-and-updates), with checksums,
entry types, ownership, modes and link targets recorded throughout the tree.
Matching state needs no task. Changing the pinned artifact can upgrade or
revert its installed bytes; application data formats and compatibility remain
the application's responsibility. A source-URL-only mirror change with the same
checksum, extraction settings and attributes is a no-op; the manifest retains
the original source URL.

The manifest binds the destination directory's device/inode identity. Replacing
that root directory is rejected. Inside the owned tree, edits or replacement of
a tracked entry with the same entry type are owned drift: Richt can repair
them, and removal can delete them. An unknown entry or a changed entry type
blocks update and deletion. Store mutable data, logs and local customization outside
the archive tree. An ordinary config, another artifact, a unit/drop-in or a
mount cannot claim a path inside this tree, and archive trees cannot overlap.
The protected metadata path is also reserved. These ownership conflicts are
validated before service filtering.

Staging finishes before an atomic whole-tree exchange; readers never see a
partly extracted installation. Supported Linux/filesystem atomic rename
operations are required; there is no non-atomic fallback. Protected transaction
metadata allows explicit continuation after interruption. Earlier successful
saved tasks stay skipped. Archive bytes are absent from saved JSON; apply needs
the frozen URL to serve bytes matching the frozen digest. See
[saved plans](../../files.md).

An actual archive change triggers linked processes through `binary.<name>`.
An `absent` assignment stops/disables managed processes, removes configs, then
removes binaries in reverse order, verifying ownership and rejecting foreign
entries before deleting the owned tree. It preserves unrelated parent paths
and data outside the tree. Apply a required newly mounted filesystem separately
before preparing an artifact underneath it.
