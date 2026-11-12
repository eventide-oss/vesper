# The image of the operator (the manager) and of vesper-github, the GitHub
# integration (ADR-0007): two programs, one image, one release.
#
# Base images are pinned by digest, so that a build always starts from the
# same bytes; Dependabot proposes the updates. Keep the Go version in line
# with go.mod.
#
# The builder runs on the platform of the build machine (BUILDPLATFORM) and
# Go cross-compiles for the target (TARGETOS, TARGETARCH): an image for
# another architecture builds without emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:08bcb99a38f85a265f9a93d1d3323e2440eec333380f3ea26ee0dec9264bb41c AS builder
ARG TARGETOS
ARG TARGETARCH
# The version the manager logs when it starts; set by the release workflow
# and by `make docker-build`.
ARG VERSION=dev

WORKDIR /workspace
# Download the modules first: this layer stays cached until go.mod or go.sum
# change.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the Go source (.dockerignore keeps only the Go files, without tests).
COPY . .

# GOARCH has no default: with a plain `docker build`, TARGETARCH is the
# architecture of the host, so the binary matches the image.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o manager ./cmd && \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o vesper-github ./cmd/vesper-github

# Distroless: no shell and no package manager, only the binary. The
# "nonroot" variant runs as user 65532.
FROM gcr.io/distroless/static:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
WORKDIR /
COPY --from=builder /workspace/manager /workspace/vesper-github ./
USER 65532:65532

# The operator by default; the Deployment of vesper-github sets its command.
ENTRYPOINT ["/manager"]
