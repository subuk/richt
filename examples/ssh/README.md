# Two SSH hosts with published Vmango RPMs

This inventory reuses the [Vmango example](../vmango/README.md) on two
**disposable AlmaLinux 10 x86_64 targets**. Its `richt-test-a` and `richt-test-b`
SSH aliases are placeholders. Configure each alias's `HostName`, non-root `User`,
authentication key/agent and trusted host key using normal OpenSSH settings.
Both accounts need noninteractive `sudo -n` for the managed operations. Both
targets need DNF 4, systemd, distribution repositories, CRB, EPEL and COPR network access;
neither needs a compiler, fixture package build, hypervisor or Vmango login user.
Prepare CRB and EPEL on each target with
`sudo -n dnf config-manager --set-enabled crb` and
`sudo -n dnf install -y epel-release`; the published RPM needs EPEL's
`genisoimage` dependency. Start without an existing Vmango installation or the
example's managed files.
See [SSH rules](../../docs/ssh.md) for explicit overrides and authentication.

From the repository root, inventory-only inspection is safe even before SSH is
configured:

```sh
bin/richt inventory validate -i examples/ssh/inventory.yaml
bin/richt inventory show -i examples/ssh/inventory.yaml
```

After replacing/configuring the aliases, check each connection, prepare once,
then explicitly apply. The checks require no remote configuration changes:

```sh
ssh -o BatchMode=yes richt-test-a 'sudo -n true'
ssh -o BatchMode=yes richt-test-b 'sudo -n true'
bin/richt diff -i examples/ssh/inventory.yaml -o /var/tmp/richt-ssh-plan.json
bin/richt apply -y --parallelism 2 /var/tmp/richt-ssh-plan.json
bin/richt diff -i examples/ssh/inventory.yaml
```

The second diff should have no changes. Hosts can run concurrently, while
services/tasks remain ordered within each host. `--parallelism 1` makes host
execution sequential; the default limit is 20. A failed host blocks its later
tasks while the other host continues. Fix the external problem, then apply the
same saved plan explicitly to skip successful tasks and retry unfinished ones.
No automatic retry occurs. Saved plans retain explicit inventory connections
and ready config bytes; OpenSSH still reads current client configuration.

For cleanup, set both assignments in the shared `tags.example.services` map to
`state: absent`, build a new removal plan and apply it. The Vmango walkthrough describes retained
application data and RPM side effects. Do not delete assignments as a substitute
for declaring absence.

Validation status: published Vmango installation/no-op/removal passed on two
independent AlmaLinux 10.2 x86_64 targets on 2026-11-01 using non-root SSH,
explicit sudo and `--parallelism 2`. A separate two-host secret/file scenario
verified frozen inputs, host failure isolation and manual continuation.
See [the acceptance report](../../docs/validation.md) for environment and limits.
