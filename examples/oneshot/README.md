# Apply a sysctl change with a oneshot

This example writes one sysctl config, then uses Richt's native `oneshot`
process to apply it. It does not create a systemd unit or install a package.
Run it as root only on an authorized disposable Linux target with
`/usr/sbin/sysctl` and a writable `vm.swappiness` parameter. Execute from the
repository root with a built or transferred Linux Richt binary.

Keep the original value in the same shell for cleanup. First check that the
example's config is absent; do not overwrite a preexisting file:

```sh
test ! -e /etc/sysctl.d/90-richt-oneshot-example.conf
original_swappiness=$(/usr/sbin/sysctl -n vm.swappiness)
work=$(mktemp -d /var/tmp/richt-oneshot-example.XXXXXX)
chmod 0700 "$work"
cp examples/oneshot/inventory.yaml "$work/inventory.yaml"

bin/richt inventory validate -i "$work/inventory.yaml"
bin/richt inventory show -i "$work/inventory.yaml"
bin/richt diff -i "$work/inventory.yaml" -o "$work/plan.json"
# Review the config write, direct argv, timeout, and config.tuning trigger:
bin/richt apply "$work/plan.json"
test "$(/usr/sbin/sysctl -n vm.swappiness)" = 10
bin/richt diff -i "$work/inventory.yaml"
```

The final diff reports no changes and schedules no command. Change the inline
value to `20`, prepare another saved plan, and apply it to see a content-triggered
execution. A linked config's mode/ownership/SELinux change also triggers the
command; changing an unrelated config does not. Several changed `run_on` refs
still schedule one command.

Inventory inspection and diff never execute `sysctl`. Apply runs the saved
argv directly, with no implicit shell, and exit status zero marks success.
The plan freezes the command even if inventory later changes or disappears.
Only target executables and supporting files are used at execution time.
Stdout and stderr are omitted from command diagnostics; inspect runtime values
with `sysctl -n` separately when needed.

If the write succeeds but the command fails, fix its external cause and apply
that same JSON plan again. Completed tasks are skipped and the pending command
is retried. A fresh diff may be empty because the config is already correct;
it does not recover that failed execution. Interrupted command effects may
repeat during continuation. See the [oneshot reference](../../docs/services/processes/oneshot.md)
and [saved-plan rules](../../docs/files.md).

## Remove and restore

Change only the tag assignment to `sysctl: {state: absent}` in the private
inventory copy. Then prepare and review removal:

```sh
bin/richt diff -i "$work/inventory.yaml" -o "$work/remove.json"
bin/richt apply "$work/remove.json"
test ! -e /etc/sysctl.d/90-richt-oneshot-example.conf
/usr/sbin/sysctl -w "vm.swappiness=$original_swappiness"
rm -rf -- "$work"
```

Absence removes the managed file and runs no oneshot. It does not restore the
kernel setting, so the explicit final `sysctl -w` is required, including after
a partially failed run. Removing the assignment alone would leave the file.
For non-root execution, configure explicit `sudo: true`; for SSH, replace the
local transport with an authorized target and follow the [SSH guide](../../docs/ssh.md).

The focused [acceptance driver](../../tests/integration/oneshot.py) automates
trigger, ordering, timeout, failure, continuation, and sysctl checks and restores
the sysctl value in a `finally` block. It uses unique fixtures and accepts a
non-root SSH destination with passwordless `sudo -n`.
