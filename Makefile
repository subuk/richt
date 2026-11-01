GO ?= go
VERSION ?= dev
LDFLAGS = -X github.com/subuk/richt/cli.Version=$(VERSION)

.PHONY: check build install linux-amd64 docs-build docs-serve docs-check

check:
	GO='$(GO)' sh scripts/check.sh

build:
	mkdir -p bin
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/richt .

install:
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' .

linux-amd64:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/richt-linux-amd64 .

docs-build:
	npm --prefix website run build

docs-serve:
	npm --prefix website run serve

docs-check: docs-build
	python3 scripts/check-docs.py website/public
	npm --prefix website run check
