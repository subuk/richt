---
title: "RAID and logical volumes"
description: "Declare RAID1 arrays, LVM groups and linear volumes with optional XFS."
weight: 30
---

```yaml
configs:
  - type: mdadm
    array: /dev/md/data
    level: raid1
    metadata: "1.2"
    members: [/dev/disk/by-id/example-a, /dev/disk/by-id/example-b]
  - type: lvm-vg
    vg: vg_data
    devices: [/dev/md/data]
  - type: lvm-lv
    vg: vg_data
    lv: data
    size: 200GiB
    filesystem: xfs
```

Resource identities are independent of optional component `name` labels. Inputs
are literal device paths and VG/LV names. Declare producer configs before their
consumers; preparation accounts for resources created by earlier entries.
It never creates or activates resources during diff.

`mdadm` requires an absolute MD `array` path, `level: raid1`, `metadata: "1.2"`,
and at least two distinct `members`. Existing arrays require the exact declared
member set and compatible healthy RAID1 layout. Degraded adoption, reshaping,
and member replacement are unsupported.

`lvm-vg` requires `vg` and nonempty distinct backing `devices`. PV initialization
belongs to this operation. `lvm-lv` requires `vg`, `lv`, and a positive integral
`size`, such as `200GiB`. Supported units are `B`, `KiB` through `EiB`, and binary
`K`, `M`, `G`, `T`, `P`, `E`. Sizes round to the VG's extent size (4 MiB for newly
created groups). Only ordinary linear volumes are supported. Existing layout
or size conflicts fail; Richt does not resize resources.

Arrays and LVs accept optional `filesystem: xfs`. It requests creation when the
device is safely eligible and verification when a filesystem exists. Conflicting
signatures fail; no implicit reformat occurs. Omission leaves filesystem state
unmanaged. A VG cannot request a filesystem. A [mount](mounts.md) consumes a
filesystem without formatting it.

The Linux target needs mdadm, LVM utilities, XFS tools when requested, and the
standard utilities used for identity/signature/consumer checks. Inspection tools
must already be installed before diff; binary components can maintain their
package state but cannot supply tools needed by preparation. Local and SSH
operations honor the explicit sudo setting. Present compatible resources are
adopted; inactive arrays/LVs without filesystem management may be assembled or
activated. Activate an existing filesystem-bearing inactive resource before
diff so its filesystem can be observed safely.

Plans retain fixed creation identities, target identities, member/PV scope,
filesystem UUIDs, and precondition/result checks. Interrupted creation recognizes
its own results without reformatting them. Substituted devices, incompatible
changed state, busy mounts, and unrelated consumers fail before destruction.
Initializing or deleting raw MD members/PVs requires a target WWN, serial, or
loop backing-file identity (filename and filesystem device/inode). Devices
without one of these identities fail; major/minor numbers and size alone are
insufficient. Existing MD/LVM resources additionally use their stable UUIDs.
Explicit absence removes data even from adopted resources: remove LVs first,
then their empty VG and managed PV metadata, then stop the MD array and remove
its member RAID metadata. An undeclared remaining LV blocks VG deletion.

Boot activation uses the distribution's normal MD udev assembly and LVM
activation policy. Distribution-specific mdadm configuration and initramfs
updates can be declared through ordinary template/process components. Loop
devices are temporary kernel mappings: a test fixture must reattach their
backing files after boot. On the validated platform, MD assembly ignores bare
device change events, so the loop fixture also emits device-add events after
attachment. This fixture behavior is not a production disk discovery policy.
Partition
tables, secure erasure, thin pools, snapshots, reshaping, resizing, migration,
and cross-service dependency scheduling are outside the supported subset.
