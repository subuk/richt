# Vmango RPM and systemd walkthrough

Run this example on a **disposable AlmaLinux 10 x86_64 machine** with root,
DNF 4, systemd as PID 1, network access to distribution repositories, EPEL and COPR,
and a built Linux Richt binary at `bin/richt`. Run all commands from the
repository root. The example owns `/etc/yum.repos.d/richt-vmango.repo`,
`/etc/vmango.conf`, the `vmango` package and `vmango.service`; start without an
existing Vmango installation or either managed configuration file.

It uses published, signed [Vmango COPR RPMs](https://copr.fedorainfracloud.org/coprs/subuk/vmango/).
No package build, fixture repository, `rpm-build`, `createrepo_c`, or COPR CLI
plugin is needed. The config-only repository service runs before the package
service. The `rhel-10` target is deliberate: EPEL 10 builds target CentOS Stream
and can require newer libvirt than AlmaLinux/RHEL provides. Runtime dependencies
are resolved by DNF during apply; no hypervisor or VM image is required.

The minimal application configuration binds only `127.0.0.1:18080`, has no
clouds/hosts or login users, and stores generated state below `/var/lib/vmango`.
It demonstrates installation and process lifecycle, not VM provisioning or a
usable authenticated deployment. The package supplies the service unit; its
configuration changes use restart because no reload interface is assumed.
Root ownership and mode 0644 let the package-created `vmango` user read the
config without requiring that account to exist during first preparation.

Enable the standard AlmaLinux CRB and EPEL repositories before applying. The
published package requires `genisoimage`, which was available through EPEL in
the recorded AlmaLinux 10.2 run:

```sh
dnf config-manager --set-enabled crb
dnf install -y epel-release
```

These prepare dependency sources; Richt still installs Vmango and its runtime
dependencies through the reviewed plan. Keep package signature checking enabled.

## Inspect, install, and inspect again

Inventory validation opens no templates, contacts no hosts and fetches no
packages. The remaining commands observe or modify this disposable local host:

```sh
bin/richt inventory validate -i examples/vmango/inventory.yaml
bin/richt inventory show -i examples/vmango/inventory.yaml
bin/richt diff -i examples/vmango/inventory.yaml -o /root/vmango-install.json
bin/richt apply -y /root/vmango-install.json
rpm -q vmango
systemctl is-active vmango.service
systemctl is-enabled vmango.service
curl --fail --silent --output /dev/null http://127.0.0.1:18080/
bin/richt diff -i examples/vmango/inventory.yaml
```

The second diff should have no changes. Richt verifies systemd's running state;
the separate HTTP request checks that an HTTP response is served, without
claiming application or hypervisor readiness. For startup diagnostics use
`journalctl -u vmango.service --no-pager -n 50`.

## Exact upgrade, downgrade, and config reaction

The pinned initial version is `0:0.16.0-1.el10.x86_64`. Repository primary
metadata read on 2026-11-01 also contained `0:0.12.3-1.el10.x86_64`,
`0:0.15.0-1.el10.x86_64`, and `0:0.17.0-1.el10.x86_64`.
[Repository metadata](https://download.copr.fedorainfracloud.org/results/subuk/vmango/rhel-10-x86_64/repodata/repomd.xml)
is mutable; if a version is no longer available, inspect available published
versions before selecting another compatible pair. Never replace this step
with a locally built fixture RPM.

Edit `examples/vmango/services/vmango.yaml`: change the exact version from
`0:0.16.0-1.el10.x86_64` to `0:0.17.0-1.el10.x86_64`, then:

```sh
bin/richt diff -i examples/vmango/inventory.yaml -o /root/vmango-upgrade.json
bin/richt apply -y /root/vmango-upgrade.json
rpm -q vmango
systemctl is-active vmango.service
```

Change it back to `0:0.16.0-1.el10.x86_64` and repeat with a new plan path
`/root/vmango-downgrade.json`. DNF's exact installation request permits a
downgrade as well as an upgrade. Omitting `version` instead keeps any installed
version; it does not request the latest available package.

For a config-only change, record the service's `InvocationID`, change
`log_level: info` to `log_level: debug` in `examples/vmango/inventory.yaml`,
prepare and apply a new plan, then compare:

```sh
systemctl show vmango.service -p InvocationID
bin/richt diff -i examples/vmango/inventory.yaml -o /root/vmango-config.json
bin/richt apply -y /root/vmango-config.json
systemctl show vmango.service -p InvocationID
bin/richt diff -i examples/vmango/inventory.yaml
```

The diff displays the config change and restart branch. The invocation changes
when the running process is restarted. A stopped process receives start only;
there is no extra restart after that start. For failure/continuation without
packages, use [the separate config-only example](../continuation/README.md).

## Explicit removal

Set `state: absent` on **both** assignments under `tags.example.services` in
`examples/vmango/inventory.yaml`, keeping service definition orders 10 and 20. Then:

```sh
bin/richt diff -i examples/vmango/inventory.yaml -o /root/vmango-remove.json
bin/richt apply -y /root/vmango-remove.json
rpm -q vmango
systemctl is-active vmango.service
bin/richt diff -i examples/vmango/inventory.yaml
```

The RPM query and active check are expected to return nonzero after removal.
Richt removes the declared repository file, stops/disables the process,
removes its config, then removes its package. Package removal uses the
installed RPM database and does not require the removed repository file.
A missing assignment alone never removes resources. Application state,
package-manager dependencies, possible `.rpmsave` files, generated keys, imported
RPM signing keys, and saved plans are not purged by Richt. Restore the example
files' original version, log level, and present assignments before another run.

Validation status: the published-package lifecycle passed on 2026-11-01 on
AlmaLinux 10.2 x86_64 with DNF 4.20.0, systemd 257 and SELinux Enforcing.
Install, no-op, unversioned preservation, exact upgrade/downgrade, config restart,
independent stopped/enablement states and explicit removal were exercised.
See [the acceptance report](../../docs/validation.md) for dependency prerequisites,
evidence, cleanup and limits.
