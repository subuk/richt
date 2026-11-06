# Local config-only example

Build with `make build` and run from the repository root. This example manages
only `/var/tmp/richt-example/greeting.conf`; it needs no root, RPM, systemd or
network access when that directory is writable by the current user.

```sh
bin/richt inventory validate -i examples/files/inventory.yaml
bin/richt diff -i examples/files/inventory.yaml -o /var/tmp/richt-files-plan.json
bin/richt apply -y /var/tmp/richt-files-plan.json
cat /var/tmp/richt-example/greeting.conf
bin/richt diff -i examples/files/inventory.yaml
```

Diff renders and observes without modifying managed resources; saving a plan
writes only that plan. The final diff should have no changes. Change `greeting`
in the inventory to inspect an update. Apply without `-y` for explicit terminal
confirmation; piped input does not supply terminal agreement.

Set `tags.example.services.greeting.state: absent`, prepare a new plan and apply it
to remove the managed file. Removing the assignment alone requests no deletion.
Restore `state: present` afterwards if reusing the checked-in example.
The containing directory, plan and lock are retained. Use
[the continuation walkthrough](../continuation/README.md) for a repeatable
failure that demonstrates saved progress and manual resume.

See [saved plans and apply](../../docs/files.md) for frozen inputs, confirmation,
locking, attributes and sensitive output, and the repository acceptance report
for executed validation results.

Template writes default to `atomic_writes: true` and `backup: false`. Add
`backup: true` to a config to keep the prior object before replacement or
removal; diff displays its frozen adjacent backup path. Saved plans use format
`1.7`; prepare a new plan instead of applying one from an older release.
See [file writes and backups](../../docs/services/configs/files.md).
