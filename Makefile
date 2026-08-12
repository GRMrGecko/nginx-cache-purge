BINARY  := nginx-cache-purge
# The build identifiers live in package main, so ldflags target it directly
# rather than an imported configuration package.
PACKAGE := main
# VERSION is the single source of truth for the version string. COMMIT and DATE
# are derived from git and the build clock.
VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null)
DATE    := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w -X $(PACKAGE).Version=$(VERSION) -X $(PACKAGE).Commit=$(COMMIT) -X $(PACKAGE).Date=$(DATE)

.PHONY: all build test vet fmt snapshot release clean

all: build

## build: native static binary into dist/
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BINARY) .

## test: run the unit tests
test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

## snapshot: local GoReleaser build without publishing (artifacts in dist/)
snapshot:
	goreleaser release --snapshot --clean

## release: full GoReleaser release (CI runs this on a tag)
release:
	goreleaser release --clean

clean:
	rm -rf dist
