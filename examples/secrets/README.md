# Independent YAML and environment secret sources

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

## Environment example

`env-inventory.yaml` declares an `env` source with a flat mapping from secret
keys to controller environment variable names. Its default source and its
`yaml` source both define `credential.password`; the template explicitly selects
each source. The unused source deliberately names a missing variable. Supply
fictional values on the machine running Richt:

```sh
export RICHT_EXAMPLE_PASSWORD='fictional-env-password-first'
export RICHT_EXAMPLE_EMPTY=''
export RICHT_EXAMPLE_MULTILINE='fictional env first line
fictional env second line
'
```

An empty value is present, and multiline values retain their bytes. The first
lookup reads every variable declared by that source; any missing variable fails
the entire source, including a variable the template never requests. Unused
sources and absent services read no variables. Each diff takes a fresh source
snapshot. Variables are read on the controller, including for SSH/sudo targets;
they need not be exported on the target. `secret` and automatic file sensitivity
work identically for YAML and env sources.

The optional env acceptance suite needs a Linux target with `/bin/sh`, GNU
`stat`, and the normal Richt transport dependencies (including Python 3). It
uses the same verifier and never installs packages or changes systemd units:

```sh
# Linux controller and local target, without privilege escalation.
python3 examples/secrets/verify.py ./bin/richt --env
# Linux local target with explicit noninteractive sudo.
python3 examples/secrets/verify.py ./bin/richt --env --sudo --work-root /tmp/secret-evidence
# Any supported controller with an authorized disposable Linux SSH target.
python3 examples/secrets/verify.py ./bin/richt --env \
  --host deploy@secret-test.example --sudo --work-root /tmp/secret-evidence
```

The original four YAML groups run locally first. The env suite supplies and
unsets its own dummy values, so the exports above are unnecessary for these
commands. It checks inspection without variables/templates/sources, missing
unrequested variables, empty and multiline values, unused sources, independent
YAML/env lookup, and local `--source-config` bindings. A linked oneshot prints
the secret-bearing file on both output streams, deliberately fails once, and
then succeeds on saved-plan continuation after inputs and variables disappear.
Captured plain progress, errors, and success output are checked for leaks. A
second diff changes the secret and verifies one actual process reaction; no-op,
successful continuation, and absent removal are checked as well.

The suite creates a unique mode-0700 `/var/tmp/richt-env-secret-*` target
directory and removes it in `finally`, including on failure. All plans, locks,
and target configs are checked for mode 0600. By default controller evidence is
also removed. `--work-root` retains a unique private evidence directory with
captured output, executable plans and `acceptance.json`; the report includes
the binary hash, transport, sudo choice, completed checks and cleanup result.
The verifier never prints captured file contents, even when a check fails.
SSH requires an existing trusted host key and noninteractive authentication;
`--sudo` requires already-authorized `sudo -n` access.

The static YAML inventory targets `/var/tmp/richt-secret-example/app.conf` when used
directly. Prefer the verifier for a walkthrough with automatic cleanup. For
the env inventory the static path is `/var/tmp/richt-env-secret-example/app.conf`.
For real use, replace the fictional sources with private local values and keep the
inventory, sources, generated plans, and containing directories under suitable
access controls. Human-readable output is redacted; executable JSON retains
real old and new bytes. Do not share saved plans as harmless examples.

See [secrets and sensitive files](../../docs/secrets.md) for the full contract.
The default YAML-only run does not establish Linux or SSH behavior. The optional
env suite establishes only the transport and privilege mode actually exercised;
it does not test RPM, systemd, SELinux labels, or external secret stores.
