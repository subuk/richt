# HTTPS inventory and reusable services

This example builds a versioned catalog containing an inventory, a service, and
two Go templates. Every external service/template reference pins the exact
downloaded bytes with SHA-256. The sample manages one environment file and a
demonstration systemd service that sleeps until stopped. Use disposable Linux
hosts with systemd, SSH access and the normal [target prerequisites](../../docs/installation.md).

Build Richt and generate a new release directory outside the checkout:

```sh
make build
python3 examples/https-sources/publish.py \
  --output /tmp/richt-catalog-v1 \
  --base-url https://config.example.org/richt/v1/ \
  --host test-a --host test-b --user deploy --sudo
```

Replace the example URL and SSH host aliases with your own. `--sudo` requests
noninteractive sudo explicitly. The generator refuses to overwrite an existing
directory; use a new release directory for each publication. It prints the root
inventory checksum. The generated `.yaml` files use JSON syntax, which YAML accepts.

Publish the **complete contents** of `/tmp/richt-catalog-v1` at the selected URL
using your existing HTTPS static server, object storage, or reverse proxy. Keep
the published release immutable. The generator does not upload files or grant
host access. Do not publish client settings, credentials, secret files or plans.

Inspect the remote inventory, then prepare and review a plan:

```sh
bin/richt inventory show -i https://config.example.org/richt/v1/inventory.yaml
bin/richt diff -i https://config.example.org/richt/v1/inventory.yaml \
  --inventory-sha256 '<checksum printed by publish.py>' -o /tmp/richt-plan.json
bin/richt apply /tmp/richt-plan.json
bin/richt diff -i https://config.example.org/richt/v1/inventory.yaml
```

The second diff should contain no changes. Inventory inspection downloads the
inventory and service definition but never templates or secrets, and does not
contact the target hosts. Template downloads and rendering happen on the
controller during diff. Applying the saved plan works after unpublishing the
catalog. Existing package/download operations, when present in a service, keep
their normal apply-time network requirements.

The release also contains `standalone.yaml`, with the same service and templates
inline, for a self-contained HTTPS inventory; and `local-inventory.yaml`, which
can be copied locally and references the remote service by absolute URL:

```sh
bin/richt inventory validate -i https://config.example.org/richt/v1/standalone.yaml
bin/richt diff -i /tmp/richt-catalog-v1/local-inventory.yaml
```

For a private server, create a **local** client file:

```yaml
# client.yaml: file paths are relative to this local file.
ca_file: corporate-ca.pem   # omit when the system trust store already trusts it
auth:
  - origin: https://config.example.org
    path_prefix: /richt
    bearer: {env: RICHT_CATALOG_TOKEN}
```

Supply the token through your usual environment setup, then add
`--source-config /path/to/client.yaml` to inspection/diff commands. Do not pass
source settings to saved-plan apply. See [HTTPS sources](../../docs/sources.md)
for Basic authentication, file-based credentials, local YAML secret bindings,
redirect rules, limits, and safe diagnostics.

To remove this example's owned service and environment file while the catalog
is still available:

```sh
bin/richt diff -i https://config.example.org/richt/v1/absent.yaml -o /tmp/richt-remove.json
bin/richt apply /tmp/richt-remove.json
```

An absent service does not fetch templates or secrets. Its containing directory
is not an owned directory resource and remains after removal. Generate and save
the removal plan before taking the catalog offline if it will no longer be
available later.
