# Two independent YAML secret sources

All values in this directory are fictional. The same `credential.password`
key exists in `default.yaml` and `production.yaml`; the template selects each
source independently and also formats a value and uses a multiline value.
Calling `secret` marks the file sensitive even though inventory specifies
`sensitive: false`. The target mode is explicitly 0600. The `example` tag assigns
the application; its execution order belongs to the service definition.

Run the isolated verifier from the repository root:

```sh
make build
python3 examples/secrets/verify.py ./bin/richt
```

The verifier copies these fixtures into a fresh private temporary directory and
changes the target path to stay inside it. It captures CLI output, checks that
no fictional values appear in that output, and prints only pass/fail messages.
It checks independent lookup, automatic sensitivity, private plan/lock/target
modes, a no-op diff, and apply after deleting both sources, the template, and
inventory. It also checks successful-task continuation and explicit-sensitive
removal without reopening missing template/source files. All temporary inputs,
plans, captures, and targets are removed on exit. It needs Python 3 and a local
Richt binary; it does not install packages, use sudo, or manage services.

The static inventory targets `/var/tmp/richt-secret-example/app.conf` when used
directly. Prefer the verifier for a walkthrough with automatic cleanup. For
real use, replace the fictional sources with private local files and keep the
inventory, sources, generated plans, and containing directories under suitable
access controls. Human-readable output is redacted; executable JSON retains
real old and new bytes. Do not share saved plans as harmless examples.

See [YAML secrets and sensitive files](../../docs/secrets.md) for the full
contract. This controller-side example does not establish Linux root, RPM,
systemd, SELinux, or SSH/sudo acceptance.
