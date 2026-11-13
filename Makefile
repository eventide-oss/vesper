# The version is injected at build time. Override it with `make build VERSION=v0.1.0`.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Image of the operator (the manager) for docker-build, docker-push and deploy.
IMG ?= controller:latest
# Replaces the YEAR placeholder in hack/boilerplate.go.txt in generated Go files.
# Fixed, not the current year: otherwise every generated file would change each
# January, and verify-generated would fail on every pull request.
YEAR ?= 2026
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

.PHONY: docs-reference
docs-reference: crd-ref-docs ## Generate docs/crd-reference.md from the Go types of the API.
	"$(CRD_REF_DOCS)" --source-path=api --config=hack/crd-ref-docs/config.yaml \
		--renderer=markdown --output-path=docs/crd-reference.md --log-level=WARN

# Files the generators write: the CRD and RBAC manifests, the DeepCopy
# methods, and the reference of the API.
GENERATED_PATHS := config/crd/bases config/rbac/role.yaml ':(glob)**/zz_generated.*.go' docs/crd-reference.md

.PHONY: verify-generated
verify-generated: manifests generate docs-reference ## Fail if the generated files differ from the committed ones.
	@if [ -n "$$(git status --porcelain -- $(GENERATED_PATHS))" ]; then \
		echo "Generated files are out of date: run 'make manifests generate docs-reference' and commit the result." >&2; \
		git status --short -- $(GENERATED_PATHS) >&2; \
		git --no-pager diff -- $(GENERATED_PATHS) >&2; \
		exit 1; \
	fi
	@echo "Generated files are up to date."

# The controller tests start a real API server and etcd (envtest): KUBEBUILDER_ASSETS
# points them to the binaries. Without it, `go test ./...` skips those tests.
.PHONY: test
test: setup-envtest ## Run the unit and controller (envtest) tests.
	KUBEBUILDER_ASSETS="$$("$(SETUP_ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" \
		go test -race -coverprofile=cover.out ./...

# The coverage of the last `make test`, per function for the lifecycle of a preview
# (expiry, finalizer, status), and in total.
COVER_FILES ?= internal/controller/(ttl|finalizer|status)\.go
.PHONY: cover
cover: ## Report the test coverage of the lifecycle code (run make test first).
	@test -f cover.out || { echo "cover.out not found: run make test first" >&2; exit 1; }
	@go tool cover -func=cover.out | grep -E '$(COVER_FILES)|^total:'

# Builds the operator image, deploys it in a fresh kind cluster (vesper-e2e, deleted at
# the end) and checks that a preview becomes Ready. Needs Docker and kind.
.PHONY: test-e2e
test-e2e: kustomize ## Run the operator end-to-end test in a fresh kind cluster.
	KUSTOMIZE="$(KUSTOMIZE)" hack/test-e2e.sh

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

.PHONY: check-license
check-license: ## Fail if a Go file has no license header.
	hack/check-license.sh

.PHONY: shellcheck
shellcheck: ## Check the scripts of hack/ with ShellCheck (install it first, see docs/development.md).
	shellcheck hack/*.sh

##@ Build

.PHONY: build
build: manifests generate ## Build vesperctl, the manager and vesper-github into bin/.
	go build -ldflags "$(LDFLAGS)" -o bin/vesperctl ./cmd/vesperctl
	go build -ldflags "$(LDFLAGS)" -o bin/manager ./cmd
	go build -ldflags "$(LDFLAGS)" -o bin/vesper-github ./cmd/vesper-github

.PHONY: run
run: manifests generate ## Run the manager from your host against the current kubectl context.
	go run ./cmd

.PHONY: docker-build
docker-build: ## Build the manager image.
	$(CONTAINER_TOOL) build --build-arg VERSION=$(VERSION) -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push the manager image.
	$(CONTAINER_TOOL) push ${IMG}

# The image is set in an overlay under dist/, not in config/manager: the
# working tree stays clean, which the release needs. IMG may carry a digest:
# IMG=ghcr.io/eventide-oss/vesper:v0.1.0@sha256:<digest>.
.PHONY: build-installer
build-installer: manifests generate kustomize ## Write dist/install.yaml (CRDs, operator) and dist/install-github.yaml (vesper-github).
	rm -rf dist/installer dist/installer-github && mkdir -p dist/installer dist/installer-github
	printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n- ../../config/default\n' > dist/installer/kustomization.yaml
	cd dist/installer && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build dist/installer > dist/install.yaml
	printf 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n- ../../config/github\n' > dist/installer-github/kustomization.yaml
	cd dist/installer-github && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build dist/installer-github > dist/install-github.yaml

.PHONY: release-check
release-check: goreleaser ## Validate .goreleaser.yaml.
	"$(GORELEASER)" check

.PHONY: release-snapshot
release-snapshot: build-installer goreleaser ## Build the release assets into dist/release/, without publishing them.
	"$(GORELEASER)" release --snapshot --clean

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
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest
GORELEASER ?= $(LOCALBIN)/goreleaser
CRD_REF_DOCS ?= $(LOCALBIN)/crd-ref-docs

KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.22.0
SETUP_ENVTEST_VERSION ?= v0.25.2
# CI installs the same version with goreleaser-action (make goreleaser-version).
GORELEASER_VERSION ?= v2.19.0
CRD_REF_DOCS_VERSION ?= v0.3.0
# Version of the kube-apiserver and etcd binaries of envtest. Keep it in line with
# the k8s.io/api version in go.mod (v0.37.x is Kubernetes 1.37).
ENVTEST_K8S_VERSION ?= 1.37.0

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize into bin/ if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen into bin/ if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: $(SETUP_ENVTEST) ## Download setup-envtest and the envtest binaries into bin/ if necessary.
	@"$(SETUP_ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path >/dev/null
$(SETUP_ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(SETUP_ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(SETUP_ENVTEST_VERSION))

.PHONY: goreleaser
goreleaser: $(GORELEASER) ## Download GoReleaser into bin/ if necessary (the first build takes a few minutes).
$(GORELEASER): $(LOCALBIN)
	$(call go-install-tool,$(GORELEASER),github.com/goreleaser/goreleaser/v2,$(GORELEASER_VERSION))

.PHONY: crd-ref-docs
crd-ref-docs: $(CRD_REF_DOCS) ## Download crd-ref-docs into bin/ if necessary.
$(CRD_REF_DOCS): $(LOCALBIN)
	$(call go-install-tool,$(CRD_REF_DOCS),github.com/elastic/crd-ref-docs,$(CRD_REF_DOCS_VERSION))

.PHONY: goreleaser-version
goreleaser-version: ## Print the pinned version of GoReleaser.
	@echo "$(GORELEASER_VERSION)"

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
