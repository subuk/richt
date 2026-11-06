# Published DEB package

This example installs the published `tree` package from a disposable Debian or
Ubuntu target's existing trusted APT repositories. It needs APT, dpkg,
Python 3.8+, network access and explicit noninteractive sudo. It does not add a
repository or build a custom DEB. Commands run from the repository root.

```sh
make build
bin/richt inventory validate -i examples/deb/inventory.yaml
bin/richt inventory show -i examples/deb/inventory.yaml
bin/richt diff -i examples/deb/inventory.yaml -o /tmp/richt-tree-plan.json
bin/richt apply /tmp/richt-tree-plan.json
bin/richt diff -i examples/deb/inventory.yaml
```

Inventory inspection does not contact the target. Diff reads installed dpkg
state without updating APT metadata or contacting repositories. Apply performs
the displayed conditional cache check/refresh and package transaction. The
next diff is empty once the package is installed. An omitted version preserves
an existing installation; it does not upgrade to a newer repository version.

For an exact version change, add a quoted `version` from the target repository,
then prepare and review a new plan. APT resolves availability and dependencies
at apply. To remove the package, change the tag assignment to
`tree: {state: absent}`, prepare a new plan, review it and apply it. Removal
preserves conffiles and does not run autoremove. Do this only if the package
was installed for this walkthrough; existing installations are not owned
exclusively by the example.

For SSH, replace transport with an existing trusted alias:

```yaml
transport: {type: ssh, host: debian-test, user: deploy, sudo: true}
```

The [DEB reference](../../docs/services/binaries/deb.md) defines cache policies,
conffile handling and frozen plans. This recipe is a runnable example; its
presence does not establish a passed Debian/Ubuntu acceptance run. Recorded
runtime results and their limits belong in [validation](../../docs/validation.md).
