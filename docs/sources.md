---
title: "HTTPS sources"
description: "Load inventories, services, and pinned templates over HTTPS with explicit client trust and credentials."
weight: 42
---

Richt can read inventory, service definitions, and templates over HTTPS on the
controller preparing the plan. Public and private/internal HTTPS endpoints are
supported. These sources are separate from [download/archive binaries](services/binaries/download.md),
which fetch public artifacts on the target during apply.
Without `-i`, Richt uses the local `inventory.yaml` in the current directory;
HTTPS locations are explicit.

```sh
richt inventory show -i https://config.example.net/fleet/inventory.yaml
richt diff -i https://config.example.net/fleet/inventory.yaml \
  --inventory-sha256 "$INVENTORY_SHA256" --source-config client.yaml -o plan.json
richt apply plan.json
```

The root inventory digest is optional; `--inventory-sha256` requires an HTTPS
inventory and exactly 64 hexadecimal digits. Obtain expected digests through a
trusted publication/review process. A checksum proves which bytes were read;
review those bytes and the resulting executable plan before applying.

## References and pins

A service entry contains either inline `order` and components, `file`, or
`url` with mandatory `sha256`. The referenced document contains the complete
service definition, including `order`. A referenced service cannot reference
another service document.

```yaml
services:
  app:
    url: https://config.example.net/fleet/services/app.yaml
    sha256: "<64 hexadecimal digits for the service document>"
```

Templates likewise contain exactly one of `inline`, `file`, or `url`. Every
`template.url` requires its own `sha256`, including templates inside a pinned
service. Units and drop-ins use the same template fields:

```yaml
order: 10
configs:
  - path: /etc/app.conf
    template:
      url: ../templates/app.gotmpl
      sha256: "<64 hexadecimal digits for the template>"
```

That relative URL is valid in a remote service. References in local documents
must use absolute HTTPS URLs for remote sources. In a remote document, relative
URL references resolve against the final response URL after redirects; absolute
HTTPS references are also accepted. Scheme-relative `//host/path` references
are rejected. A pinned service fixes its template URLs and expected digests,
so changing the template bytes requires publishing an updated service pin.

Local documents retain local `file` references relative to the declaring file.
Remote inventory and service documents cannot declare local `file` references
or `secret_sources`. Remote templates may use `secret`; the operator supplies
the local bindings described below. Absolute target paths such as `/etc/app.conf`
continue to describe target resources.

Source and redirect URLs must be absolute HTTPS after resolution, without
userinfo, query strings, or fragments. Credentials and signed query URLs cannot
be embedded in documents. There is no HTTP fallback or TLS verification bypass.

## Explicit client settings

`--source-config PATH` reads one local YAML file explicitly selected by the
operator. There is no automatic discovery or remote selection of this file.
Unknown fields are errors. It may add a CA file, scoped credentials, and named
local YAML secret bindings:

```yaml
ca_file: trust/company-ca.pem
auth:
  - origin: https://config.example.net
    path_prefix: /fleet
    bearer: {env: RICHT_SOURCE_TOKEN}
  - origin: https://other.example.net
    path_prefix: /services
    basic:
      username: richt-reader
      password: {file: credentials/password.txt}
secret_sources:
  production: {type: yaml, file: secrets/production.yaml}
```

`ca_file` adds certificates to the system trust roots. Its path, credential
file paths, and secret binding paths resolve relative to the client file;
absolute local paths are also accepted. They cannot be URLs. Secret source
values remain lazy and are read only when a rendered template calls `secret`.
A client binding that collides with an inventory's source name is rejected.

Each authentication entry requires exactly one `bearer` or `basic` block.
Bearer tokens and basic passwords require exactly one `env` or `file` reference;
literal credential values are unsupported. Basic usernames are explicit strings.
Credentials must be nonempty single-line values of at most 64 KiB. A credential
file may end in one line ending, which is removed. The client file is limited
to 1 MiB. Keep these files private and keep credentials out of inventory,
templates, shell arguments, and version control.

Credentials match an exact origin (scheme, host and effective port) and a path
prefix at a path boundary. Omitted `path_prefix` means `/`; the longest matching
prefix wins. Duplicate scopes are rejected. Each redirect rebuilds authorization
for its new destination, including redirects within the same origin; credentials
are never inherited from the previous request. No cookie session is used.
Normal controller proxy environment settings apply.

## Preparation, limits, and saved plans

Inventory inspection can fetch the inventory and all referenced service
definitions, including unselected services, to validate the complete schema.
It never fetches templates, reads secret values, or contacts managed hosts.
Diff acquires and checks selected present templates on the controller, then
renders them with each host's merged variables and lazy secret lookup. All
templates finish preparation before any host observation. Absent services do
not fetch templates or secrets.

One source client retains one byte snapshot or failure per requested URL for
the preparation. Repeated use across services, configs, owned units, drop-ins,
and hosts reuses those bytes while rendering separately for each host. Every
reference checks its expected digest against the snapshot. There are no
automatic retries or persistent source caches. Cancellation reaches active
HTTPS requests.

Default bounds are 30 seconds per request including redirects, 10 MiB of
decoded bytes per document, 100 MiB of decoded bytes total, 256 distinct
documents, and five redirects. Source errors do not print response bodies,
credentials, or underlying HTTP URL diagnostics. Sensitive template acquisition
and rendering retain the [safe error and display rules](secrets.md).

Saved plans keep rendered contents and executable operations in format `1.8`.
Preparation source URLs, checksums, CA configuration, and authentication are
absent from those operations. Apply does not fetch inventory, services, or
templates, load client settings, or look up secrets. It therefore works after
these sources disappear. Other execution-time dependencies remain: package
transactions and download/archive binaries can still need network access.

The [HTTPS sources example](../examples/https-sources/README.md) includes a
publisher that generates a pinned catalog from local service and template files.
See [embedding](embedding.md#https-source-clients) for the public Go API.
