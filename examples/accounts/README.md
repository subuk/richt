# Local account example

This example creates two local groups and one local system user, sets an exact
supplementary membership, then creates a directory and file owned by that user.
Use a disposable Linux host with GNU/glibc `getent -s files`, shadow account
utilities, and root privileges. User removal additionally needs util-linux
`unshare`/`mount` and mount namespace privilege. This example does not create a
home directory or configure a usable password.

From the repository root:

```sh
make build
bin/richt inventory validate -i examples/accounts/inventory.yaml
bin/richt inventory show -i examples/accounts/inventory.yaml
sudo bin/richt diff -i examples/accounts/inventory.yaml -o /var/tmp/richt-accounts-plan.json
sudo bin/richt apply /var/tmp/richt-accounts-plan.json
getent -s files passwd richt-example
getent -s files group richt-example
getent -s files group richt-example-extra
stat -c '%U:%G %a %n' /var/tmp/richt-account-example /var/tmp/richt-account-example/marker
sudo bin/richt diff -i examples/accounts/inventory.yaml
```

The last diff should show no changes. To remove resources, copy the inventory
and change the tag assignment from `accounts: {}` to
`accounts: {state: absent}`. Prepare and review a new saved plan before applying.
Reverse config order removes the file and directory, then user, then groups.
The directory removal deletes its contents. User removal does not purge the
home path or implicitly delete a same-name group; both group removals here
are explicitly declared.

If apply fails, fix the external cause and explicitly apply the same saved plan
again. Successful tasks remain skipped; unfinished tasks may repeat after
interruption. These are usage instructions; see the repository validation report
for separately recorded live acceptance coverage.
