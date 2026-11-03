---
title: "Plain files and directories"
description: "Manage existence and attributes while preserving present contents."
weight: 20
---

```yaml
configs:
  - type: directory
    path: /var/lib/example
    owner: example
    group: example
    mode: "0750"
  - name: marker
    type: file
    path: /var/lib/example/marker
    mode: "0640"
```

`file` ensures a regular file exists. It creates an empty file only when missing
and never truncates existing contents, including a file that appeared after diff
or during continuation. `directory` ensures a directory exists and preserves
its children. Requested attributes affect only the named resource, never its
descendants. Existing incompatible path types fail.

Both types accept optional `name`, required absolute `path`, and optional
`owner`, `group`, and quoted octal `mode`. Owner/group names must exist on the
target and are resolved there during preparation and again at apply. Omitted
attributes preserve existing values. New files default to 0644; new directories
default to 0755. Necessary parent directories are created with mode 0755,
subject to the execution user's umask; existing parents are left alone.
Template sources, validators, sensitivity, SELinux, and storage fields are not
accepted by these types.

Present operations follow named and parent symlinks. Absent removes the named
entry: a symlink is unlinked without removing its target. An absent `file`
removes the regular file and its data. An absent `directory` recursively removes
all children, including undeclared data, and refuses to cross an active mount
or filesystem device boundary. Unmount using an earlier absent mount operation
before recursive deletion. See [lifecycle](../lifecycle.md#absent-services).

Plans freeze requested attributes and the device/inode identity of observed
entries. Apply checks the identity before modifying an existing entry; explicit
continuation tolerates an already completed deletion but refuses a replacement
at the same path. File contents and directory children are unmanaged during
presence and produce no spurious diffs or [reactions](../reactions.md).

The [plain-path example](../../../examples/typed-configs/README.md) is runnable
locally without block devices.
