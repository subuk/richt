# Deliberate failure and manual continuation

This config-only example requires a built `bin/richt`, a POSIX shell, and a
writable `/var/tmp`. It needs no RPM, systemd, SSH, root, or external service.
Run commands from the repository root. Inventory validation is safe by itself:

```sh
bin/richt inventory validate -i examples/continuation/inventory.yaml
```

Allocate a fresh workspace and substitute its path into a copy of the inventory:

```sh
work=$(mktemp -d /var/tmp/richt-continuation.XXXXXX)
sed "s|/var/tmp/richt-continuation-example|$work/target|g" \
  examples/continuation/inventory.yaml > "$work/inventory.yaml"
bin/richt diff -i "$work/inventory.yaml" -o "$work/plan.json"
```

Diff prepares all three files while the target directory is absent. Create a
regular file at the second file's future parent path **after** preparation:

```sh
mkdir -p "$work/target"
printf 'deliberate obstruction\n' > "$work/target/blocked"
if bin/richt apply -y "$work/plan.json"; then
  echo 'Unexpected success: inspect the workspace before continuing.'
else
  echo 'Expected failure: the second target has a regular file as its parent.'
fi
cat "$work/target/first.conf"
test ! -e "$work/target/third.conf"
```

The first service succeeds; the second fails; the third is blocked on that
host. The saved JSON contains this progress. Remove only the deliberate
obstruction and explicitly apply the **same plan**:

```sh
rm "$work/target/blocked"
bin/richt apply -y "$work/plan.json"
cat "$work/target/first.conf" "$work/target/blocked/second.conf" \
  "$work/target/third.conf"
bin/richt diff -i "$work/inventory.yaml"
```

Continuation reports previously completed tasks and skips them, retries the
unfinished second service, and then runs the third. The final diff has no
changes. It does not rerender templates or recalculate the saved plan. A task
interrupted after taking effect may run again; exactly-once execution is not
guaranteed. There is no automatic retry or rollback.

To repeat, allocate another workspace. Once inspected, remove this run's
allocated directory and its plan/lock together:

```sh
rm -r "$work"
```

Validation status: this walkthrough passed on AlmaLinux 10.2 x86_64 on
2026-11-01, including deletion of the inventory before explicit continuation.
See [the acceptance report](../../docs/validation.md) for environment and scope.
