# Pinned K3s executable

This binary-only example installs the Linux amd64 K3s `v1.34.3+k3s1`
executable from the [upstream release](https://github.com/k3s-io/k3s/releases/tag/v1.34.3%2Bk3s1).
The pinned SHA-256 was read from its published
[sha256sum-amd64.txt](https://github.com/k3s-io/k3s/releases/download/v1.34.3%2Bk3s1/sha256sum-amd64.txt).
It demonstrates direct file installation; it does not provision a cluster,
install the upstream service/unit, container networking, SELinux policy,
symlinks, kill/uninstall scripts, or container images. This pinned release is
an illustration, not a recommendation for a new cluster.

Use an authorized disposable Linux amd64 target with Python 3.8+, CA trust,
HTTPS access to GitHub release assets, a trusted SSH alias and noninteractive
sudo. Replace `k3s-test`/`deploy` with your configured alias/login. Start without
`/usr/local/bin/k3s`; Richt refuses adoption of an existing executable. Ensure
`/usr/local/bin` has trustworthy ownership/permissions and no symlink parents.
No Richt daemon or Go installation is needed on the target.

From the repository root after `make build`:

```sh
bin/richt inventory validate -i examples/k3s/inventory.yaml
bin/richt diff -i examples/k3s/inventory.yaml -o /var/tmp/richt-k3s-plan.json
bin/richt apply /var/tmp/richt-k3s-plan.json
ssh k3s-test /usr/local/bin/k3s --version
bin/richt diff -i examples/k3s/inventory.yaml
```

Diff does not download the executable. Apply fetches the frozen URL, verifies
its SHA-256 and atomically publishes mode `0755`; JSON carries no binary bytes.
A subsequent diff has no artifact change. To update the installed bytes, change
the URL and digest together to a verified release, prepare a new plan and review
it. Merely editing inventory does not change an already saved plan.

For cleanup, set the assignment to `k3s-binary: {state: absent}`, diff and apply.
This removes only the owned executable. It is **not a K3s uninstall or cluster
teardown**. If you later use the executable to run Kubernetes, preserve cluster
data and follow the [K3s upgrade guidance](https://docs.k3s.io/upgrades) and
[uninstall guidance](https://docs.k3s.io/installation/uninstall). Replacing a
binary with an older release does not make Kubernetes downgrade generally safe;
datastore snapshots and supported recovery/version procedures are separate
operational requirements.
