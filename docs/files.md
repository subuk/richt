---
title: "Files and saved plans"
description: "Render templates, manage file attributes, and execute protected JSON plans."
weight: 50
---

Build with `make build`. The example writes `/var/tmp/richt-example/greeting.conf` on the local machine:

```console
bin/richt inventory validate -i examples/files/inventory.yaml
bin/richt diff -i examples/files/inventory.yaml
bin/richt diff -i examples/files/inventory.yaml -o /var/tmp/richt-example-plan.json
bin/richt apply /var/tmp/richt-example-plan.json
bin/richt apply -y /var/tmp/richt-example-plan.json
bin/richt apply -y -i examples/files/inventory.yaml
```

`diff` reads and compares files without changing managed resources. It renders Go text/templates with the selected host's resolved variables, and missing variables fail preparation. Template paths are relative to the service definition file. The `secret` function reads a string from a local YAML source and automatically marks the file sensitive. Standard Go template built-ins remain available, including `printf`, `index`, and `slice`. Parse and execution failures use generic diagnostics for every template so errors cannot quote template text or transformed secret values. Change `greeting` in the example to see an update; run a second diff after apply to see no changes. Set the assigned service's `state: absent` and build a new plan to explicitly remove its managed file; removal does not open templates or secret sources.

A saved plan contains rendered text, unified patches, requested owner/group names, file attributes, and a progress entry per task. It stays executable after the source template, inventory, or secret source changes or disappears. Apply uses those exact inputs, writes the target file, and does not compare current file contents against the old diff. It creates necessary parent directories. Ordinary file access follows target and parent symlinks; removal unlinks the named file or symlink. Users and groups named by `owner` and `group` must exist on the target. Names are resolved during observation and again when applying attributes; changed numeric IDs between diff and apply are allowed. Unspecified ownership and permissions of existing files are preserved; new files default to mode 0644. Specify modes as quoted octal strings.

Set `selinux: system_u:object_r:etc_t:s0` on a config to manage its full SELinux label on an SELinux-enabled Linux machine. The kernel must accept the label. The label is compared during diff and applied through the open target file descriptor. SELinux requests fail explicitly on other systems.

Without `-y`, apply requires a terminal and explicit `y` or `yes`; empty or negative answers cancel. Piped input does not count as a terminal. `-y` only skips confirmation. Host/tag/service filters and `-i` are accepted while building a plan and rejected when applying a saved plan.

Saved plan progress is written atomically before and after each task. The stable adjacent `.lock` file prevents simultaneous applies even while the JSON file is replaced. Plan and lock files use restrictive permissions. Keep the sidecar file; its existence alone does not mean an apply is running. Parent-directory aliases are resolved for locking. Hardlink aliases of saved plans are rejected. If a task fails, later tasks on that host are blocked and other hosts continue. Run `apply` explicitly again to retry unfinished tasks; successful tasks are skipped. Interrupted tasks can run again. There are no automatic retries or rollback. Hosts execute concurrently, with a default limit of 20. Use `--parallelism N` on every apply, including a saved plan; `1` executes sequentially. Tasks within each host remain ordered. A progress write failure stops admission of further actions globally; already running operations are awaited and may have taken effect. Without a plan file, progress exists only in memory and no plan is automatically saved.

Calling `secret` automatically marks the file sensitive; `sensitive: false` cannot override this. Use `sensitive: true` when old bytes also need protection after removing a secret call or declaring a service absent. There is no persistent secret history, and absent services do not render templates. Sensitive displays hide the entire old/new file diff, including context, and file operation error details. Actual bytes still determine changes and process reactions. These rules include owned units and drop-ins.

Saved JSON still contains the real rendered text and patch, including old secret bytes, so protect it as sensitive data. Plans, adjacent locks, and atomic plan replacements use mode 0600. Restrict access to the containing directory and do not publish plans or command captures as harmless artifacts. Sensitivity controls display, not target file permissions: new targets still default to mode 0644, so specify `mode: "0600"` for private files. The examples contain only fictional values. See [YAML secrets and sensitive files](secrets.md) and the [isolated example](../examples/secrets/README.md).

New plans use strict JSON schema version `1.1`. Legacy local `1.0` plans remain accepted with their numeric ownership fields; new connection and named-ownership fields require `1.1`. Unknown fields, incompatible versions, invalid operations, and malformed progress are rejected. Plans are executable inputs: inspect their displayed changes and trust their source before confirmation. The file executor supports local and SSH targets, with optional noninteractive sudo. Explicit connection settings are stored in the plan; the system SSH client reads its current configuration on each invocation. See [SSH and parallel apply](ssh.md). Unsupported transports and backends fail before changes. Results show completed, previously completed, failed, and blocked tasks per host, with the saved plan path for manual continuation.

Run local unit and CLI checks with `go test ./...`. On a disposable Linux machine with SELinux Enforcing, run the file acceptance suite as root:

```console
python3 tests/integration/files.py --binary ./bin/richt --work-root /root --target-root /var/lib
```

The suite allocates unique test directories beneath the specified existing roots and removes only those directories afterwards. `--keep` retains them for inspection. It verifies create/update/no changes/absent, ownership/mode/SELinux, frozen saved inputs, inode preservation, missing owner/group errors and ordinary symlink writes, partial progress and continuation, sensitive display, actual terminal confirmation, and concurrent apply locking. It does not install packages or manage services.

All 17 checks passed on 2026-11-01 on a disposable AlmaLinux 10.2 x86_64 host
with SELinux Enforcing. This includes two independent YAML sources, multiline
secret bytes, private plan permissions, continuation after deleting sources,
requested target mode and SELinux label, and explicit sensitivity when replacing
old secret content. The suite removes its temporary directories after completion.
