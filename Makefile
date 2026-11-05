.PHONY: build test test-integration vet lint kind-up kind-down

# The version is injected at build time. Override it with `make build VERSION=v0.1.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	go build -ldflags "$(LDFLAGS)" -o bin/vesperctl ./cmd/vesperctl

test:
	go test -race ./...

# Runs vesperctl against the kind cluster (created if missing) with a stub CRD.
# -count=1 disables the test cache: the result depends on the cluster.
test-integration: kind-up
	VESPER_IT_CONTEXT=kind-$(KIND_CLUSTER) go test -race -count=1 -tags integration ./test/integration/...

vet:
	go vet ./...

lint:
	golangci-lint run

# Local development cluster (see docs/development.md).
# The kubectl context created by kind is named "kind-$(KIND_CLUSTER)".
KIND_CLUSTER ?= vesper-dev

# Idempotent: does nothing if the cluster already exists.
kind-up:
	@command -v kind >/dev/null || { echo "kind is not installed: see docs/development.md" >&2; exit 1; }
	@command -v docker >/dev/null || { echo "docker is not installed: see docs/development.md" >&2; exit 1; }
	@if kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)"; then \
		echo "kind cluster $(KIND_CLUSTER) already exists"; \
	else \
		kind create cluster --name "$(KIND_CLUSTER)" --config hack/kind-config.yaml; \
	fi

# Idempotent: kind does not fail when the cluster does not exist.
kind-down:
	@command -v kind >/dev/null || { echo "kind is not installed: see docs/development.md" >&2; exit 1; }
	kind delete cluster --name "$(KIND_CLUSTER)"
