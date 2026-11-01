---
title: "Linux validation"
description: "Recorded Linux acceptance results, environment, commands, and limits."
weight: 120
---

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
