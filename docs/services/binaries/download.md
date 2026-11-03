---
title: "Downloaded files"
description: "Install checksum-pinned public HTTPS files with protected ownership and atomic replacement."
weight: 20
---

A `type: download` binary owns one regular file. It downloads on the target at
apply, verifies the required SHA-256, then publishes the complete file atomically.
It never executes the downloaded contents during preparation or installation.

```yaml
binaries:
  - name: executable
    type: download
    url: https://downloads.example.org/releases/v1.2.3/example-linux-amd64
    sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    path: /usr/local/bin/example
    owner: root
    group: root
    mode: "0755"
    timeout: 5m
```

The URL and digest above illustrate the fields. Use a published artifact and its
verified digest, as in the [pinned K3s example](../../../examples/k3s/README.md).

| Field | Required | Meaning and default |
| --- | --- | --- |
| `name` | no | Nonempty reaction label, unique in the binary list |
| `type` | yes | `download` |
| `url` | yes | Public, unauthenticated HTTPS URL |
| `sha256` | yes | Exactly 64 hexadecimal characters, the SHA-256 of the downloaded bytes |
| `path` | yes | Clean absolute non-root file destination; no control characters |
| `owner`, `group` | no | Existing target account/group names or numeric IDs; omitted values use the executing identity |
| `mode` | no | Regular-file permissions, default `0755` |
| `timeout` | no | Positive Go duration for the whole operation, default `5m`, maximum `24h` |

RPM-only fields (`manager`, `package`, `version`, `disable_excludes`) and
archive-only fields (`format`, `strip_components`) are rejected.

## Fetching, preparation, and saved plans

Only HTTPS is supported. User information, query strings, fragments, private
registry authentication, credentials, and custom request headers are unsupported.
Redirects are limited to five and every destination must remain credential-free
HTTPS without a fragment. Transient signed query parameters on redirect URLs
are allowed for public release hosting; they are not saved or displayed. The
inventory source URL itself cannot contain a query. TLS certificate verification
uses the target's trust configuration;
there is no insecure or certificate-verification bypass option. The target
needs Python 3.8 or newer with its standard library, HTTPS access, Linux
atomic rename support, GNU `readlink`, and util-linux `findmnt` for target path
ownership checks. The temporary Python helper uses isolated mode (`-I`),
so target working-directory files and `PYTHONPATH` cannot replace standard
library imports. No agent or daemon is installed. See
[installation requirements](../../installation.md).

Inventory inspection validates the declaration without fetching the URL or
contacting a host. Diff observes installed state and prepares a request without
downloading the artifact. JSON freezes the URL, digest, destination, attributes,
timeout and safety identities; it does not contain artifact bytes. Apply uses
that request after inventory changes or deletion. It still requires the target
to reach the URL and obtain bytes matching the saved checksum. A bad checksum,
TLS/network error, size limit or timeout fails before publication. Downloads are
limited to 1 GiB. There are no automatic retries.

## Ownership and updates

Richt refuses to adopt a pre-existing file, directory or symlink at `path`,
even if its bytes match the requested digest. Start with an unused destination.
It keeps a protected sibling directory named
`.richt-artifact-<first 24 hex characters of SHA256(clean absolute path)>`, mode
`0700`, holding a private manifest, a stable lock and recovery data. Reserve
this directory for Richt; deleting or modifying it breaks ownership evidence.
The parent path must be trustworthy: symlink parents and untrusted writable
parents are rejected. A root-owned sticky ancestor such as `/var/tmp` is allowed,
but the immediate parent must be protected. Necessary missing parents may be
created; create an explicitly managed directory config in an earlier service
when you need declared parent attributes.

The manifest records the destination file's device/inode identity, checksum and
attributes. Matching tracked state is a no-op. Changes to the checksum or attributes, and
edits to the tracked file's bytes or attributes, prepare repair. Changing only
the source URL to a mirror with the same checksum, artifact settings and
attributes is a no-op; the manifest retains the original source URL.
Replacing the destination file with a different inode is rejected, even if its
bytes match. Unknown metadata and foreign resources fail safely. Staging and checksum checks
finish before publication on the destination filesystem. The final file replaces
the managed entry atomically; an interrupted operation can be continued with its
saved plan and protected recovery state. Concurrent operations on the same
artifact fail promptly on its stable lock. This is per-artifact publication,
without rollback of earlier service tasks.

A named changed download participates in ordinary `restart_on`, `reload_on` and
`run_on` reactions through `binary.<name>`. An unchanged artifact triggers
nothing. Binaries run before configs and processes. If an artifact belongs on a
filesystem that a mount config will mount, apply that mount in a separate run
before preparing the artifact. See [lifecycle](../lifecycle.md).

An `absent` assignment stops managed processes and removes configs before
removing the owned download in reverse binary order. Removal verifies ownership
and refuses a replaced destination file. It removes the artifact and manifest,
retaining the protected sidecar and stable lock so later operations on this
path remain serialized. It does not remove unrelated parent directories or
application data. Removing an assignment alone removes nothing.
