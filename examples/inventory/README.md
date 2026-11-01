# Inventory inspection illustration

This example illustrates merging variables, assignment ordering, selections,
service file paths, and fictional SSH settings. It is **not** an execution
quickstart: `lanst.example.invalid` is a fictional address, its declared secret
source files need not exist, and the local cloud assumes an existing libvirt
hypervisor. Use [the runnable Vmango walkthrough](../vmango/README.md) for an
isolated RPM/systemd installation with no hypervisor.

Run from the repository root:

```sh
bin/richt inventory validate -i examples/inventory/richt.yaml
bin/richt inventory show -i examples/inventory/richt.yaml
bin/richt inventory show -i examples/inventory/richt.yaml --tag hypervisor --service vmango
```

These commands do not contact hosts, open templates, or read secret values.
The `home` and `net` tags override only `vmango.cloud_name`; the shared listen
address and libvirt URI remain. Adding both tags to one host causes an
equal-weight conflict. Both services must be selected for a first installation;
`--service vmango` does not implicitly select the repository service.
