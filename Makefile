.PHONY: build test vet lint

# The version is injected at build time. Override it with `make build VERSION=v0.1.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/vesperctl ./cmd/vesperctl

test:
	go test -race ./...

vet:
	go vet ./...

lint:
	golangci-lint run
