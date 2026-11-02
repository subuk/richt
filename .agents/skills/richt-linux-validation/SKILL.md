---
name: richt-linux-validation
description: Validate Richt file, SELinux, RPM, and systemd behavior using its existing acceptance harnesses on an authorized disposable Linux host.
---

# Richt Linux validation

Read [AGENTS.md](../../../AGENTS.md), then the affected contract in
[files](../../../docs/services/configs/files.md) or
[services](../../../docs/services/_index.md), and the
[validation harness instructions](../../../docs/validation.md).
Prefer extending the checked-in harnesses when new behavior needs coverage;
avoid duplicating their acceptance workflows.
This skill does not grant host access or authority to install packages.

## Local test-host context

Read [local test hosts](references/hosts.local.md), when present, before asking
for a test host. It records this workspace owner's reusable disposable hosts,
SSH destinations, authorization scope, and retained fixtures. When the user
says to use the same test boxes, use those recorded hosts within the current
task's authorization; do not ask for their addresses again. Keep renewed host
instructions in that reference without treating them as a fresh live check.
The file is local and gitignored; keep host addresses out of public examples
and acceptance reports.

## Prepare the environment

Run `make check` locally for code changes and `make linux-amd64` when a Linux
amd64 binary is needed. Match the target architecture; do not report a local
cross-build as Linux execution coverage. Transfer the built binary and required
repository fixtures using an already authorized connection. Record the source
revision and any uncommitted changes, binary identity, and test command.

Use an authorized disposable Linux machine with the required systemd and SELinux
facilities. Prefer a snapshot-capable VM for package/service acceptance so its
state can be restored. Check `/etc/os-release`, `uname -m`,
effective UID, `getenforce`, the active systemd environment, and relevant tool
versions before running. The documented verified platform is AlmaLinux 10.2
x86_64 with DNF4/YUM4, systemd, and SELinux Enforcing; record actual versions
rather than assuming another host matches. File acceptance requires root and
SELinux Enforcing. Package/service acceptance also needs `rpm-build`,
`createrepo_c`, and Python 3; install missing prerequisites only within the
authorized disposable-host scope. Do not disable SELinux to make a test pass.

Coordinate host ownership before mutating fixtures. The RPM/systemd harness
uses fixed package, repository, unit, `/etc`, `/run`, and `/var/lib` names.
Different work directories do **not** isolate concurrent runs. Serialize runs
using that shared namespace or use separate VMs. Check for another worker and
preexisting resources; do not overwrite unrelated resources merely because they
have a fixture-like name.

## Run the relevant harness

Execute from the repository root on the target with its Linux binary:

```sh
python3 tests/integration/files.py --binary ./bin/richt-linux-amd64 --work-root /root --target-root /var/lib --keep
python3 examples/rpm-systemd/verify.py ./bin/richt-linux-amd64 /root/richt-service-test
```

For SSH/sudo changes, also use two isolated targets with their own filesystem,
RPM database, and systemd instance. The controller needs the system SSH client,
trusted host keys, and noninteractive credentials; non-root target users need
`sudo -n` access. See [SSH acceptance](../../../docs/ssh.md) and run:

```sh
python3 tests/integration/ssh.py --binary ./bin/richt \
  --host-a richt-test-a --host-b richt-test-b \
  --work-root /tmp/richt-ssh-acceptance
```

The harness uses the existing RPM repository fixture, retains artifacts, and
creates/removes a unique test user/group to verify changed IDs between diff and
apply. It does not provision targets or grant access to them. Isolated system
containers can supply separate SSH/systemd targets on an authorized disposable
host; do not represent two aliases to the same OS instance as isolated targets.

Run the suite relevant to the change; run both local suites for shared preparation, plan,
execution, or progress changes. The file suite allocates unique directories
under existing roots. `--keep` preserves them for diagnosis; without it, its
`finally` block removes them even after failure. Capture stdout/stderr and the
exit status outside those temporary directories. Remove retained directories
after review when appropriate.

The service harness deliberately installs/removes `richt-demo`, changes named
test units, and resets fixture resources at startup. It retains inventories,
plans, `.diff`, `.apply`, and `acceptance.json` under its work directory, plus
application event evidence on the host. Preserve those results before rerunning
or restoring the VM. A new work directory keeps result files separate but does
not isolate host resources. See the service guide for coverage and fixture
details.

## Diagnose and report precisely

Separate an environment/precondition failure from a product regression or a
harness assumption. Inspect the exact failed assertion/command, its exit status,
displayed diff, prepared task payload, and saved progress before changing code.
For process failures, inspect `systemctl show` and the relevant journal; for
package failures, record DNF/YUM and RPM results; for SELinux failures, inspect
labels and relevant AVC evidence. Keep SELinux mode and platform facts in the
report. A timeout can leave an accepted systemd job running, so inspect its
state before any retry.

Do not repeatedly rerun a mutating suite against unexplained state. Preserve
evidence, fix the identified cause or restore the disposable VM, then rerun the
affected verification. Report the source/binary tested, actual platform,
commands, passed checks, failures, evidence location, and cleanup or retained
state. Exclude credentials and sensitive plan contents from reports. Include
target identity only within the authorized destination and scope.
