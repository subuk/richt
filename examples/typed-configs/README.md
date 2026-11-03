# Typed config paths

This local config-only service creates a directory, a plain marker file, and a
rendered config. Use a disposable `/var/tmp/richt-typed-configs` directory.

```sh
make build
bin/richt inventory validate -i examples/typed-configs/inventory.yaml
bin/richt diff -i examples/typed-configs/inventory.yaml -o /var/tmp/richt-paths-plan.json
bin/richt apply /var/tmp/richt-paths-plan.json
printf 'unmanaged contents\n' > /var/tmp/richt-typed-configs/marker
bin/richt diff -i examples/typed-configs/inventory.yaml
```

The last diff is empty: the plain file preserves its contents and directory
children do not produce changes. The template's contents remain managed.
Set the `markers` assignment to `{state: absent}`, prepare a new plan, and apply
it to remove the settings, marker, and entire directory in reverse config order.
Directory absence deletes all children, including undeclared data.

For the Linux RAID1 → VG → LV → XFS → mount syntax and its destructive removal
scope, see [storage configs](../../docs/services/configs/storage.md) and
[mounts](../../docs/services/configs/mounts.md). The storage acceptance harness
uses disposable loop devices.

On an authorized disposable Linux host with mdadm, LVM2, xfsprogs and systemd,
run the complete stack example as root:

```sh
python3 tests/integration/typed_configs.py --binary ./bin/richt-linux-amd64 --work-root /var/tmp/richt-typed-results
```

It allocates private loop backing files, retains the generated inventories,
prepared plans and verification logs, and cleans its own storage resources.
The separate boot harness also reboots its target; see the
[validation report](../../docs/validation.md#typed-configs--2026-11-03).
