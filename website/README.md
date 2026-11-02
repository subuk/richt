# Richt website

This local Hugo and Docsy site has a custom landing page and a documentation
layout with a sidebar, table of contents, local search, code copying, and a
shared light/dark/system color preference. The production site is
[richt.dev](https://richt.dev/), published from `main` by GitHub Actions.
Analytics are not configured.

## Build and preview

Use Go (for Hugo modules) and Node.js 22 or newer. From the repository root:

```sh
npm ci --prefix website
make docs-build
make docs-serve
```

Open <http://localhost:1313/>. Stop the server with Ctrl-C. The equivalent
commands are `npm --prefix website run build` and `npm --prefix website run serve`.
The preview overrides the production base URL with `http://localhost:1313/`,
binds only to loopback, and serves rendered pages from memory, so live-reload
scripts do not alter the checked build. Production builds use `https://richt.dev/`
and write `website/public/`.

The npm lockfile pins Hugo extended 0.163.0, Sass embedded 1.105.0, Lunr 2.3.9,
Bootstrap, and Font Awesome. `go.mod` and `go.sum` pin Docsy 0.18.0. The scripts
use the checkout's Hugo and native Sass compiler instead of globally installed
versions, and place Hugo's downloaded modules in `website/.cache/`. Initial
installation and the first module download need network access; later builds
use the local caches. On macOS and Linux, npm installs the matching native Sass
binary automatically.

## Content and checks

Canonical guides stay in `../docs/`, recursively mounted at `/docs/`. Section
indexes and nested guides define the documentation hierarchy; services have
their own section for the service model, binary, config, and process components,
and lifecycle behavior. The landing page introduces inventory and services,
then reads both YAML examples directly from `../examples/files/inventory.yaml`
and `../examples/files/service.yaml`. Keep these source inclusions rather than
copying YAML into the template. Walkthrough wrappers render
`../examples/*/README.md` directly through a shortcode; their prose is not copied here. The link render hook maps repository
Markdown links to local documentation/walkthrough routes. Other example files
are mounted as downloads, with an explicit allowlist in `hugo.yaml` so stray
local plans or secret files cannot be included automatically. Add intended new
example downloads to that allowlist.

```sh
make docs-check
npm --prefix website run check
```

The first command builds and runs both the local link/anchor checker and the
generated-site checks. The second runs only the generated search index, local
search assets, theme and copy control checks, and checks for third-party scripts.
Browser checks cover actual search, copying,
theme persistence, and responsive navigation.

`layouts/_partials/head.html` follows Docsy 0.18.0 with one deliberate change:
Lunr loads from a local npm asset rather than a CDN. Recheck that override on
theme upgrades. The site intentionally has no Google Custom Search configuration.

## GitHub Pages publication

[`.github/workflows/pages.yml`](../.github/workflows/pages.yml) installs the
pinned dependencies, builds for `https://richt.dev/`, checks local links and
anchors using that production origin, and runs the generated-site checks.
Pull requests targeting `main` run validation without uploading or deploying.
Pushes to `main` upload the checked `website/public/` directory and deploy it
through the official GitHub Pages actions. A manual run also deploys only when
the selected branch is `main`. Deployment uses the `github-pages` environment;
only that job receives Pages write and identity-token permissions.

To reproduce the production checks locally:

```sh
npm --prefix website run build -- \
  --baseURL https://richt.dev/ --destination /tmp/richt-pages-release
python3 scripts/check-docs.py /tmp/richt-pages-release \
  --base-url https://richt.dev/
node website/scripts/check.mjs /tmp/richt-pages-release
```

In repository **Settings → Pages**, set **Source** to **GitHub Actions** and
**Custom domain** to `richt.dev`. Restrict the `github-pages` environment to
deployments from `main`. The former `gh-pages` branch is no longer the
publication source. A `CNAME` file is not required for an Actions deployment;
the domain is stored in GitHub Pages settings.

For the apex domain, DNS must point to GitHub Pages using an `ALIAS`/`ANAME`
record for `subuk.github.io`, or the GitHub Pages `A` records:

```text
185.199.108.153
185.199.109.153
185.199.110.153
185.199.111.153
```

Optional IPv6 uses the four GitHub Pages `AAAA` records:

```text
2606:50c0:8000::153
2606:50c0:8001::153
2606:50c0:8002::153
2606:50c0:8003::153
```

After GitHub validates DNS and provisions the certificate, enable **Enforce
HTTPS**. See GitHub's [custom domain guide](https://docs.github.com/en/pages/configuring-a-custom-domain-for-your-github-pages-site/managing-a-custom-domain-for-your-github-pages-site)
for current DNS and certificate details, and its [custom workflow guide](https://docs.github.com/en/pages/getting-started-with-github-pages/using-custom-workflows-with-github-pages)
for the Actions publication setup.
