# Kubernetes repository exclusions

This example creates a Kubernetes RPM repository with its normal `exclude`
policy and requests kubelet and kubeadm version `1.34.3` using
`disable_excludes: kubernetes`. The version illustrates the KRG-99 migration
case; it is not a recommendation for a new cluster. Choose an available release
and its matching minor-version repository for your environment.

Use a disposable Linux host with DNF 4, network access to the published
repository and signing key, and noninteractive `sudo -n`. The local transport
explicitly requests sudo. Start without kubelet/kubeadm, a repository named
`kubernetes`, or `/etc/yum.repos.d/richt-kubernetes.repo`. Package dependencies
must be available from the host's configured repositories. This example manages
packages only; it does not configure a container runtime, start kubelet, or
initialize a cluster. SELinux settings are left unchanged.

The repository service has order 10 and the package service has order 20.
They are separate because binaries precede configs inside a service.
Preparation observes installed packages without consulting the new repository;
apply writes the repository before requesting packages.

Run from the repository root after `make build`:

```sh
bin/richt inventory validate -i examples/kubernetes/inventory.yaml
bin/richt diff -i examples/kubernetes/inventory.yaml -o /var/tmp/richt-kubernetes-plan.json
bin/richt apply /var/tmp/richt-kubernetes-plan.json
rpm -q kubelet kubeadm --qf '%{NAME} %{VERSION}\n'
bin/richt diff -i examples/kubernetes/inventory.yaml
```

Review `dnf -y --disableexcludes=kubernetes install -- kubelet-1.34.3`
and the corresponding kubeadm action. The saved plan retains the override,
package requests, and repository contents. Changing inventory after diff does
not alter them. The repo-file exclusion remains in place after apply; ordinary
DNF transactions continue to use it. A subsequent matching diff has no package
tasks, even if only the override is changed or omitted. A DNF failure leaves
the saved task available for explicit continuation after its cause is fixed.

The override follows DNF's transaction scope: it disables exclusion and inclusion
filters for all packages from this repository involved in that transaction,
including dependencies. It does not restrict package selection to that
repository. See the [RPM reference](../../docs/services/binaries/rpm.md) for
`main`, `all`, exact versions, YUM 4, and validation.

For cleanup, first set only `kubernetes-packages` to `{state: absent}`,
prepare and apply its removal plan while the repository remains present.
The removal actions retain `--disableexcludes=kubernetes`. Then set
`kubernetes-repository` to `{state: absent}` and prepare/apply again to remove
the repo file. Repository and service order stays ascending during absence.
Package-manager dependency removal and residual state follow normal DNF/RPM
rules; this is not a cluster teardown procedure.

Repository layout follows the upstream
[kubeadm package installation guide](https://kubernetes.io/docs/setup/production-environment/tools/kubeadm/install-kubeadm/).
This inventory is schema-checked with the other source examples. Runtime
exclusion tests use published Vmango packages in the
[RPM acceptance harness](../rpm-versions/README.md); they do not claim Kubernetes
cluster validation.
