# systemd masking example

Use an authorized disposable Linux system with systemd and root privileges.
This example manages only `richt-mask-example.service`. Run from the repository
root after building `bin/richt`.

Prepare and review a persistent mask, including when the unit does not exist:

```sh
bin/richt inventory validate -i examples/systemd-mask/inventory.yaml
bin/richt diff -i examples/systemd-mask/inventory.yaml -o /tmp/richt-mask.json
bin/richt apply -y /tmp/richt-mask.json
systemctl show -p LoadState -p UnitFileState richt-mask-example.service
```

`state: stopped` and `masked: true` are independent targets. Masking follows a
needed stop. Omission of `masked` preserves existing masks; `masked: false`
explicitly removes both persistent and runtime masks.

The second inventory replaces the mask with an owned full unit, enables it, and
starts it. The unmask task precedes the prepared unit file write, daemon reload,
enablement, and start. File writes therefore never follow the mask to `/dev/null`.

```sh
bin/richt diff -i examples/systemd-mask/running.yaml -o /tmp/richt-unmask.json
bin/richt apply -y /tmp/richt-unmask.json
systemctl show -p LoadState -p ActiveState -p UnitFileState richt-mask-example.service
bin/richt diff -i examples/systemd-mask/running.yaml
```

Each mask, enablement, and lifecycle task has separate progress. If a command or
verification fails, fix its external cause and explicitly apply that same saved
plan again. Successful tasks are skipped; files are not reread or rendered again.

After reviewing the resources, clean up the example:

```sh
systemctl stop richt-mask-example.service
systemctl disable richt-mask-example.service
systemctl unmask richt-mask-example.service
systemctl unmask --runtime richt-mask-example.service
rm -f /etc/systemd/system/richt-mask-example.service
systemctl daemon-reload
rm -f /tmp/richt-mask.json /tmp/richt-mask.json.lock
rm -f /tmp/richt-unmask.json /tmp/richt-unmask.json.lock
```

Do not apply the masking inventory over the owned full unit: both occupy the
same `/etc/systemd/system` path, and systemd rejects masking that existing file.
Remove the owned full unit first if you want to repeat the complete walkthrough.

The acceptance harness allocates unique fixture unit names, removes its managed
resources in `finally`, and retains inventories, saved plans, command output,
platform details, and an `acceptance.json` report. It installs no packages. Run
it as root on the disposable Linux system, or from a controller with an existing
trusted SSH login and noninteractive sudo:

```sh
sudo python3 examples/systemd-mask/verify.py \
  --binary ./bin/richt-linux-amd64 --output /tmp/richt-mask-local-check
python3 examples/systemd-mask/verify.py --binary ./bin/richt \
  --ssh-host deploy@test-target --output /tmp/richt-mask-ssh-check
```

The checks cover masking after stop/disable, matching targets, omission,
runtime-to-persistent conversion, removal of both masks, hidden enablement links,
missing units, owned-unit safety, and saved-plan continuation after an intentional
mask failure and deletion of the inventory.

Socket checks use a separate unique socket/service pair. They cover listening
state, enablement, persistent and runtime masks, full units and drop-ins,
coalesced restarts, unsupported reload, and continuation after a socket cannot
start while its activated service is still active. The service is explicitly
stopped before continuing; socket operations never add hidden service tasks.
See the [socket example](../systemd-socket/README.md) for a small owned listener.

The same harness also prepares and applies an ordered service with four configs
and four owned processes. It mixes reverse-alphabetical names (`z-first`, then
`a-third`) with two unnamed items in each list. This demonstrates that declaration
order controls execution, while names remain optional reference labels. Configs
use both the omitted type default and explicit `type: template`.

The generated process list has this shape; each unit includes an owned definition
that records its start and stop in a unique event file:

```yaml
processes:
  - name: z-first
    type: systemd
    unit: example-first.service
  - type: systemd
    unit: example-second.service
  - name: a-third
    type: systemd
    unit: example-third.service
  - type: systemd
    unit: example-fourth.service
```

The checks compare prepared config and process order, actual start events, reverse
config removal order, and actual stop events. Absence visits the process list
from fourth to first, stopping each before disabling it, and removes configs in
reverse config-list order. Services continue to run in ascending service order.
The event log, configs, and owned units are cleaned up in `finally`.
