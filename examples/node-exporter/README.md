# Pinned node_exporter archive and systemd

This example installs the published Linux amd64 node_exporter `1.10.2`
[release](https://github.com/prometheus/node_exporter/releases/tag/v1.10.2),
strips its leading distribution directory, and runs
`/opt/node_exporter/node_exporter` through an owned systemd unit. The SHA-256
comes from the release's published
[sha256sums.txt](https://github.com/prometheus/node_exporter/releases/download/v1.10.2/sha256sums.txt).
The entire `/opt/node_exporter` tree belongs to the archive. Put mutable data,
textfile collector inputs and customizations elsewhere; unknown extra tree
entries block later update or removal.

Use an authorized disposable Linux amd64/systemd target with Python 3.8+, CA
trust, HTTPS access to GitHub release assets, a trusted SSH alias, and passwordless
`sudo -n`. Replace `exporter-test`/`deploy` with your established alias/login.
Start with `/opt/node_exporter` and `richt-node-exporter.service` absent, and
loopback port `19100` unused. The separate unit and loopback port help avoid
colliding with an existing exporter; inspect target resources before applying.
No Go toolchain or installed Richt binary is needed there.

Richt does not create users. Create or arrange an existing dedicated
`node_exporter` system account and group before diff. On a disposable target
with shadow-utils and no such account, an administrator can run:

```sh
ssh exporter-test sudo -n useradd --system --user-group --no-create-home --shell /sbin/nologin node_exporter
```

If the account already exists, review its suitability and do not recreate it.
The archive stays root-owned and cannot be modified by the exporter process.
Keep SELinux Enforcing and install any platform policy needed for your chosen
service; this example does not disable or modify SELinux.

Run from the repository root after `make build`:

```sh
bin/richt inventory validate -i examples/node-exporter/inventory.yaml
bin/richt diff -i examples/node-exporter/inventory.yaml -o /var/tmp/richt-exporter-plan.json
bin/richt apply /var/tmp/richt-exporter-plan.json
ssh exporter-test systemctl is-active richt-node-exporter.service
ssh exporter-test curl -fsS http://127.0.0.1:19100/metrics
bin/richt diff -i examples/node-exporter/inventory.yaml
```

Review the pinned URL/checksum, extraction fields, archive destination and the
start/restart branches before confirming. Diff does not fetch the archive;
apply downloads, verifies and stages it before atomic whole-tree publication,
then writes the unit, reloads systemd and starts the process. An unchanged diff
has no artifact task. The HTTP request is an application check separate from
Richt's systemd-active verification.

For an upgrade or byte-level reversion, change the URL and SHA-256 together to
another verified release. Prepare and apply a new saved plan; the named archive
change requests one restart of an active process. The running process then uses
the newly published executable. Follow upstream compatibility guidance when
changing application versions. Inventory edits or deletion after diff do not
alter a saved request; successful tasks remain skipped during continuation.

To remove the example, change the tag assignment to
`node-exporter: {state: absent}`, prepare and apply. The process is stopped and
disabled, its owned unit is removed, and the archive tree is removed after
ownership/foreign-entry checks. Existing unrelated exporter units, ports and
data are outside this example's scope. Remove the dedicated account manually
only if you created it for this example and it has no other consumers.
