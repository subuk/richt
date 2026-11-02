---
title: "CI and releases"
description: "Run the shared checks and archive build, publish a manual release, and configure required CI."
weight: 110
---

## Shared checks and archives

The `CI` workflow (`.github/workflows/ci.yml`) runs on pushes to `main` and pull
requests targeting `main`. Pull requests check GitHub's merge commit, so the
result covers the proposed change combined with the base branch. CI has
read-only repository permissions and does not publish releases or tags.

CI and the manual release workflow call the same reusable `Build binaries`
workflow (`.github/workflows/build.yml`). It runs:

```sh
make check
go test -race ./...
make release-check
```

`make check` covers formatting, vet, Go tests, CLI smoke checks, and a Linux
amd64 cross-build. `make release-check` tests the release scripts. The shared
workflow then builds all four archives on one `ubuntu-24.04` Linux runner, using
the Go version from `go.mod`, `CGO_ENABLED=0`, trimmed paths, and an explicit
binary version:

```text
richt-VERSION-linux-amd64.tar.gz
richt-VERSION-linux-arm64.tar.gz
richt-VERSION-darwin-amd64.tar.gz
richt-VERSION-darwin-arm64.tar.gz
```

The workflow extracts the Linux amd64 archive outside the checkout and checks
its version/help, inventory validation/display, config-only diff, private saved
plan, application of prepared bytes after deleting the inventory/template,
file permissions, persisted success, and skipping successful tasks during
continuation. Only this native Linux amd64 archive gets a runtime smoke test.
The other targets are compilation results. These checks do not prove macOS
runtime behavior or real RPM, systemd, root, SSH, or SELinux behavior; see
[Linux validation](validation.md) for separate disposable-host acceptance.

Each run uploads the four archives as the Actions artifact `binaries`. Ordinary
CI versions use `ci-RUN_NUMBER-COMMIT_SHA`; they are build labels rather than
published release versions. An archive contains one directory matching its
filename stem, holding `richt` and `README.md`. Windows is deferred to
[KRG-92](https://linear.app/krglv/issue/KRG-92/support-windows-as-an-ssh-only-controller).
This workflow produces no DEB, RPM, or Docker artifacts.

## Publish a release

The workflow must first be present on the default branch for GitHub to offer
its manual run form. Use **Actions → Release binaries → Run workflow** on
`main`. These instructions describe publication; they do not assert that a
release has already been published.

1. Commit and push the intended changes. Prefer a reviewed commit SHA as the
   source. No version change in a source file is needed.
2. Enter `version` without a leading `v`, using `X.Y.Z` or a prerelease such as
   `X.Y.Z-rc.1`. Build metadata (`+...`) is not accepted. Set `source_ref` to a
   branch, tag, or commit; it defaults to `main`.
3. Preparation validates the version and resolves `source_ref` to one exact
   commit SHA. All checks and builds use that SHA. An existing `vVERSION` tag
   must already point to that commit; the workflow never moves it.
4. After the full shared checks, build, and native smoke test succeed, the
   publication job verifies the local set of four archives and generates
   `SHA256SUMS`. It creates a draft release with all four archives and the
   checksum file, then publishes only after that upload command succeeds.
   GitHub creates `vVERSION` at the resolved commit if needed.
   Prerelease versions are marked as prereleases.
5. Inspect the published assets and verify the downloaded archive using the
   [installation instructions](installation.md#install-a-release-archive).
   Record any additional real-platform validation in the release notes.

Only the final publication job has `contents: write`; source checks and builds
have read-only permissions and no publication credentials. Publication uses
artifacts from its own workflow run and does not rebuild code. No personal
access token is required. Runs for the same version are serialized.

The workflow rejects an existing release, including a draft, and never
overwrites assets. If publication fails after creating a draft, inspect its
assets and source commit before either completing publication manually or
deleting the incomplete draft and rerunning. Investigate a tag mismatch;
do not move the tag to make a rerun pass. Published source changes require a
new version.

## Required repository protection

Workflow files alone do not enforce merge policy. Configure branch protection
or a ruleset for `main` separately: require pull requests, require the unique
aggregate status check **CI**, and require branches to be up to date with
`main` before merging. The aggregate check must pass only after the shared
checks, archive build, and smoke test succeed. Confirm the check appears after
the workflow's first run before selecting it in repository settings. This
guide does not assert that those repository settings are already configured.

## Reproduce locally

From a checkout, install Go as specified in `go.mod`, Make, and Python 3.9 or
newer (CI uses Python 3.12). Run the same checks and archive builder:

```sh
make check
go test -race ./...
make release-check
python3 scripts/release/package.py --version X.Y.Z --output-dir dist
```

Replace `X.Y.Z` with a valid version label. The package script cross-compiles
all four targets. On Linux amd64, smoke-test the resulting native archive:

```sh
python3 scripts/release/smoke.py \
  --archive dist/richt-X.Y.Z-linux-amd64.tar.gz --version X.Y.Z
```

The smoke test must run on the archive's native platform. It uses a temporary
directory and config-only resources without root or managed host access.
`make release-archives VERSION=X.Y.Z` is a shortcut for the package command.
`SHA256SUMS` is generated by the publication workflow, not the local package
script. Reproducing this process does not promise byte-identical archives
across different toolchain versions or build environments.
