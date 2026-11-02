# Validate SSH configuration before writing

This illustrative example manages `/etc/ssh/sshd_config` on a **disposable
AlmaLinux target**, runs `/usr/sbin/sshd -t -f {candidate}` before replacing its
contents, and reloads `sshd.service` only after the file task succeeds. The
template is deliberately minimal and replaces the full configuration. Adapt
its authentication and network settings to the test machine before applying.
Keep console access available and use a separate management channel for the
experiment.

Configure the placeholder `richt-validation-test` SSH alias with its address,
non-root login, key/agent, and trusted host key. The login needs noninteractive
`sudo -n`. The target needs DNF 4, GNU coreutils, systemd, an installed SSH
service with reload support, and existing host keys. The package declaration
keeps `openssh-server` installed; Richt does not check validator availability
during diff, so a preceding package installation can supply a checker for
other applications. See [SSH](../../docs/ssh.md) for connection setup.

From the repository root:

```sh
make build
bin/richt inventory validate -i examples/validated-ssh/inventory.yaml
ssh -o BatchMode=yes richt-validation-test 'sudo -n true'
bin/richt diff -i examples/validated-ssh/inventory.yaml -o /var/tmp/richt-validated-ssh.json
bin/richt apply /var/tmp/richt-validated-ssh.json
bin/richt diff -i examples/validated-ssh/inventory.yaml
```

Inventory validation does not contact the machine or open the template. Review
the displayed command, timeout, replacement contents, and reload before apply.
The final diff should show no changes. Change `sshd.log_level` from `INFO` to
`VERBOSE`, prepare a new plan, and apply it to exercise another validated reload.

To demonstrate rejection, set `sshd.log_level` to `INVALID`, prepare a new plan,
and apply it. `sshd -t` returns nonzero, so the old destination bytes,
permissions, and ownership remain intact and the reload stays blocked. Its
diagnostic output is discarded. The candidate uses mode 0600 in a private
target-side directory, uses `/` as its working directory, and is removed after
success, failure, or timeout.

A saved plan freezes its validator arguments, timeout, and candidate bytes.
Changing the inventory or deleting the template after diff does not change
that plan. An invalid saved candidate needs a new diff with corrected inputs.
For a temporary external checker failure, repair the target environment and
explicitly apply the same plan to retry validation and writing. Successful
tasks are skipped, including an already successful config task when only a
later reload needs continuation. There are no automatic retries or rollback.

This example has not been applied to a live SSH configuration as part of the
development tests. Automated tests use isolated temporary files and fake
package/systemd boundaries; Linux acceptance uses disposable application files.
