#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
GO=${GO:-go}
unformatted=$(find . \( -path './.git' -o -path './vendor' -o -path './website' \) -prune -o -name '*.go' -exec gofmt -l {} +)
if [ -n "$unformatted" ]; then
    printf 'Run gofmt on:\n%s\n' "$unformatted" >&2
    exit 1
fi
"$GO" vet ./...
"$GO" test ./...
check_dir=$(mktemp -d)
trap 'rm -rf "$check_dir"' EXIT HUP INT TERM
"$GO" build -trimpath -o "$check_dir/richt" .
"$check_dir/richt" --help
"$check_dir/richt" --version
if "$check_dir/richt" unknown-command >"$check_dir/stdout" 2>"$check_dir/stderr"; then
    printf 'Unknown command unexpectedly succeeded\n' >&2
    exit 1
fi
if [ ! -s "$check_dir/stderr" ]; then
    printf 'Unknown command did not explain its error\n' >&2
    exit 1
fi
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO" build -trimpath -o "$check_dir/richt-linux-amd64" .
