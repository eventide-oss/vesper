# The version is injected at build time. Override it with `make build VERSION=v0.1.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Image of the operator (the manager) for docker-build, docker-push and deploy.
IMG ?= controller:latest
# Replaces the YEAR placeholder in hack/boilerplate.go.txt in generated Go files.
YEAR ?= $(shell date +%Y)
# Tool used to build images. The targets are tested with Docker only.
CONTAINER_TOOL ?= docker

# Recipes run with bash; a failing command, even inside a pipe, stops the target.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate the CRD and RBAC manifests from the Go types and markers.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate the DeepCopy methods of the API types.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: test
test: ## Run the unit tests.
	go test -race ./...

# Runs vesperctl against the kind cluster (created if missing) with the generated CRD.
# -count=1 disables the test cache: the result depends on the cluster.
.PHONY: test-integration
test-integration: kind-up ## Run the CLI integration tests against kind.
	VESPER_IT_CONTEXT=kind-$(KIND_CLUSTER) go test -race -count=1 -tags integration ./test/integration/...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint (install it first, see docs/development.md).
	golangci-lint run

##@ Build

.PHONY: build
build: manifests generate ## Build vesperctl and the manager into bin/.
	go build -ldflags "$(LDFLAGS)" -o bin/vesperctl ./cmd/vesperctl
	go build -o bin/manager ./cmd

.PHONY: run
run: manifests generate ## Run the manager from your host against the current kubectl context.
	go run ./cmd

.PHONY: docker-build
docker-build: ## Build the manager image.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push the manager image.
	$(CONTAINER_TOOL) push ${IMG}

.PHONY: build-installer
build-installer: manifests generate kustomize ## Write the CRDs and the operator deployment to dist/install.yaml.
	mkdir -p dist
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install the CRDs into the cluster of the current kubectl context.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests kustomize ## Remove the CRDs. Add ignore-not-found=true to ignore missing ones.
	@out="$$( "$(KUSTOMIZE)" build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests kustomize ## Deploy the operator (image IMG) to the cluster of the current kubectl context.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove the operator. Add ignore-not-found=true to ignore missing objects.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Local cluster

# Local development cluster (see docs/development.md).
# The kubectl context created by kind is named "kind-$(KIND_CLUSTER)".
KIND_CLUSTER ?= vesper-dev

# Idempotent: does nothing if the cluster already exists.
.PHONY: kind-up
kind-up: ## Create the local kind cluster if it does not exist.
	@command -v kind >/dev/null || { echo "kind is not installed: see docs/development.md" >&2; exit 1; }
	@command -v docker >/dev/null || { echo "docker is not installed: see docs/development.md" >&2; exit 1; }
	@if kind get clusters 2>/dev/null | grep -qx "$(KIND_CLUSTER)"; then \
		echo "kind cluster $(KIND_CLUSTER) already exists"; \
	else \
		kind create cluster --name "$(KIND_CLUSTER)" --config hack/kind-config.yaml; \
	fi

# Idempotent: kind does not fail when the cluster does not exist.
.PHONY: kind-down
kind-down: ## Delete the local kind cluster.
	@command -v kind >/dev/null || { echo "kind is not installed: see docs/development.md" >&2; exit 1; }
	kind delete cluster --name "$(KIND_CLUSTER)"

##@ Dependencies

# Tools are installed into bin/ at a pinned version.
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.22.0

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize into bin/ if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen into bin/ if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

# go-install-tool installs a Go tool at a given version as bin/<name>-<version>
# and points the symlink bin/<name> to it.
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
