# Richt website

This local Hugo and Docsy site has a custom landing page and a documentation
layout with a sidebar, table of contents, local search, code copying, and a
shared light/dark/system color preference. Analytics and automatic publication
from the source branch are not configured.

## Build and preview

Use Go (for Hugo modules) and Node.js 22 or newer. From the repository root:

```sh
npm ci --prefix website
make docs-build
make docs-serve
```

Open <http://localhost:1313/>. Stop the server with Ctrl-C. The equivalent
commands are `npm --prefix website run build` and `npm --prefix website run serve`.
The preview binds only to loopback and serves rendered pages from memory, so
live-reload scripts do not alter the checked build. Builds write `website/public/`.

The npm lockfile pins Hugo extended 0.163.0, Sass embedded 1.105.0, Lunr 2.3.9,
Bootstrap, and Font Awesome. `go.mod` and `go.sum` pin Docsy 0.18.0. The scripts
use the checkout's Hugo and native Sass compiler instead of globally installed
versions, and place Hugo's downloaded modules in `website/.cache/`. Initial
installation and the first module download need network access; later builds
use the local caches. On macOS and Linux, npm installs the matching native Sass
binary automatically.

## Content and checks

Canonical guides stay in `../docs/*.md`, mounted at `/docs/`. The landing page
reads its YAML example directly from `../examples/files/inventory.yaml`.
Walkthrough wrappers render `../examples/*/README.md` directly through a
shortcode; their prose is not copied here. The link render hook maps repository
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

## Manual GitHub Pages publication

GitHub Pages serves the root of the separate `gh-pages` branch. That branch
contains only the generated site and `.nojekyll`; updating `main` does not
publish a new website. No custom domain is configured in this snapshot.

Build and validate the project-path version before updating the publication
branch:

```sh
npm --prefix website run build -- \
  --baseURL https://subuk.github.io/richt/ --destination /tmp/richt-pages-release
python3 scripts/check-docs.py /tmp/richt-pages-release \
  --base-url https://subuk.github.io/richt/
node website/scripts/check.mjs /tmp/richt-pages-release
```

Commit that directory's contents to `gh-pages` and push it explicitly. Keep
dependencies, caches, executable plans, and source checkout files out of that
branch. When moving to a custom domain, rebuild with its HTTPS root as the
base URL and configure the domain separately in GitHub Pages and DNS.
