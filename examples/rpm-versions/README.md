# Upstream RPM versions

This package-only example compares the three RPM version policies using
published Vmango packages. Use a disposable AlmaLinux 10 x86_64 host with root
and DNF 4, following the dependency repository prerequisites in the
[Vmango walkthrough](../vmango/README.md). It owns the `vmango` package and
`/etc/yum.repos.d/richt-vmango.repo`; start without an existing Vmango
installation or that repository file. It does not configure or manage a
process. The shared repository service runs before the package request.

Run from the repository root with a built Linux `bin/richt`:

```sh
bin/richt inventory validate -i examples/rpm-versions/inventory.yaml
bin/richt diff -i examples/rpm-versions/inventory.yaml -o /root/rpm-upstream.json
bin/richt apply -y /root/rpm-upstream.json
rpm -q vmango --qf '%{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH}\n'
bin/richt diff -i examples/rpm-versions/inventory.yaml
```

The request saves `version: "0.16.0"`, without resolving repositories at diff.
The displayed action explains that apply resolves the missing components.
During apply, Richt runs `dnf -y install -- vmango-0.16.0`; DNF resolves the
omitted epoch, release, and architecture using its normal package-spec and
transaction policy. Repository contents can change between diff and apply;
an unavailable VERSION fails at apply.
The plan keeps its original short request after execution.

The next diff has no package task for any installed epoch/release/architecture
whose VERSION is exactly `0.16.0`. A VERSION such as `0.16.01` does not match.
Edit the version to another available published upstream VERSION, prepare a
new plan, and apply it to upgrade or downgrade. Published versions can
disappear; inspect the repository before choosing a pair. This example does
not claim that every historical release remains available.

For an exact request, replace the version with the complete installed or
available value, for example `"0:0.16.0-1.el10.x86_64"`. Epoch, release, and
architecture now participate in the comparison. Removing `version` instead
preserves any installed version and installs only if the package is absent.
Partial forms and user-supplied globs are rejected. See the
[RPM contract](../../docs/services/binaries/rpm.md) for command and saved-input
boundaries.

For cleanup, set both tag assignments to `{state: absent}`, prepare and
review a new removal plan, and apply it. Richt removes the declared package
and repository file; generated application state and package-manager side
effects are not purged. Restore the example to its original present state
and short version before another run.

For repeatable local acceptance on an authorized disposable host with the same
prerequisites, run from the repository root:

```sh
python3 examples/rpm-versions/verify.py bin/richt-linux-amd64 /root/richt-rpm-versions-acceptance
```

The harness
requires its acceptance repository and Vmango to be absent, exercises published
install/upgrade/downgrade, no-op, omitted and exact requests, unavailable-version
failure, saved inputs and continuation, then removes the declared package and
repository. It retains command logs and `acceptance.json` in the new work
directory. A failure leaves evidence and managed resources for diagnosis.

Add `--disable-excludes` to exercise a repository with `excludepkgs=vmango*`.
For both DNF 4 and YUM 4, the harness checks omitted-override failures followed
by successful unversioned, upstream, and exact installations using saved
overrides after deleting the inventory. It checks successful continuation,
override-only no-op, removal with the override, and unchanged repository bytes.
Live scopes are the repository ID and `all`; `main` is covered by Go argument
tests without changing the host's global DNF configuration.

```sh
python3 examples/rpm-versions/verify.py bin/richt-linux-amd64 /root/richt-rpm-excludes \
  --disable-excludes --source-revision YOUR_REVISION
# On a controller with a native Richt binary and trusted SSH host:
python3 examples/rpm-versions/verify.py bin/richt /tmp/richt-rpm-excludes-ssh \
  --disable-excludes --host deploy@rpm-test.example --source-revision YOUR_REVISION
```

SSH mode uses the target's RPM database and repository files through explicit
`sudo -n`, while the controller retains plans and logs. Use a fresh evidence
directory for each run. Serialize runs against the same target: different work
directories do not isolate the fixed package and repository names.

To additionally verify mixed named and unnamed binary order, supply three
published packages that are available in the target's enabled repositories and
are not installed. For example, on a prepared disposable AlmaLinux host:

```sh
python3 examples/rpm-versions/verify.py bin/richt-linux-amd64 /root/richt-rpm-ordered \
  --ordered-packages tree ed lsof
```

The harness checks declaration order in the saved install plan, successful
installation of all four packages, reverse binary-list order during absence,
and removal of the added packages. It rejects preexisting test packages.

Validation on 2026-11-02 passed on AlmaLinux 10.2 x86_64 with DNF/YUM 4.20.0,
RPM 4.19.1.1, and SELinux Enforcing. The local harness passed all 10 checks;
separate SSH with non-root login and `sudo -n` checks passed short install,
YUM upgrade, DNF downgrade, no-op, unavailable-version failure, saved-input
continuation, and cleanup. All packages used were published Vmango RPMs.
