---
title: "Linux validation"
description: "Recorded Linux acceptance results, environment, commands, and limits."
weight: 120
---

## Local accounts — 2026-11-03

User/group configs were exercised on an authorized AlmaLinux 10.2 x86_64 host
with shadow-utils 4.15.0, glibc 2.39, util-linux 2.40.2, systemd 257 and SELinux
Enforcing. Source was `7362897979337341f0017f4d0e1954c9c66e3b16` plus the
account changes recorded with this report. The final local Linux binary SHA-256
was `7976720ae84579dac6a08d9204b63047e69936f5df6cb4e0cffb65fa3045b5eb`;
the SSH controller binary was
`3cc82f040230dd5c7e7643960f0ac178a506f0701918d33bac2a3e368baa80b2`.
The final local rerun includes the additional absent-account consumer preflight;
SSH command execution is unchanged by that guard.

| Suite | Result |
| --- | --- |
| Local account CLI harness | 21 checks passed |
| Account CLI over SSH and explicit sudo | 21 checks passed |
| Existing file/SELinux/progress/secret regression | 17 checks passed |
| Existing oneshot and systemd process regression | 28 checks passed; no package transaction requested |
| Existing file validators | 18 checks passed, including real sshd configuration validation |
| Repository verification | `make check`, `make docs-check`, and relevant race tests passed |

The account runs covered side-effect-free inspection and preparation, group/user
ordering, planned file owners, saved apply without inventory, empty subsequent
diffs, automatic and explicit IDs, exact and omitted supplementary memberships,
config reactions, interrupted create/update/delete continuation, changed-UID
refusal and recovery, reverse removal, and preservation of home data, same-name
groups and the host login.defs checksum. Go tests cover external NSS conflicts,
protected/duplicate IDs, malformed saved operations, absent-account consumers,
and account ownership conflicts before service filtering.

The first local run exposed an unsupported `useradd --key CREATE_MAIL_SPOOL=no`
override in shadow-utils 4.15.0. The override was removed; mailbox creation now
follows target defaults. Its fixtures were cleaned before the passing rerun.
No host login policy was changed. Private plans, command logs and reports were
retained; temporary users, groups, files and service units were removed.
These checks do not establish LDAP/SSSD mutation support, other distributions,
or exhaustive interruption recovery. User deletion requires the documented
private mount namespace capability.

Reproduce only on an authorized disposable Linux target:

```sh
python3 tests/integration/accounts.py --binary ./bin/richt-linux-amd64 --output /var/tmp/richt-accounts-local
python3 tests/integration/accounts.py --binary ./bin/richt --ssh-host deploy@account-test.example --output /tmp/richt-accounts-ssh
```

## Typed configs — 2026-11-03

KRG-96 was exercised on three authorized AlmaLinux 10.2 x86_64 targets with
SELinux Enforcing, systemd 257, LVM2 and XFS tools. The storage fixtures used
two private 2 GiB sparse files and loop devices, never the host's real disks.
The missing mdadm package was installed on the storage test targets.

| Suite | Local Linux | SSH/sudo |
| --- | ---: | ---: |
| RAID1, VG, LV, XFS, two mounts, frozen plans and removal | 26 passed | 27 passed |
| Plain files/directories, identity checks and bind-mount refusal | 13 passed | 13 passed |
| Existing files/SELinux/progress/locking | 17 passed | — |
| Existing systemd masks/lifecycle | 24 passed | — |
| Secret-source regression | 4 groups passed | — |
| Boot persistence, filesystem identity and data | — | Verified after VM recovery |

The storage runs verified creation of downstream resources absent during diff,
compatible adoption with an empty subsequent diff, unmanaged file contents,
UUID fstab persistence and composition, preservation of comments, mount-only
absence retaining source data, busy-mount failure, explicit continuation, and
reverse destructive deletion including exact MD member metadata removal.
The local harness reported a loop-detach command error after all product checks;
postflight confirmed both loops had detached and removed its remaining backing
files. The harness command was corrected. SSH cleanup completed without errors.

Go tests additionally cover interruptions after storage side effects, raw and
managed resource replacement, filesystem signature conflicts, empty-only VG
deletion, aliases, invalid sequence, reactions, and hidden mount-point paths.
`make check`, `make docs-check`, and race tests for `plan`, `engine`, `config`,
`mount`, and `storage` passed. The source is `ab34a00e9f1f` plus the KRG-96 changes.

Boot acceptance verified automatic MD assembly, LVM activation and the UUID
fstab mount, the original filesystem UUID and marker contents, and an empty
post-boot diff. The initial fixture exposed a loop-specific setup error: MD
udev assembly ignores bare-device change events. Its required mount blocked
boot until the operator added `nofail`. The corrected fixture reattaches its
loops and emits scoped device-add events; its fstab entry uses `nofail` and a
bounded device timeout. A subsequent controller run exceeded its SSH deadline
because of an unrelated VM configuration problem reported and corrected by the
operator. Verification resumed against the same persisted fixture after boot,
without recreating storage or manually assembling or mounting it. This is a
verified resumed acceptance result, not an uninterrupted harness run. Explicit
absence then removed the LV, VG, MD and member signatures; cleanup detached the
loops and removed their backing files and fixture service. The original fstab
was restored byte-for-byte. SELinux remained Enforcing and no systemd units
were failed. These results do not establish physical disk discovery behavior
or support for other distributions.

Reproduce storage checks only on an authorized disposable Linux target:

```sh
python3 tests/integration/typed_configs.py --binary ./bin/richt-linux-amd64 --work-root /var/tmp/richt-typed-results
python3 tests/integration/typed_configs.py --binary ./bin/richt --host richt-test-b --work-root /tmp/richt-typed-ssh
python3 tests/integration/typed_configs_boot.py --binary ./bin/richt --host richt-test-a --work-root /tmp/richt-typed-boot
```

The boot harness explicitly reboots its target and installs a temporary service
for reattaching its loop backing files and emitting scoped device-add events.
Its mount and data assertions remain
mandatory even though the test fstab entry uses `nofail`.

## Change-triggered oneshots — 2026-11-03

KRG-97 passed on three independent authorized AlmaLinux 10.2 x86_64 hosts
with SELinux Enforcing, DNF 4.20.0, RPM 4.19.1.1, systemd 257, and Python
3.12.13. One target ran the Linux binary locally as root; two targets used
the macOS controller over SSH with non-root logins and explicit `sudo -n`.

| Suite | Local Linux | SSH/sudo |
| --- | ---: | ---: |
| Oneshot triggers, execution, and saved continuation | 33 passed | 34 passed on each of two hosts |
| Files, SELinux, locking, and saved-plan regression | 17 passed | — |
| Systemd masks and ordered lifecycle | 24 passed | 24 passed |
| Config validators and ordered continuation | — | 19 passed |
| Secret-source example | 4 groups passed | — |

The [oneshot harness](#oneshot-process-acceptance) covered config creation,
content and attribute changes, merged references, unrelated and unchanged
inputs, literal argv, named and unnamed tasks, mixed systemd execution order,
and absence. Published `tree` RPM installation triggered a command without a
systemd daemon reload; matching installed state and package removal did not.
The sysctl check changed `vm.swappiness` from 30 to 10 through a prepared config
and native oneshot, then restored 30. No custom RPM fixtures were built.

A deliberate command failure after successful file and command tasks blocked
later work. Continuing the same saved plan after deleting inventory skipped
successful named and unnamed commands, preserved an external edit to the
completed config, retried the failed command, and unblocked the next service.
A fresh diff after the original config write scheduled no oneshot. Nonzero
exit, missing executable, timeout, and stdout/stderr canary checks passed.

Local `make check`, `make docs-check`, and race tests for `plan`, `engine`,
`oneshot`, and `transport` passed. Go tests also cover interrupted side-effect
repetition after progress persistence fails, malformed saved argv, strict
backend fields, SSH executable lookup without shell builtin/function dispatch,
and inspection with unavailable executables, templates, and secret sources.

The tested source was `93df8583f8d3` plus the KRG-97 working-tree changes.
Binary SHA-256 identities were:

| Binary | SHA-256 |
| --- | --- |
| Linux amd64 | `cf2c34dfc8c581166e078dd64afba5f4004cb605f565e8471ec9feee77d76c04` |
| macOS controller | `3aa6b91da099fb3085f1cc012fc613b1a3a74af6164cf20fb3d2adf2e937af4b` |

Private logs, executable plans, source snapshots, and JSON reports were retained
outside the tracked repository. Postflight confirmed restored sysctl values,
removed `tree` packages and temporary oneshot/mask units and files, SELinux
Enforcing, and unchanged prior PostgreSQL/exporter service state. Command
timeout coverage does not establish termination of all remote descendants.

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

This historical run predates saved format `1.7` and its atomic template writes
and final-symlink replacement contract. The file suite passed all **17 checks**: create/update/no-op/explicit absence,
root ownership, requested modes and SELinux labels, symlink behavior, missing
owner/group rejection, frozen saved inputs, the then-current in-place inode
preservation, sensitive displays, partial failure and manual continuation, terminal confirmation, and
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

The separate [typed-path driver](../tests/integration/typed_paths.py) covers
plain files and directories on an authorized disposable Linux target:

```sh
sudo python3 tests/integration/typed_paths.py --binary ./bin/richt-linux-amd64 \
  --output /root/richt-typed-paths-local
python3 tests/integration/typed_paths.py --binary ./bin/richt \
  --ssh-host deploy@typed-path-test.example --output /tmp/richt-typed-paths-ssh
```

It creates unique target paths and one temporary bind mount, then cleans them
in `finally`. Required target capabilities are root (or noninteractive sudo),
Python 3 for the controller, GNU stat/coreutils, find, mountpoint, mount, and
umount. Private output retains inventories, saved progress, command captures,
and `acceptance.json`. Checks include no-effect inspection/diff, exact creation
defaults under a restrictive controller umask, unmanaged content preservation,
nonrecursive attributes, newly appeared files, replacement identity protection,
named symlink removal, incompatible path types, reverse absent order, recursive
data deletion, and refusal to cross an active same-device bind mount. It installs
no packages and does not use fixed fixture services.

Run local unit and CLI checks with `go test ./...`. On an authorized disposable
Linux machine with SELinux Enforcing, run the file acceptance suite as root:

```sh
python3 tests/integration/files.py --binary ./bin/richt --work-root /root --target-root /var/lib
```

The suite allocates unique test directories beneath the specified existing
roots and removes only those directories afterwards. `--keep` retains them for
inspection. It verifies create/update/no changes/absent, ownership/mode/SELinux,
frozen saved inputs, atomic replacement, missing owner/group errors, final
symlink replacement without referent changes, partial progress and continuation,
sensitive display, actual
terminal confirmation, and concurrent apply locking. It does not install
packages or manage services.

The earlier 17-check suite, using pre-`1.7` in-place writes and final-symlink
following, passed on 2026-11-01 on a disposable AlmaLinux 10.2 x86_64 host
with SELinux Enforcing. This includes two independent YAML sources, multiline
secret bytes, private plan permissions, continuation after deleting sources,
requested target mode and SELinux label, and explicit sensitivity when replacing
old secret content. The suite removes its temporary directories after completion.
The later 2026-11-02 run and its retained directories are recorded above.

## RPM transaction exclusion overrides

KRG-99 validation on 2026-11-03 passed on disposable AlmaLinux 10.2 x86_64
with DNF/YUM 4.20.0, RPM 4.19.1.1, and SELinux Enforcing. The extended
[published RPM harness](../examples/rpm-versions/README.md) passed 29 checks in
local mode and 29 through SSH with a non-root login and explicit `sudo -n`.
The two modes ran sequentially against the same isolated target, with package
and repository cleanup between runs.

For each manager, exclusion tests cover unversioned, upstream VERSION, and
exact NEVRA installation requests. Omitting the override fails while the
repository excludes the package; a saved repository-ID or `all` override
installs it after the source inventory is deleted. Subsequent successful
continuation skips the task, override-only edits remain no-ops, and removal
retains the override. Repository bytes remain unchanged by transactions.
All RPMs are published Vmango packages; no custom fixture RPMs were built.

Go tests cover `main`, validation of malformed YAML/JSON, frozen values after
source changes, failure/continuation, local/SSH target binding, and absence of
process reactions for an override-only change. Live acceptance does not change
global DNF configuration to test `[main]` filters. The Kubernetes example is
schema-validated; these results do not claim Kubernetes cluster validation.

`make check`, focused engine race tests, and `make docs-check` passed. The
existing local file/SELinux acceptance suite also passed all 17 checks,
including saved inputs, progress, confirmation, and secret handling. The
package and repository fixtures were removed; private evidence includes plans,
command logs, binary SHA-256 values, platform details, and `acceptance.json`.

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

## Oneshot process acceptance

The focused [oneshot driver](../tests/integration/oneshot.py) exercises the native
command backend rather than expressing the command as a systemd unit. It uses
the existing Python standard-library acceptance pattern, private controller
artifacts, unique target paths, and one uniquely named systemd ordering probe.
Run only on an authorized disposable Linux target with root privileges,
Python 3, GNU coreutils, systemd, SELinux tooling, and a writable
`vm.swappiness` sysctl:

```sh
python3 tests/integration/oneshot.py --binary ./bin/richt-linux-amd64 \
  --work-root /root/richt-oneshot
# Or from a controller with trusted host keys and noninteractive sudo:
python3 tests/integration/oneshot.py --binary ./bin/richt \
  --host deploy@oneshot-test.example --work-root /tmp/richt-oneshot
```

It checks inspection and diff without execution; config creation, content and
attribute triggers; unrelated and unchanged inputs; merged actual trigger
references; mixed systemd/oneshot declaration order; no command on absence;
frozen argv after inventory removal; successful named and unnamed task skipping;
failure after config write; saved continuation versus a fresh diff; missing
executables; bounded timeout; and omission of stdout/stderr canaries from CLI
output and saved errors. A real sysctl command applies a unique config containing
a changed kernel value. The driver removes that file and restores the
original value in `finally`, including after failure.

Add `--package tree` to verify a real `binary.package` trigger using a published
RPM from the target's existing DNF 4 repositories. That package must be absent
before the run. The driver installs it, checks no-op behavior, removes it without
running the command during absence, and attempts package cleanup on failure.
It builds no fixture RPM. Omit this flag for file/command coverage only; the
JSON report records whether binary triggers were exercised.

`--work-root` retains inventories, executable plans/progress, captured displays,
platform facts, and `acceptance.json`. Keep that directory private. Without it,
the controller's temporary artifacts are removed. Fixtures are removed on exit;
an SSH timeout still does not guarantee remote process termination. This driver
is separate from the custom RPM fixture suites and does not require rpm-build
or createrepo_c. Running it establishes only the reported target and selected
checks; cross-compiling its binary alone does not establish runtime coverage.

## Artifact regression harness

The [artifact harness](../tests/integration/artifacts.py) exercises local Linux
execution through the Richt CLI with a private HTTPS fixture. Run it only as
root on an authorized disposable Linux host with Python 3.8+, OpenSSL, GNU
coreutils, util-linux (including permission to create a temporary bind mount),
and a Linux Richt binary:

```sh
python3 tests/integration/artifacts.py --binary ./bin/richt-linux-amd64 \
  --work-root /var/tmp/richt-artifact-results
```

It allocates unique private destinations and a temporary HTTPS server. Its
private test CA is trusted through `SSL_CERT_FILE` for its subprocesses; TLS
verification remains enabled. It installs no package and changes no existing
service. Plans, command captures, platform facts, binary SHA-256 and
`acceptance.json` remain in a unique child of `--work-root`; managed targets
are removed in cleanup. Without that option, controller evidence is removed.

The harness covers prepared/frozen requests, checksum verification, partial
HTTP responses, timeouts, TLS rejection, HTTPS redirect policy, no-op and
tracked drift repair, attribute changes, byte-level upgrade/reversion, oneshot
reactions, continuation, safe archive extraction, rejected traversal/symlink
escapes/hardlinks/special entries/duplicates, exclusive ownership, unknown
archive entries, and ordered resource removal. It does not establish application
compatibility, readiness, distribution portability, disk-full behavior, or every
possible power-loss boundary. The [node_exporter walkthrough](../examples/node-exporter/README.md)
provides a published-artifact/systemd/SSH scenario separately from these controlled
fixtures. The existence or syntax check of a harness is not evidence of a
passed runtime acceptance run; dated results are recorded separately.

The [published artifact SSH driver](../tests/integration/artifacts_ssh.py)
exercises real GitHub releases through SSH and explicit noninteractive sudo:

```sh
python3 tests/integration/artifacts_ssh.py --binary ./bin/richt \
  --host deploy@artifact-test.example --work-root /tmp/richt-artifact-ssh
```

The target needs Linux amd64, systemd, the artifact backend dependencies, HTTPS
access to GitHub, and unused loopback ports 19183 and 19184. The driver checks
unique fixture paths before creating them and removes its paths and units on
exit. It retains private controller evidence. Add `--archive-only` to run only
node_exporter. The full run also uses K3s **only as a kubectl proxy** against an
unavailable API endpoint, with isolated data/config paths. It never creates a
Kubernetes cluster and does not establish cluster upgrade/downgrade safety.

### Recorded artifact acceptance — 2026-11-03

The KRG-82/KRG-83 implementation was tested on AlmaLinux 10.2 x86_64 with
Python 3.12.13, systemd 257, explicit root/sudo execution and SELinux Enforcing.
Builds used base revision `d45425f136b3277d7384d86fd1076eb84b6f939c` plus the
feature changes recorded with this report.

| Suite | Result |
| --- | --- |
| Local artifact CLI/HTTPS harness | 43 checks passed, including real symlink and bind-mount ownership aliases with service filtering |
| Target Python helper tests on Linux | 13 passed, including concurrent locking, transaction recovery and hostile archive metadata |
| Published artifacts over SSH/sudo | 46 passed: node_exporter 1.10.1 → 1.10.2 → 1.10.1 and K3s 1.34.2+k3s1 → 1.34.3+k3s1 → 1.34.2+k3s1 proxy lifecycle |
| Final archive SSH/sudo rerun after stronger ownership preflight | 23 passed, including metrics, exact restart counts, frozen inputs, unknown-file protection and removal |
| Existing file/SELinux, secrets and systemd-mask regressions | 17 checks, 4 groups and 24 checks passed respectively |
| Published RPM/oneshot regression (`oneshot.py --package tree`) | 33 passed, including RPM install/trigger/no-op/removal, mixed process ordering and continuation; no custom fixture RPMs used |
| Repository verification | `make check` and `go test -race ./artifact ./engine ./plan ./transport ./inventory` passed |

The final local Linux binary SHA-256 was
`9fd12bc8934be33097882db1b7b93d73de2152237b20a621bef3dfe91443d53e`;
the final SSH controller binary was
`720e6a92664defe735171e9202485abed49263871787339387365770e6e44b9c`.
The earlier full published-artifact run used controller SHA-256
`a021c6d09f08037b73482a01414d3a881ae16f3a106a9d81f0a4e76f36b356bd`.
Private reports retain commands, plans/progress, platform facts and binary
identities. Fixture destinations, temporary bind mounts and owned service units
were removed; pre-existing workloads remained active, with SELinux Enforcing
and no failed units. These runs do not establish other distribution/architecture
support, K3s cluster compatibility, disk-full recovery or exhaustive power-loss
recovery.

## Atomic config write acceptance

The [prepared file driver](../tests/integration/config_writes.py) verifies the
version 1.7 template and owned-file publication contract. It requires an
authorized disposable Linux target, root or explicit noninteractive sudo,
Python 3.8+, ACL tools, and SELinux Enforcing. Only unique fixture paths are
changed; private inventories, plans, command logs and `acceptance.json` remain
under the chosen controller evidence root.

```sh
python3 tests/integration/config_writes.py --binary ./bin/richt-linux-amd64 \
  --work-root /root
python3 tests/integration/config_writes.py --binary ./bin/richt \
  --host deploy@file-test.example --work-root /tmp
```

### Recorded config write acceptance — 2026-11-06

KRG-103 passed on AlmaLinux 10.2 x86_64 with Python 3.12.13, DNF 4.20.0,
systemd 257, and SELinux Enforcing. Builds used base revision
`cc0231f5370d5de4dbc207e562dda4f75ccd1d3e` plus the feature changes.

| Suite | Result |
| --- | --- |
| Local prepared file driver | 40 checks passed |
| SSH with non-root login and explicit sudo | 41 checks passed |
| Existing local file/SELinux, terminal and secret regression | 18 checks passed |
| Existing local validator regression, including real sshd syntax checking | 18 checks passed |
| Published RPM and oneshot/systemd regression | 33 checks passed with the published `tree` package; no fixture RPMs built |
| Helper failure injection | 13 tests passed, including backup/receipt publication, rename, removal and directory sync boundaries |
| Repository verification | `make check`, relevant race tests and `make docs-check` passed |

The prepared file driver covers atomic defaults, explicit in-place writes,
creation/replacement/removal, content/type/presence/link conflicts, identical-byte
inode replacement, live attribute drift, final and dangling symlinks, hardlinks,
independent backups, frozen identities and paths, continuation, validators,
backup tampering/collision and old-version refusal. Real ACLs, user extended
attributes, ownership, permissions and SELinux labels were verified on the
target and backup. A Go boundary test also injects loss of the final progress
write after publication and verifies safe saved continuation.

The final Linux binary SHA-256 was
`d44fc6821886c3ed582b656021611651e9db8f58cf3b271df6920b585a9f63e3`;
the final SSH controller binary was
`6a318f9be418b54988581576deadb330b020a0cec0dd97dd14a1bea33d593a4b`.
Managed fixtures, the temporary package and systemd probe were removed;
`vm.swappiness` returned to 30. The existing exporter remained active, the
system was running with no failed units, and SELinux remained Enforcing.
The ACL prerequisite package remains installed. Private evidence retains the
plans and logs. This does not establish other platforms, compare-and-swap
against concurrent external writers, exhaustive power-loss recovery or a
rollback command.
