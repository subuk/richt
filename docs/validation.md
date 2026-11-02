---
title: "Linux validation"
description: "Recorded Linux acceptance results, environment, commands, and limits."
weight: 120
---

## Ordered component lists — 2026-11-03

KRG-98 was verified on three independent authorized AlmaLinux 10.2 x86_64
hosts with SELinux Enforcing, DNF 4.20.0, RPM 4.19.1.1, and systemd 257.
Local suites used the Linux binary as root; SSH suites used the macOS binary,
a non-root login, and explicit noninteractive sudo.

| Suite | Local Linux | SSH/sudo |
| --- | ---: | ---: |
| Files, SELinux, locking, and saved-plan regression | 17 passed | — |
| Secret-source example | 4 groups passed | — |
| Config validators and ordered continuation | 18 passed | 19 passed |
| Systemd masks and ordered process lifecycle | 24 passed | 24 passed |
| Published RPM versions and ordered binary lists | 13 passed | — |

Mixed named and unnamed configs retained their declaration order and distinct
saved task IDs. A deliberate validator failure recorded the earlier success
and blocked later tasks; continuation after deleting inventory skipped that
success and executed remaining validators in order, without changing IDs.
Validators required earlier files to exist, checking actual execution order.

Four systemd processes with mixed optional labels wrote start/stop event logs.
Starts followed declaration order; absence stopped them in reverse declaration
order, with each stop preceding its disablement. Saved config removal tasks
also reversed their own list. Owned units and configs were removed afterwards.
The same scenarios passed locally and through SSH/sudo, alongside the existing
mask, unmask, daemon-reload, and continuation checks.

The RPM suite used published Vmango, tree, ed, and lsof packages. All four were
absent at preflight. Their install tasks followed declaration order, including
multiple unnamed binaries and named entries whose alphabetical order differed.
All packages installed successfully, and absence produced reverse binary-list
order and removed the declared packages and acceptance repository. Existing
PostgreSQL, Kubernetes/CRI-O, and exporter fixtures were preserved. Custom RPM
fixture suites were not run.

Reproduce on authorized disposable hosts with the prerequisites described by
the individual harnesses:

```sh
python3 tests/integration/files.py --binary ./bin/richt-linux-amd64 --work-root /root --target-root /var/lib
python3 examples/secrets/verify.py ./bin/richt-linux-amd64
python3 tests/integration/config_validation.py --binary ./bin/richt-linux-amd64 --work-root /root/richt-ordered-validation
python3 examples/systemd-mask/verify.py --binary ./bin/richt-linux-amd64 --output /root/richt-ordered-systemd
python3 examples/rpm-versions/verify.py ./bin/richt-linux-amd64 /root/richt-ordered-rpm --ordered-packages tree ed lsof
```

For SSH execution, use the controller binary and add `--host USER@HOST` to the
validator harness or `--ssh-host USER@HOST` to the systemd harness. Supply the
actual authorized target and a new private evidence directory.

The tested source was `8cc99e5ced6e` plus the KRG-98 changes. Binary SHA-256
identities were:

| Binary | SHA-256 |
| --- | --- |
| Linux amd64 | `6d219f1b0dd55ffc4c48a8d1a9a86e5cbf5856cfdfa143a650b872eccfa1b10a` |
| macOS controller | `faa9058f47530d6fbf88e3294c4bc4f978f099c57afd53cffd06a591b238f94a` |

Local `make check`, `make docs-check`, release smoke tests, and race tests for
engine, plan, config, inventory, and YAML loading passed. Go tests additionally
cover side-effect-free CLI inspection, strict list schemas, optional names,
reference validation without reordering, unchanged resources retaining their
original task positions, named/unnamed unit associations, and frozen saved-plan
order during failure and continuation.

Private logs, saved plans, and JSON reports were retained outside the tracked
repository. Harnesses removed temporary target files, units, and added packages;
package-manager-generated application state was not purged. This verifies the
recorded DNF 4/systemd platform, not other distributions or package managers.

## RPM versions, masks, and validators — 2026-11-02

KRG-93, KRG-94, and KRG-95 were verified on three independent authorized
AlmaLinux 10.2 x86_64 hosts with SELinux Enforcing, DNF 4.20.0, RPM 4.19.1.1,
and systemd 257. Each feature was exercised by a Linux binary as local root
and by a macOS controller over SSH using a non-root login with explicit
`sudo -n`. Existing PostgreSQL, Kubernetes/CRI-O, and exporter fixtures were
preserved. No Kubernetes cluster was initialized and the active SSH
configuration was not replaced.

| Suite | Local Linux | SSH/sudo |
| --- | ---: | ---: |
| Published Vmango short/exact/omitted RPM requests | 10 passed | 7 passed |
| systemd masks, ordering, owned units, and continuation | 15 passed | 15 passed |
| Config validators and real isolated `sshd -t` candidates | 17 passed | 18 passed |
| Existing file/SELinux/saved-plan regression harness | 17 passed | — |

The 99 acceptance checks cover RPM install, upgrade and downgrade through
ordinary DNF/YUM versioned install; matching upstream VERSION no-op; exact
and omitted-version compatibility; unavailable-version failure; frozen saved
requests and continuation. Package fixtures use published signed Vmango RPMs,
without building custom packages.

Mask checks cover stop/disable before masking, no-op when already masked,
omission preserving the mask, runtime-to-persistent conversion, unmasking both
mask locations, disabling hidden enablement after unmasking, missing units,
owned-file safety, deliberate failure and saved-plan continuation. Systemd's
masked `UnitFileState` hides enablement links: clearing those links requires
explicit `masked: false` with `enabled: false`, as documented in
[systemd masks](services/processes/systemd.md#masks).

Validator checks cover literal argv, private candidate permissions, fixed
working directory, success, failure, missing executable, timeout, cleanup,
diagnostic redaction, and preservation of destination content and attributes
on rejection. They verify deleted source files, skipped completed tasks,
blocked later tasks, matching-file no-op, attribute-only changes and removal.
The real `sshd -t` tests validate copied configuration at isolated file paths.
The file package tests also ran on Linux, including GNU timeout cleanup,
symlinks, sensitive permissions, and SELinux behavior. Go orchestration tests
verify that a prerequisite package task can supply a missing validator and
that validator failure blocks reload/restart.

Reproduce the focused suites from a checkout on an authorized disposable
host, following each harness's prerequisites:

```sh
python3 examples/rpm-versions/verify.py ./bin/richt-linux-amd64 /root/richt-rpm-versions
python3 examples/systemd-mask/verify.py --binary ./bin/richt-linux-amd64 --output /root/richt-masks
python3 tests/integration/config_validation.py --binary ./bin/richt-linux-amd64 --work-root /root/richt-validation
python3 tests/integration/files.py --binary ./bin/richt-linux-amd64 --work-root /root --target-root /var/lib
```

The mask harness accepts `--ssh-host`, and the validator harness accepts
`--host`, with a non-root SSH destination and a controller binary. The RPM SSH run used the same published
package scenario, including a YUM 4 upgrade and DNF 4 downgrade.

Local `make check`, relevant `go test -race`, and `make docs-check` passed.
The source was `e0b7991bf3f7fa9bb17a13a65001b69ce3182190` plus the three feature
changes and tests. Live binary SHA-256 identities were:

| Binary | SHA-256 |
| --- | --- |
| Linux file/mask suites | `032282b709db7ee04d9ccce7ce4a3b3a35eb0e06e2215d53fc9b3eddc07efb2a` |
| macOS file/mask controller | `ab4d75129387c1ad5bde4e3173f70cce837624abe4710a5b52fe0e1654bc5e0d` |
| Linux RPM suite | `b6d48ba6dccae2a5b83b373a0fcf978ac862ec0fa4cae711fd1eb3e01d68a41c` |

Command logs and JSON acceptance reports were retained in private test work
directories. Temporary validator files, test units and configs, and declared
RPM acceptance packages/repositories were cleaned up; package-manager-generated
application state was not purged. This is DNF 4/systemd 257 coverage, not DNF 5,
other distributions, or full multi-file application validation. Abrupt host or
process termination can leave private validator candidates behind.

## Baseline acceptance — 2026-11-01

Acceptance on 2026-11-01 passed on two authorized disposable AlmaLinux 10.2
x86_64 hosts with DNF 4.20.0, systemd 257, Python 3.12.13 and SELinux
Enforcing. The controller was macOS with the system OpenSSH client; remote
execution used a non-root account with explicit passwordless `sudo -n`.
Both targets had independent RPM databases, filesystems and systemd instances.

The tested source was revision
`187f67a62a09179e0f5e2c01c918e3341aeb102d` plus the current documentation and
example changes. The coordinator's `make check` passed before transfer. Both
binaries reported `richt dev`. Binary SHA-256 identities were:

| Binary | SHA-256 |
| --- | --- |
| macOS controller | `d07ed96789b60d4c8aee7bbca1185c30cecc2b8c314be8fd35273db6a77fc225` |
| Linux amd64 target | `40335cbccfa7e4dc51604d1ef6e579d94e7292f8a8c6aa278859613a4b2e6feb` |

The Linux binary was cross-built and transferred with the example sources.
It was executed on Linux; a Go installation or target-side build was not tested.

## Files, secrets and saved plans

These commands passed on host A as root:

```sh
python3 tests/integration/files.py --binary ./bin/richt --work-root /root --target-root /var/lib
python3 examples/secrets/verify.py ./bin/richt
```

The file suite passed all **17 checks**: create/update/no-op/explicit absence,
root ownership, requested modes and SELinux labels, symlink behavior, missing
owner/group rejection, frozen saved inputs, inode preservation, sensitive
displays, partial failure and manual continuation, terminal confirmation, and
same-plan apply locking. Its secret checks cover independent YAML sources,
multiline bytes, private plans and continuation without source files.

The standalone secret example passed all four verification groups, including
source/template/inventory deletion before apply and explicit-sensitive removal.
Its private temporary files and the file suite's allocated directories were
removed automatically.

The [quickstart](quickstart.md) shell commands passed on Linux using the transferred
binary in place of `make build` and explicit `apply -y` for noninteractive
execution: create, no-op, update and removal all succeeded. The
[continuation walkthrough](../examples/continuation/README.md) produced the expected
second-service failure, blocked later work and successful explicit continuation.
Its inventory was deleted before continuation to additionally verify frozen inputs.
The separate file suite exercised actual terminal agreement and cancellation.

## Published RPM and local systemd lifecycle

The [Vmango walkthrough](../examples/vmango/README.md) used published signed RPMs
from the COPR `rhel-10-x86_64` repository, with signature checking enabled.
The repository and Vmango were absent before preparation. CRB was already
enabled. The first apply wrote the repository file, then failed dependency
resolution because `genisoimage` was unavailable without EPEL. After:

```sh
dnf install -y epel-release
```

an explicit apply of the **same saved plan** skipped the successful repository
write and installed Vmango. The examples now document CRB and EPEL prerequisites.
No package was rebuilt to work around this dependency.

Private copies of the example were used for the following passed scenarios:

| Scenario | Observed result |
| --- | --- |
| Install `0:0.16.0-1.el10.x86_64` | RPM version matched; unit active and enabled; loopback HTTP request succeeded |
| Repeat diff | No changes |
| Omit `version` while 0.17 was available | No changes; installed version remained 0.16 |
| Exact upgrade to `0:0.17.0-1.el10.x86_64` | RPM version matched; active/enabled and HTTP checks passed |
| Exact downgrade to `0:0.16.0-1.el10.x86_64` | RPM version matched; active/enabled and HTTP checks passed |
| Change `log_level` from info to debug | Restart branch applied; systemd `InvocationID` changed; HTTP and subsequent no-op checks passed |
| Request stopped and enabled | Process inactive while enablement remained enabled |
| Request running and disabled | Process active while enablement was disabled |
| Restore enabled, then declare both services absent | Stop/disable completed; managed config, repository and RPM removed; subsequent diff had no changes |

Each mutation used `bin/richt diff -i INVENTORY -o PLAN` followed by
`bin/richt apply -y PLAN`. Runtime checks used `rpm -q`, `systemctl is-active`,
`systemctl is-enabled`, `systemctl show -p InvocationID` and
`curl --fail --silent --output /dev/null http://127.0.0.1:18080/`.
The HTTP check confirms an HTTP response, not hypervisor functionality or an
authenticated application deployment. No clouds, login users or hypervisors
were configured.

## Two real SSH targets

The [SSH example](../examples/ssh/README.md) was copied with explicit target
addresses, `user: almalinux` and `sudo: true`. On the macOS controller:

```sh
bin/richt diff -i INVENTORY -o PLAN
bin/richt apply -y --parallelism 2 PLAN
bin/richt diff -i INVENTORY
```

Both hosts installed published Vmango 0.16, reached active/enabled state,
served loopback HTTP and then produced a no-op diff. A separate absent plan,
also applied with parallelism 2, stopped/disabled and removed the package,
managed config and repository on both targets.

A separate temporary config-only inventory exercised two independent local
YAML secret sources across the same SSH targets. Its sources, template and
inventory were deleted after diff and before apply. A deliberate parent-path
obstruction on host A caused its second service to fail after the first
succeeded; its third service was blocked. Host B completed all three services.
After removing the obstruction, explicit continuation of that same plan
completed host A and skipped all successful tasks on both hosts. Deliberate
external edits to already successful files remained untouched, proving those
writes were skipped. Frozen secret bytes, target mode 0600, private plan/lock
modes and redacted CLI displays were verified. All six tasks ultimately had
saved success status. Temporary remote targets were then removed.

## Cleanup and limits

Final checks on both hosts found no installed Vmango package, no managed
`/etc/vmango.conf` or repository file, and a not-found/inactive Vmango unit.
SELinux remained Enforcing. Neither `rpm-build` nor `createrepo_c` was installed
at preflight or completion.

Package removal left about 12 KiB of generated application state under
`/var/lib/vmango` on each host, including generated signing material. Host A
also retained a package-generated `/etc/vmango.conf.rpmnew` after the version
changes. EPEL release/policy packages, imported RPM signing keys and package
manager caches were retained; DNF removed the `genisoimage` dependency during
package removal. Private source copies, command captures and executable plans
remain outside the tracked repository for review. They are not published with
this documentation.

No custom RPM was built. The custom `richt-demo` suites
(`examples/rpm-systemd/verify.py` and the full `tests/integration/ssh.py`) were
deliberately excluded from this run. Their fixture-specific reload, timeout,
changed-identity and YUM scenarios are not established by this report. This run
also does not establish behavior on other distributions or DNF versions.

## Tag assignments and service order, 2026-11-02

KRG-91 was verified on three independent disposable AlmaLinux 10.2 x86_64
hosts with SELinux Enforcing, DNF 4.20.0 and systemd 257. The macOS controller
used non-root SSH logins and explicit `sudo: true`. CRB was already enabled;
EPEL was initially absent on the PostgreSQL host and present on the two package
hosts. Firewalld was inactive, so no firewall changes were required.

The source was revision `aaa56d175b1ef22e6826b131591b5820b6c5e198` plus the
KRG-91 working-tree changes. Tested binary SHA-256 identities were:

| Binary | SHA-256 |
| --- | --- |
| macOS controller | `905855e589a6422048b39d04c0c2d53446b361458e904e857a9f21537693d191` |
| Linux amd64 | `a5d360a89c4abe442aae009d517421aa207b17e2cf9f97d6b539d1db23bd3f61` |

The updated Linux file harness passed all 17 checks, and the secret example
passed all four groups. The updated quickstart passed create, no-op, update
and explicit removal on Linux using the transferred binary and `apply -y`.
Local `make check` and `make docs-check` passed; inventory validate/show smoke
checks covered every checked-in example. Go regression tests cover inherited
ordering, duplicate and ownership failures before service filtering, strict
source validation, saved inputs, failure blocking and explicit continuation.

### Three-host workload

The standard-library driver [tag_services.py](../tests/integration/tag_services.py)
generates private inventory, bare templates, a YAML secret source and saved
plans. Run it only against explicitly authorized disposable hosts. The following
names are illustrative; supply the authorized addresses or SSH aliases:

```sh
make build
python3 tests/integration/tag_services.py --binary ./bin/richt \
  --work-root /tmp/richt-tag-services \
  --postgres-host postgres-test.example \
  --kubernetes-host kubernetes-test-a.example \
  --kubernetes-host kubernetes-test-b.example
```

Targets need trusted host keys, noninteractive sudo, Python 3, curl, ss,
enabled CRB, network access to the package repositories and an unused PostgreSQL
data directory. This fixture requires inactive firewalld; prepare an equivalent
isolated network policy separately when using a firewall. `--user` defaults to
`almalinux`; use `--postgres-address` when the SSH name is not a database address
reachable from the Kubernetes hosts. The fixture does not build custom RPMs.
Kubernetes and CRI-O use their official v1.36 repositories, as described by
[Kubernetes](https://v1-36.docs.kubernetes.io/docs/setup/production-environment/tools/kubeadm/install-kubeadm/)
and [CRI-O](https://cri-o.io/); repository and RPM signature checks remain enabled.

Assignments came exclusively from tags. All hosts had one shared `monitoring`
tag assigning EPEL setup (service order 5) and node exporter (50). Kubernetes
hosts also received repository configuration (10) and packages (30) from the
`kubernetes` tag; the PostgreSQL host received its service (40) from the
`postgresql` tag. Resolved services and saved tasks followed service order.
`--tag monitoring` selected all three hosts, and `--service node-exporter`
resolved exactly one assignment on each.

| Resource | Installed version or verified state |
| --- | --- |
| PostgreSQL server/client | `16.15-1.el10_2.x86_64` on the database host; client also installed on both Kubernetes hosts |
| kubeadm, kubelet, kubectl | `1.36.6-150500.1.1.x86_64` on both Kubernetes hosts |
| CRI-O | `1.36.7-150500.1.1.x86_64` on both Kubernetes hosts |
| node-exporter | `1.12.1-1.el10_2.x86_64` on all three hosts |
| epel-release | `10-6.el10.noarch` on all three hosts |
| PostgreSQL unit | `postgresql.service`, active and enabled |
| Exporter unit | Managed through `node_exporter.service`, an alias of `prometheus-node-exporter.service`; active and enabled on all hosts |
| kubelet unit | Inactive and disabled on both hosts |

No kubeadm command was invoked, no cluster was initialized or joined, and
CRI-O coverage establishes package installation rather than container runtime
functionality. Each exporter returned HTTP 200 and `node_uname_info` at
`http://127.0.0.1:9100/metrics`.

PostgreSQL initialized a fresh test cluster through an owned systemd oneshot
unit. Its effective settings were `listen_addresses = '0.0.0.0'` and
`password_encryption = 'scram-sha-256'`; `ss` confirmed `0.0.0.0:5432`.
The external HBA file contained only local peer authentication and
`host all all 192.168.0.0/16 scram-sha-256`, with no earlier password bypass.
Both Kubernetes hosts connected to the dedicated `richt_krg91` database and
role using the correct generated password and failed password authentication
with an incorrect one. A boolean database query verified the stored role
password was SCRAM. The password entered through `secret`, remained in a
root-owned mode-0600 SQL file and private executable inputs, and was excluded
from displays and reports. External PostgreSQL config files received the
`postgresql_etc_t` SELinux label; SELinux remained Enforcing throughout.

### Fixture correction, continuation and retained state

The first saved apply completed both Kubernetes hosts but PostgreSQL startup
failed with `203/EXEC`: the fixture initially used `/usr/bin/postmaster`, while
the installed package provides `/usr/bin/postgres`. Inspection of the packaged
unit identified the mismatch. A separate config-only Richt plan repaired the
drop-in and reloaded systemd; the fixture now uses the verified path. Explicit
apply of the original saved plan skipped successful tasks, retried PostgreSQL
startup, bootstrapped the test role and completed monitoring. All 44 original
tasks ended in success. No backend or saved-plan format change was needed.

The driver's `--verify-existing` mode then passed its runtime assertions and
saved a private final no-op plan with zero tasks on each host. This mode
requires the original private work directory and matching target options,
performs probes and diff without apply, and preserves the initial report and
logs. Commands used for correction and continuation were:

```sh
bin/richt diff -i REPAIR.yaml -o REPAIR.json
bin/richt apply -y REPAIR.json
bin/richt apply -y --parallelism 3 FIRST-PLAN.json
# Repeat the driver invocation with the same paths/targets and:
# --verify-existing
```

Retained resources include the installed packages and repositories, node
exporter on all hosts, PostgreSQL data/test role/database, its external configs,
private bootstrap SQL and initialization/bootstrap oneshot units. The file
harness retained its uniquely named inputs and targets for inspection; the
standalone secret example and quickstart removed their temporary resources.
Private controller evidence retains inventories, secret source, executable
plans/progress, original failure, repair and continuation logs, verification
results and actual target identities outside the tracked repository. Restore
the disposable hosts and choose an empty work directory before a fresh workload
run. The custom RPM/systemd and full SSH fixture suites were not rerun; their
inputs were updated and validated locally.

## File acceptance harness

Run local unit and CLI checks with `go test ./...`. On an authorized disposable
Linux machine with SELinux Enforcing, run the file acceptance suite as root:

```sh
python3 tests/integration/files.py --binary ./bin/richt --work-root /root --target-root /var/lib
```

The suite allocates unique test directories beneath the specified existing
roots and removes only those directories afterwards. `--keep` retains them for
inspection. It verifies create/update/no changes/absent, ownership/mode/SELinux,
frozen saved inputs, inode preservation, missing owner/group errors, ordinary
symlink writes, partial progress and continuation, sensitive display, actual
terminal confirmation, and concurrent apply locking. It does not install
packages or manage services.

All 17 checks passed on 2026-11-01 on a disposable AlmaLinux 10.2 x86_64 host
with SELinux Enforcing. This includes two independent YAML sources, multiline
secret bytes, private plan permissions, continuation after deleting sources,
requested target mode and SELinux label, and explicit sensitivity when replacing
old secret content. The suite removes its temporary directories after completion.
The later 2026-11-02 run and its retained directories are recorded above.

## RPM and systemd fixture regression

The following harness is a separate developer regression tool. Its custom RPM
build prerequisites are not required by the ready-vmango walkthrough or current
package acceptance.

Use a disposable AlmaLinux machine with root privileges. The fixture only
manages resources named `richt-*`; the harness deliberately installs/removes
its test package and changes its test units. Do not run it on a user host.

```sh
dnf install -y rpm-build createrepo_c python3
# Build Richt on the target, or copy a Linux binary built on another machine:
# CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o richt .
python3 examples/rpm-systemd/verify.py ./richt /root/richt-service-test
```

The harness builds real versions `1.0-1` and `2.0-1` of a minimal noarch RPM,
creates a file repository, and runs the CLI against it. It checks first install
with a previously absent repo config, exact update/downgrade, no-op diff,
unversioned policy, merged reactions, no extra restart after start, independent
enablement and stopped state, multiple processes in declaration order, command failure,
a shared command/wait timeout, unsupported reload, persisted process resume,
YUM 4 exact install/removal, and complete declared-resource removal. It also
checks initial secret rendering and secret rotation, actual reload reactions,
redacted file display, and frozen apply after deleting the secret source.
The recorded fixture run passed all 33 checks on 2026-11-01 on AlmaLinux 10.2
x86_64 with DNF 4.20.0, YUM 4 compatibility, systemd 257, and SELinux Enforcing.
This is evidence for that fixture suite, not a verification claim for the
ready-vmango example. Plans, displayed diffs, apply output,
and `acceptance.json` remain in the work directory. Application event traces
are deliberately retained. Fixture scripts require only standard Python and
shell facilities in addition to the listed RPM build tools.

For a manual walkthrough:

```sh
sh examples/rpm-systemd/build-repository.sh
./richt diff -i examples/rpm-systemd/inventory.yaml -o /root/demo-plan.json
./richt apply -y /root/demo-plan.json
./richt diff -i examples/rpm-systemd/inventory.yaml
```

Change `version` between `0:1.0-1.noarch` and `0:2.0-1.noarch`, or change the
config inline content, to inspect the resulting exact package and process
branches. Set the demo assignment to `absent` to remove it.
