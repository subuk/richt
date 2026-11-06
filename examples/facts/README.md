# Linux facts and templates

This example collects Linux observations and renders a report under
`/var/tmp/richt-facts-example`. Run on a disposable Linux target with iproute2
supporting JSON link/address/route commands, Python 3 isolated mode, standard
tools, and readable
procfs/sysfs. No root or systemd is needed when that directory is writable.
For a macOS controller, change each transport to SSH with your configured Linux
alias; local macOS fact collection is unsupported. Collection installs nothing.

From the repository root after `make build`:

```sh
./bin/richt inventory validate -i examples/facts/inventory.yaml
./bin/richt facts -i examples/facts/inventory.yaml --refresh
./bin/richt facts -i examples/facts/inventory.yaml --host unassigned --cached
./bin/richt diff -i examples/facts/inventory.yaml --host example --refresh-facts -o /tmp/richt-facts-plan.json
./bin/richt apply -y /tmp/richt-facts-plan.json
cat /var/tmp/richt-facts-example/report.conf
```

Both hosts appear in facts output, including `unassigned`, which has no services.
They represent the same local machine here solely to demonstrate selection and
per-host cache identity. JSON stdout includes provenance, collection time, age,
staleness, and availability; progress goes to stderr. `--cached` requires a prior
successful collection and never contacts targets. Default freshness is 72 hours.
Use `--facts-cache-dir /tmp/richt-example-cache` consistently to isolate the cache,
or `--no-facts-cache` to disable persistent reads/writes for an invocation.

`report.gotmpl` shows typed schema lookup, deterministic interface iteration,
and the IPv4 main-table default. No default or ambiguous defaults leave singular
path fields empty; no primary IP is guessed. For a literal interface name,
including one containing dots, use
`{{ with index (fact "network.interfaces") "eth0.100" }}` and its known fields.

Applying the saved plan uses its prepared bytes without consulting facts or the
cache. Rerun diff with `--refresh-facts` to prepare against current observations.
The full schema, route selection rules, and cache boundaries are in
[host facts](../../docs/facts.md). This walkthrough is illustrative; dated Linux
acceptance results belong in the validation report.

Remove `/var/tmp/richt-facts-example` and the temporary plan when finished.
