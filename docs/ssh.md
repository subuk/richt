# SSH, sudo, and parallel apply

Richt uses the system OpenSSH `ssh` executable. Existing `ssh_config` aliases,
identity files, SSH agent, proxy/jump settings, and known-hosts policy remain in
effect. The controller needs OpenSSH in `PATH`; remote targets need the Linux
utilities used by the selected resources (GNU coreutils/findutils, `getent`,
and, when requested, DNF 4/YUM 4, systemd, or SELinux tools). No Richt daemon or
installed Richt binary is needed on an SSH target.

```yaml
hosts:
  - name: application-a
    transport:
      type: ssh
      host: app-a             # address or existing ssh_config alias
      user: deploy           # optional; otherwise use SSH configuration
      port: 2222             # optional; otherwise use SSH configuration
      sudo: true             # explicit; never enabled implicitly
    services:
      - {name: application, order: 10}
```

`host` accepts a hostname, address, or SSH alias; use the separate `user` field
instead of `user@host` or an SSH URI. Explicit `host`, `user`, and `port` select
the destination and override client
defaults. SSH configuration is read on every invocation. Saved plans keep these
explicit inputs and the `sudo` flag, not a snapshot of resolved SSH options,
keys, or passwords. Changes to `ssh_config` between diff and apply are allowed,
including changes to the address behind an alias. Review the alias, explicit
overrides, and privilege mode displayed before applying a plan.

SSH always runs in batch mode without a terminal. Richt does not prompt for
login passwords, key passphrases, or host-key confirmation. Configure keys or
load them into your agent beforehand, and establish host trust using your normal
SSH workflow. The normal client host-key policy remains effective; Richt never
disables verification. Authentication or trust failures fail the affected host.
Client settings that replace the requested command, disable its session, or
discard its stdin are overridden so the prepared operation can execute.

When `sudo: true`, target commands and file operations use `sudo -n`. Configure
noninteractive privilege access for the required commands before preparing the
plan. A password-required sudo policy fails rather than opening a prompt.
`sudo: false` leaves the login user's privileges unchanged. Local transport also
accepts explicit sudo. `apply -y` skips only Richt's plan confirmation; it does
not enable interactive authentication or change privileges.

Files are read on the target during diff. Prepared content is sent through SSH
stdin during apply, including for privileged writes; file content is never put
into command arguments. Writes follow ordinary target and parent symlinks, and
removal unlinks the named file or symlink. Owner/group names are resolved on the
target at observation and again when attributes are applied. Changed numeric
IDs are allowed. These rules also apply to local files. The saved file bytes,
resource requests, task order, and process branches remain fixed; apply does
not reread inventory/templates or recalculate changes.

## Parallel execution and continuation

```sh
./bin/richt diff -i examples/ssh/inventory.yaml -o /tmp/richt-plan.json
./bin/richt apply -y --parallelism 2 /tmp/richt-plan.json
# Continue unfinished tasks, with a different limit:
./bin/richt apply -y --parallelism 1 /tmp/richt-plan.json
```

Each apply handles at most 20 hosts concurrently by default. `--parallelism N`
sets a positive limit for that run; `1` runs sequentially. The limit is not part
of the saved plan and does not select hosts or alter tasks. Inventory/host/tag/
service filters remain forbidden when applying a saved plan. Local and SSH
hosts can share one plan.

Services and tasks remain sequential within each host. A task failure blocks
the rest of its host; other active and queued hosts continue. All hosts finish
before the final ordered summary, and any failure produces a nonzero exit.
Explicit continuation skips successful tasks and retries unfinished tasks.
There are no automatic retries or rollback.

All task progress changes and atomic plan saves are synchronized. The adjacent
lock still prevents two processes applying the same saved plan. A save failure
stops admission of new actions globally. Already admitted actions are awaited
and may have taken effect; interrupted unfinished tasks can run again on an
explicit continuation. SSH client cancellation cannot guarantee that a command
already accepted by the remote host stopped.

## Disposable acceptance

Use two isolated Linux SSH targets with their own RPM database, filesystem, and
systemd instance. Both need an SSH alias, a non-root login with noninteractive
sudo, and the fixture repository produced by
[`build-repository.sh`](../examples/rpm-systemd/build-repository.sh) at
`/var/lib/richt-demo-fixture/repo`. The test manages only its fixture resources,
but installs/removes packages and services: use disposable targets.

The [SSH harness](../tests/integration/ssh.py) records plans, command output, and
an acceptance report. It also creates and removes a unique test user/group to
verify ownership changes between preparation and apply. This stage needs the
target shadow-utils commands (`useradd`, `usermod`, `groupadd`, `groupmod`,
`userdel`, and `groupdel`). See its `--help` for target and output options. The local
[file harness](files.md) and [service harness](services.md) remain regression
checks; concurrency and persistence failures also have Go race-detector tests.

```sh
go test -race ./plan ./engine ./config ./transport ./cli
python3 tests/integration/ssh.py --binary ./bin/richt \
  --host-a richt-test-a --host-b richt-test-b \
  --work-root /tmp/richt-ssh-acceptance
```

Verified on 2026-11-01 with two isolated AlmaLinux 10.2 x86_64 system containers,
each running its own SSH server, systemd 257, filesystem, and DNF 4.20 RPM
database, on a disposable SELinux-Enforcing Linux host. The 26 SSH harness
checks passed with non-root logins, SSH-agent authentication, and noninteractive
sudo. The 14 local file and 29 local RPM/systemd checks also passed.

Additional checks verified changed live SSH aliases, unknown/changed host-key
rejection, password-required sudo failure, conflicting SSH session options, and
local named UID/GID changes after preparation. A macOS controller prepared and
applied a sensitive file with an explicit SELinux label on the Linux host, then
observed no changes. Container isolation is not evidence of a separate kernel;
SELinux file behavior was verified on the enforcing host itself.
