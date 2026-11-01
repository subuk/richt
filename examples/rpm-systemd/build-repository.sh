#!/bin/sh
# Run on an isolated DNF 4 Linux machine with rpm-build and createrepo_c.
set -eu
root=${1:-/var/lib/richt-demo-fixture}
mkdir -p "$root/build/SPECS" "$root/build/SOURCES" "$root/repo"
for version in 1.0 2.0; do
cat > "$root/build/SPECS/richt-demo.spec" <<SPEC
Name: richt-demo
Version: $version
Release: 1
Summary: Reproducible Richt lifecycle acceptance fixture
License: MIT
BuildArch: noarch
Requires: /bin/bash

%description
Small process for testing explicit RPM versions and systemd lifecycle behavior.

%install
mkdir -p %{buildroot}/usr/libexec %{buildroot}/usr/lib/systemd/system
cat > %{buildroot}/usr/libexec/richt-demo <<'PROGRAM'
#!/bin/bash
mkdir -p /run/richt-demo
printf 'start $version\\n' >> /run/richt-demo/events
trap 'printf "reload\\n" >> /run/richt-demo/events' HUP
trap 'exit 0' TERM INT
while :; do sleep 0.1; done
PROGRAM
chmod 0755 %{buildroot}/usr/libexec/richt-demo
cat > %{buildroot}/usr/lib/systemd/system/richt-demo.service <<'UNIT'
[Unit]
Description=Richt demonstration process
StartLimitIntervalSec=0
[Service]
Type=simple
ExecStart=/usr/libexec/richt-demo
ExecReload=/bin/kill -HUP \$MAINPID
[Install]
WantedBy=multi-user.target
UNIT

%files
/usr/libexec/richt-demo
/usr/lib/systemd/system/richt-demo.service
SPEC
rpmbuild --define "_topdir $root/build" -bb "$root/build/SPECS/richt-demo.spec"
cp "$root/build/RPMS/noarch/richt-demo-$version-1.noarch.rpm" "$root/repo/"
done
createrepo_c "$root/repo"
printf 'Repository ready: file://%s/repo\n' "$root"
