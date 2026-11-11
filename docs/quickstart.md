# Quickstart

Run Vesper on your machine in about ten minutes: a local cluster with
[kind](https://kind.sigs.k8s.io/), the operator from a release, a preview
opened in your browser, then gone when its TTL passes.

You need [Docker](https://docs.docker.com/get-docker/),
[kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation) v0.33.0
and `kubectl`. The commands are for macOS and Linux.

## 1. Create a cluster

```sh
kind create cluster --name vesper \
  --image kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
```

The node image is pinned by tag and digest: Kubernetes 1.37, the version Vesper
is tested with. kind switches your kubectl context to `kind-vesper`.

## 2. Install the operator

Pick the release, then apply its install bundle: the CRD, the RBAC, the
namespace `vesper-system` and the operator, with its image pinned by digest.

```sh
VERSION=v0.1.0
kubectl apply -f "https://github.com/eventide-oss/vesper/releases/download/$VERSION/install.yaml"
kubectl -n vesper-system rollout status deploy/vesper-controller-manager
```

## 3. Install vesperctl

Download the archive for your platform and the checksums, check the archive,
and extract `vesperctl`:

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64) arch=amd64;; aarch64) arch=arm64;; esac
base="https://github.com/eventide-oss/vesper/releases/download/$VERSION"
curl -sSLO "$base/vesperctl_${VERSION#v}_${os}_${arch}.tar.gz"
curl -sSLO "$base/checksums.txt"
shasum -a 256 --check --ignore-missing checksums.txt     # sha256sum on Linux
tar xzf "vesperctl_${VERSION#v}_${os}_${arch}.tar.gz" vesperctl
sudo install -m 755 vesperctl /usr/local/bin/vesperctl
vesperctl version
```

With the [GitHub CLI](https://cli.github.com/), you can also check that the
archive was built by the release workflow of Vesper:
`gh attestation verify vesperctl_*.tar.gz --repo eventide-oss/vesper`.

## 4. Create a preview

```sh
vesperctl create demo --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 1h
kubectl wait --for=condition=Ready previewenvironment/demo --timeout=3m
vesperctl get demo
```

The image is pinned (Vesper refuses `latest`) and runs as a non-root user, as
the `restricted` Pod Security profile of preview namespaces requires. `Runs in`
is the namespace of the preview: its Deployment, Service, quota and
NetworkPolicy live there.

## 5. Open it

Without a domain, a preview has no public address. Forward a local port to
its Service:

```sh
kubectl port-forward -n "$(kubectl get pe demo -o jsonpath='{.status.namespace}')" svc/app 8080:80
```

Open <http://localhost:8080>: the nginx welcome page. Stop the forward with
Ctrl-C.

## 6. Let a preview expire

```sh
vesperctl create short --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 1m
kubectl wait --for=delete previewenvironment/short --timeout=3m
vesperctl list
```

After a minute, `short` and its namespace are gone; `demo` is still there.
`vesperctl extend demo --by 2h` gives a preview more time, and
`vesperctl delete demo` removes it now.

## 7. Clean up

```sh
kind delete cluster --name vesper
```

## Configure the operator

The operator reads its settings from flags, such as `--preview-domain` for
public addresses and `--preview-tls` for HTTPS. Set them with a
[kustomize](https://kustomize.io/) overlay over the bundle of the release,
rather than by editing the Deployment: applying a new `install.yaml` would
reset your changes, the overlay keeps them. Save this as
`vesper/kustomization.yaml`:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- https://github.com/eventide-oss/vesper/releases/download/v0.1.0/install.yaml
patches:
- target:
    kind: Deployment
    name: vesper-controller-manager
  patch: |-
    - op: add
      path: /spec/template/spec/containers/0/args/-
      value: --preview-domain=preview.example.com
```

```sh
kubectl apply -k vesper/
```

To upgrade, change the version in the URL and apply it again. To list the
flags, run the image with `--help`:
`docker run --rm ghcr.io/eventide-oss/vesper:$VERSION --help`.
[Development](development.md#public-address) explains the preview domain, HTTPS
and the ingress class.

## Next

- [vesperctl reference](cli.md) and [cheat sheet](cheatsheet.md).
- [Try Vesper on a server](try-on-a-server.md): k3s, a real domain and HTTPS.
- [Security model](security.md): what Vesper protects against, and what it
  does not.
