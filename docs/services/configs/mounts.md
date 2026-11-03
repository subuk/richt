---
title: "Mounts and fstab"
description: "Manage an active XFS mount and its persistent fstab entry together."
weight: 40
---

```yaml
configs:
  - type: mount
    source: /dev/vg_data/data
    target: /srv/data
    filesystem: xfs
    options: [defaults, noatime]
```

A mount config requires an absolute block-device `source`, absolute non-root
`target`, and `filesystem: xfs`. Optional `options` is a list of nonempty mount
options and defaults to `[defaults]`. Optional `name` is a process reaction
label. Bind mounts, network filesystems, and other filesystem types are outside
the initial subset.

Present means an active mount of the intended source with the declared
filesystem and requested options, plus a mandatory `/etc/fstab` entry. The mount
operation creates necessary parent and mount-point directories. It never
formats its source: use an earlier array/LV config with `filesystem: xfs` or
provide an existing compatible filesystem. See [storage configs](storage.md).
Existing incompatible source or filesystem declarations fail.

Fstab entries use the filesystem UUID so persistence survives device aliases
and name renumbering. Preparation freezes exact before/after edits and composes
multiple mount entries in declaration order. Unrelated entries and comments
are preserved. Compatible duplicate entries collapse to one; conflicts fail.
A template or plain file claiming all of `/etc/fstab` conflicts with mount
entry management. Applying a saved plan checks the prepared fstab precondition
instead of replacing unrelated intervening edits silently.

Each mount task includes its atomic fstab update, required systemd
mount-definition reload, mount operation, and source/filesystem/options
verification. Failure blocks later configs and processes on that host. Kernel
mount options are checked, including the effective read-only/read-write state
represented by `defaults`. Prepared device identities and filesystem UUIDs are
checked again during apply.

Declare a new filesystem before its mount, then files and directories inside
that mount. Preparation uses a freshly prepared filesystem's known empty state
and does not inspect the hidden mount-point directory as filesystem contents.
For existing unmounted filesystems, mount them in a separate apply before
preparing downstream configs that require observing their contents. No component
dependency graph or automatic prerequisite selection is introduced.

Absent unmounts the declared target and removes its fstab entry. A busy mount
fails; there is no force or lazy unmount. The backing array, LV, filesystem, and
data remain unless their own resource configs request deletion. Reverse config
order unmounts before earlier-declared storage deletion. Reboot persistence
requires the distribution's normal storage activation and availability of the
filesystem UUID at boot; required custom MD/initramfs configuration belongs to
ordinary template/process components.

For optional or disposable storage, include `nofail` in `options` so an
unavailable device does not block boot. A bounded
`x-systemd.device-timeout=90s` can limit device discovery waits. Richt still
requires a successful active mount during apply. `noauto` instead disables
automatic boot mounting and is unsuitable when testing fstab boot persistence.
