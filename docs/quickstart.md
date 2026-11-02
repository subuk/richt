---
title: "Config-only quickstart"
linkTitle: "Quickstart"
description: "Create, review, apply, update, and remove a templated file without root privileges."
weight: 10
---

This walkthrough uses one local host and a config-only service. Run it from the
repository root on Linux with Go 1.27.3 or newer, Make, and standard shell tools.
It creates all managed targets in a fresh private temporary directory and needs
no root, DNF, systemd, or network access after building. Full Linux platform
requirements and verification scope are in [installation](installation.md) and
[validation](validation.md).

## Build and describe the file

```sh
make build
./bin/richt --version

demo_dir=$(mktemp -d "${TMPDIR:-/tmp}/richt-quickstart.XXXXXX")
chmod 700 "$demo_dir"
printf '%s\n' 'message={{ .greeting }}' > "$demo_dir/greeting.gotmpl"
cat > "$demo_dir/inventory.yaml" <<EOF_INVENTORY
tags:
  defaults:
    order: 10
    services:
      greeting: {state: present}
    vars:
      greeting: Hello from Richt
services:
  greeting:
    order: 10
    configs:
      main:
        path: $demo_dir/target/greeting.conf
        mode: "0644"
        template: {file: greeting.gotmpl}
hosts:
  - name: local
    tags: [defaults]
    transport: {type: local}
EOF_INVENTORY
```

Keep this shell open so `demo_dir` stays available. Tag `defaults` assigns the
service and supplies the template's `.greeting` variable. The service
definition's `order` fixes execution order;
config-only services need no binary or process component. Template paths are
relative to their declaring service file, or the inventory for inline services.

## Inspect, prepare, and apply

```sh
./bin/richt inventory validate -i "$demo_dir/inventory.yaml"
./bin/richt inventory show -i "$demo_dir/inventory.yaml"
./bin/richt diff -i "$demo_dir/inventory.yaml" -o "$demo_dir/create.json"
./bin/richt apply "$demo_dir/create.json"
cat "$demo_dir/target/greeting.conf"
./bin/richt diff -i "$demo_dir/inventory.yaml"
```

Inventory inspection does not read templates or contact the host. Diff renders
the template and observes the target, then displays the planned file write and
attributes. It does not change managed resources. Review the display and type
`yes` at apply's terminal prompt. The resulting file contains
`message=Hello from Richt`; the final diff reports no changes.

In a noninteractive shell, use `./bin/richt apply -y "$demo_dir/create.json"`
when you intend to skip confirmation. Piping `yes` into apply does not satisfy
the terminal requirement. The saved JSON contains executable prepared inputs
and task progress; apply does not reread the source files or recalculate the
diff. Keep saved plans private, especially when templates use secrets.

## Update and explicitly remove

```sh
printf '%s\n' 'message={{ .greeting }} (updated)' > "$demo_dir/greeting.gotmpl"
./bin/richt diff -i "$demo_dir/inventory.yaml" -o "$demo_dir/update.json"
./bin/richt apply "$demo_dir/update.json"
cat "$demo_dir/target/greeting.conf"

sed 's/state: present/state: absent/' "$demo_dir/inventory.yaml" > "$demo_dir/remove.yaml"
./bin/richt diff -i "$demo_dir/remove.yaml" -o "$demo_dir/remove.json"
./bin/richt apply "$demo_dir/remove.json"
test ! -e "$demo_dir/target/greeting.conf"
```

The update changes the file to `message=Hello from Richt (updated)`. Explicit
`state: absent` removes the declared file. Deleting an assignment alone does
not request removal. Removal does not render templates or read secret sources,
and leaves the parent directory in place.

To remove this walkthrough's remaining inputs and plans after reviewing them:

```sh
rm -rf -- "$demo_dir"
```

Continue with [inventory rules](inventory.md), the [ready-package vmango
example](../examples/vmango/README.md), [SSH](ssh.md), or the [two-source secret
example](../examples/secrets/README.md). To see a partial failure and retry only
unfinished work, use the [continuation walkthrough](continuation.md).
