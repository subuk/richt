---
title: "Host facts"
description: "Read-only Linux observations, controller caching, and strict template lookup."
weight: 65
---

Facts describe a Linux target as seen by the collector. `richt facts` prints
selected hosts as indented JSON; configuration templates access the same typed
schema through `fact`. Facts do not override merged inventory variables, select
hosts or services, choose package backends, or interpolate resource definitions.

```sh
richt facts -i inventory.yaml
richt facts -i inventory.yaml --host app-a --refresh
richt facts -i inventory.yaml --tag production --cached
richt diff -i inventory.yaml --facts-max-age 15m --refresh-facts -o plan.json
```

The facts command accepts inventory/source settings and host/tag selectors,
including hosts with no assigned services. It does not accept `--service`.
Host/tag selection uses the existing union rules. The default `--parallelism 20`
bounds concurrent hosts; any positive override is accepted. A failed host does
not stop the other selected hosts. Any host failure produces a nonzero exit.
`--progress=auto|plain|off` uses stderr; stdout remains JSON even when some hosts
fail. Inventory commands never access the facts cache or collect facts.

## Schema and defaults

Snapshot schema version is `"1"`. A snapshot contains `schema_version`, UTC
`collected_at`, `facts`, and `availability`. The command envelope has
`schema_version: 1` and a `hosts` array sorted by host name. Each successful
host result includes `host`, the snapshot fields, `source` (`cache` or `live`),
`age_seconds` (number), and `stale` (boolean). Failed results include `host` and
an `error`; snapshot and source fields are omitted. Availability and fact
interface dictionaries encode their keys in sorted order.

| Fact path | Type and meaning | Default |
| --- | --- | --- |
| `os.id` | String from target os-release `ID` | `""` |
| `os.version_id` | String from `VERSION_ID` | `""` |
| `os.name` | String from `NAME` | `""` |
| `os.id_like` | Ordered list of strings split from `ID_LIKE` | `[]` |
| `kernel.release` | String from target `uname -r` | `""` |
| `architecture` | String from target `uname -m` | `""` |
| `hostname` | Target hostname string; no DNS FQDN lookup | `""` |
| `cpu.logical_count` | Integer count of online logical CPUs visible through target sysfs | `0` |
| `memory.total_bytes` | Integer host-visible RAM in bytes from target `/proc/meminfo` | `0` |
| `network.interfaces` | Dictionary keyed by exact interface name | `{}` |
| `network.default_ipv4` | IPv4 main-table default-route record | Empty path, `status: absent` |
| `network.default_ipv6` | IPv6 main-table default-route record | Empty path, `status: absent` |

Interface records contain `name`, `mac`, and lowercase `state` strings,
integer `mtu` in bytes, and an `addresses` list. String defaults are `""`,
number defaults are `0`, and list defaults are `[]`. Interface names are literal,
including dots. Each address contains `family` (`inet` or `inet6`), IP `address`,
integer `prefix_length` in bits, and string `scope`. Known address-field defaults
are empty strings and zero, although a populated address record must contain a
valid family/address/prefix. Addresses are sorted by family,
address, prefix length, then scope. There is no guessed primary host IP.

Default-route records contain string fields `interface`, `gateway`, and
`status`. Status is `selected`, `absent`, or `ambiguous`. Unselected interface
and gateway are empty strings. A selected directly connected default can have
an empty gateway. IPv4 and IPv6 may select different interfaces.

`availability` maps known schema paths to `{status, reason}` records. Status is
`observed` or `unavailable`; fixed reasons describe unpopulated OS attributes
(`attribute_absent` or `os_release_absent`) and unselected routes
(`default_route_absent` or `default_route_ambiguous`). Route observations remain
`observed` when their result is absent or ambiguous; the reason and route status
explain why no singular path was selected. A default value alone
cannot distinguish missing information from an observed empty/zero value.
Known unpopulated fields keep their typed defaults in JSON, cache, and templates.
Interface availability has an `entries` map keyed by literal interface name;
each entry has a `fields` map for `name`, `mac`, `mtu`, `state`, and `addresses`.
Address availability uses index-string `entries` with field metadata for
`family`, `address`, `prefix_length`, and `scope`. Missing fields use
`attribute_absent`; missing address records use `address_record_absent`. Empty
observed values stay distinguishable from unavailable defaults.
Unknown schema paths fail lookup, including lookups used inside formatting.
An absent dictionary entry is handled separately with template `with`.

CPU and memory values describe the host-visible kernel interfaces. In containers
they may describe resources beyond the container's quotas; they are not a
promise of CPUs or RAM available to an application.

## Target observation

Collection uses the existing local/SSH transport, OpenSSH client settings,
authentication, host-key policy, and explicitly configured `sudo -n`. Sudo is
never enabled implicitly. A macOS controller can collect from a Linux SSH target;
local collection on unsupported platforms fails clearly.

Targets require Python 3 with isolated mode (`python3 -I`), `cat`, `uname`,
`hostname`, readable
`/sys/devices/system/cpu/online` and `/proc/meminfo`, and iproute2 supporting
`ip -j link show`, `ip -j address show`, and IPv4/IPv6 JSON route inspection.
Richt installs nothing and needs no target daemon or installed Richt agent.
Output is bounded and one host collection has a 30-second total timeout with
caller cancellation. A snapshot is one logical observation; fields need not be
measured at the same physical instant.

A bounded Python file reader selects `/etc/os-release`, falling back to `/usr/lib/os-release`
only when the former is absent. It parses the file as data and never sources
or executes it. Read/permission/parse failures do not trigger fallback. If both
files are absent, OS attributes default with unavailable metadata. Kernel and
architecture always come from the target's separate uname observations.

Interface link/address records join by interface index. The collector examines
IPv4 and IPv6 defaults separately with
`ip -j -4 route show table main default` and
`ip -j -6 route show table main default`. Unusable routes are excluded. Lowest
metric/priority wins; IPv6 preference ranks high, medium, then low among equal
metrics. A unique best outgoing path selects its interface and gateway.
Equally preferred distinct paths, multipath without a unique path, or unresolved
nexthop information produce `ambiguous`; no usable default produces `absent`.
Output order and interface names never break ties. Inspection makes no external
connectivity probe.

The scope is the collector's network namespace and routing table `main`.
These records do not resolve policy routing, VRFs, or routes for a particular
source/destination packet. Refresh when current addresses/routes matter.
Connection failures, malformed output, missing dependencies, and timeouts fail
the whole host snapshot; they never become an all-default successful result.

## Controller cache

The default directory is `os.UserCacheDir()/richt/facts` on the controller;
`--facts-cache-dir PATH` selects another directory. Default maximum age is
**72 hours**, adjustable through `--facts-max-age DURATION`. Negative ages fail;
zero forces collection in normal mode. Missing, expired, incompatible,
malformed, or future-dated records require fresh collection.

`--refresh` on `facts` and `--refresh-facts` on `diff` or inventory-based `apply`
bypass cache reads and publish a successful replacement. `--no-facts-cache`
disables persistent reads and writes while preserving one snapshot per host in
the invocation. `facts --cached` reads successful snapshots without contacting
targets, opening templates, or resolving template secrets. It can load inventory
from HTTPS. Expired snapshots remain visible with `stale: true`; missing or
invalid snapshots are per-host failures. `--cached --refresh` and
`--cached --no-facts-cache` are rejected before target work.

Cache keys include the canonical configured root inventory source (absolute
local path or sanitized requested HTTPS URL), host name, explicit transport
type/target/user/port/sudo, and schema/collector version. Redirect destinations
are not the configured root source identity. No URL credentials are persisted.
A cache hit does not validate that a DNS name or SSH alias still identifies the
same machine. Explicitly refresh after alias/DNS retargeting, reinstallation,
or a changed HTTPS redirect destination.

Only complete successful snapshots are cached. A failed refresh preserves the
previous successful entry and fails the host without silently using stale data.
Existing cache directories must be private; Richt does not change permissions
on an explicitly selected shared directory such as `/tmp`. Cache
directories/files are private (0700/0600 where supported), publication is
atomic, and concurrent writers cannot publish torn or older snapshots over newer
ones. Cache I/O errors, including write failures after successful collection,
fail the host or preparation. Correct the cache permissions/storage or explicitly
choose `--no-facts-cache` to run without persistent caching.

The cache is disposable derived data. It contains no template contents, secrets,
SSH credentials, arbitrary command output, or executable plans.

## Templates and saved plans

```gotemplate
os = {{ fact "os.id" }}
arch = {{ fact "architecture" }}
workers = {{ fact "cpu.logical_count" }}
memory_bytes = {{ fact "memory.total_bytes" }}
{{ with index (fact "network.interfaces") "eth0.100" }}
mtu = {{ .mtu }}
{{ range .addresses }}
address = {{ .address }}/{{ .prefix_length }}
{{ end }}
{{ end }}
{{ with fact "network.default_ipv4.interface" }}
{{ with index (fact "network.interfaces") . }}
default_ipv4_mac = {{ .mac }}
{{ end }}
{{ end }}
```

Use `network.default_ipv6` for IPv6. Inspect route `status` to distinguish
absence from ambiguity. Access interface names with `index`; dynamic names
are not dotted schema paths. Unknown interface record fields fail rendering.
Existing inventory variables named `fact` or `facts` remain accessible as
`.fact` and `.facts`.

The first evaluated `fact` call lazily obtains the host snapshot, reusing it
for every template and component in that preparation, including systemd units
and drop-ins. An unexecuted branch, template without facts, excluded service,
or absent assignment does not request facts. Fact-dependent rendering may read
the target before ordinary resource observation. Templates without facts retain
their rendering-before-observation behavior. Sensitive templates retain safe
generic errors.

Facts affect the rendered bytes at preparation only. Saved-plan apply and
continuation do not collect facts, consult the cache, rerender templates, or
recalculate changes. Cache deletion or changed target facts cannot alter the
prepared bytes. Full fact snapshots and cache data are not embedded in plans.
All explicitly supplied facts preparation flags are rejected with a saved plan,
even when their values equal defaults. See [saved plans](files.md),
[templates](services/configs/files.md), and the [facts example](../examples/facts/README.md).
