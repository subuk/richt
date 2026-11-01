# Files and saved plans

Build with `make build`. The example writes `/var/tmp/richt-example/greeting.conf` on the local machine:

```console
bin/richt inventory validate -i examples/files/inventory.yaml
bin/richt diff -i examples/files/inventory.yaml
bin/richt diff -i examples/files/inventory.yaml -o /var/tmp/richt-example-plan.json
bin/richt apply /var/tmp/richt-example-plan.json
bin/richt apply -y /var/tmp/richt-example-plan.json
bin/richt apply -y -i examples/files/inventory.yaml
```

`diff` reads and compares files without changing managed resources. It renders Go text/templates with the selected host's resolved variables, and missing variables fail preparation. Template paths are relative to the service definition file. Change `greeting` in the example to see an update; run a second diff after apply to see no changes. Set the assigned service's `state: absent` and build a new plan to explicitly remove its managed file.

A saved plan contains rendered text, unified patches, requested owner/group names, file attributes, and a progress entry per task. It stays executable after the source template or inventory changes or disappears. Apply uses those exact inputs, writes the target file, and does not compare current file contents against the old diff. It creates necessary parent directories. Ordinary file access follows target and parent symlinks; removal unlinks the named file or symlink. Users and groups named by `owner` and `group` must exist on the target. Names are resolved during observation and again when applying attributes; changed numeric IDs between diff and apply are allowed. Unspecified ownership and permissions of existing files are preserved; new files default to mode 0644. Specify modes as quoted octal strings.

Set `selinux: system_u:object_r:etc_t:s0` on a config to manage its full SELinux label on an SELinux-enabled Linux machine. The kernel must accept the label. The label is compared during diff and applied through the open target file descriptor. SELinux requests fail explicitly on other systems.

Without `-y`, apply requires a terminal and explicit `y` or `yes`; empty or negative answers cancel. Piped input does not count as a terminal. `-y` only skips confirmation. Host/tag/service filters and `-i` are accepted while building a plan and rejected when applying a saved plan.

Saved plan progress is written atomically before and after each task. The stable adjacent `.lock` file prevents simultaneous applies even while the JSON file is replaced. Plan and lock files use restrictive permissions. Keep the sidecar file; its existence alone does not mean an apply is running. Parent-directory aliases are resolved for locking. Hardlink aliases of saved plans are rejected. If a task fails, later tasks on that host are blocked and other hosts continue. Run `apply` explicitly again to retry unfinished tasks; successful tasks are skipped. Interrupted tasks can run again. There are no automatic retries or rollback. Hosts execute concurrently, with a default limit of 20. Use `--parallelism N` on every apply, including a saved plan; `1` executes sequentially. Tasks within each host remain ordered. A progress write failure stops admission of further actions globally; already running operations are awaited and may have taken effect. Without a plan file, progress exists only in memory and no plan is automatically saved.

Use `sensitive: true` to hide the entire old/new file diff, including context, and to hide file operation error details. Saved JSON still contains the real rendered text and patch, so protect it as sensitive data. Templates using `secret` fail explicitly until secret backends are implemented. The examples contain no real secrets.

New plans use strict JSON schema version `1.1`. Legacy local `1.0` plans remain accepted with their numeric ownership fields; new connection and named-ownership fields require `1.1`. Unknown fields, incompatible versions, invalid operations, and malformed progress are rejected. Plans are executable inputs: inspect their displayed changes and trust their source before confirmation. The file executor supports local and SSH targets, with optional noninteractive sudo. Explicit connection settings are stored in the plan; the system SSH client reads its current configuration on each invocation. See [SSH and parallel apply](ssh.md). Unsupported transports and backends fail before changes. Results show completed, previously completed, failed, and blocked tasks per host, with the saved plan path for manual continuation.

Run local unit and CLI checks with `go test ./...`. On a disposable Linux machine with SELinux Enforcing, run the file acceptance suite as root:

```console
python3 tests/integration/files.py --binary ./bin/richt --work-root /root --target-root /var/lib
```

The suite allocates unique test directories beneath the specified existing roots and removes only those directories afterwards. `--keep` retains them for inspection. It verifies create/update/no changes/absent, ownership/mode/SELinux, frozen saved inputs, inode preservation, missing owner/group errors and ordinary symlink writes, partial progress and continuation, sensitive display, actual terminal confirmation, and concurrent apply locking. It does not install packages or manage services.
