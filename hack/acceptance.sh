#!/usr/bin/env bash
# The acceptance run of a release (docs/acceptance-v0.1.md): installs the
# release on a fresh kind cluster and checks each criterion, printing what
# it sees. It never touches your other clusters: it uses a kubeconfig of its
# own, in a temporary directory, and deletes the cluster at the end.
#
#   hack/acceptance.sh v0.1.0-rc.3
#
# Needs kind, kubectl, curl, jq, docker and the GitHub CLI (gh), logged in,
# for the attestation checks.
set -uo pipefail
VERSION=${1:?usage: hack/acceptance.sh <release tag, such as v0.1.0>}
work=$(mktemp -d)
cd "$work" || exit 1
export KUBECONFIG=$work/kubeconfig
cleanup() { kind delete cluster --name vesper-acceptance >/dev/null 2>&1; rm -rf "$work"; }
trap cleanup EXIT
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m); case "$arch" in x86_64) arch=amd64;; aarch64) arch=arm64;; esac
shasum256() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
step(){ echo; echo "### $*"; }
step "1. cluster"
kind create cluster --name vesper-acceptance --image kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5 2>&1 | tail -1
step "2. install from the release"
kubectl apply -f "https://github.com/eventide-oss/vesper/releases/download/$VERSION/install.yaml" | tail -2
kubectl -n vesper-system rollout status deploy/vesper-controller-manager --timeout=5m
kubectl -n vesper-system logs deploy/vesper-controller-manager | head -1
kubectl -n vesper-system get deploy vesper-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}{"\n"}'
step "3. vesperctl from the release, checked"
base="https://github.com/eventide-oss/vesper/releases/download/$VERSION"
curl -sSLO "$base/vesperctl_${VERSION#v}_${os}_${arch}.tar.gz"; curl -sSLO "$base/checksums.txt"
shasum256 --check --ignore-missing checksums.txt
gh attestation verify "vesperctl_${VERSION#v}_${os}_${arch}.tar.gz" --repo eventide-oss/vesper >/dev/null 2>&1 && echo "attestation: verified"
tar xzf "vesperctl_${VERSION#v}_${os}_${arch}.tar.gz" vesperctl; ./vesperctl version
img=$(kubectl -n vesper-system get deploy vesper-controller-manager -o jsonpath='{.spec.template.spec.containers[0].image}')
gh attestation verify "oci://$img" --repo eventide-oss/vesper >/dev/null 2>&1 && echo "image attestation: verified"
docker buildx imagetools inspect "$img" --format '{{json .Image}}' | jq -r 'to_entries[] | "\(.key) user=\(.value.config.User)"'
step "4. create from an image, Ready, served"
./vesperctl create demo --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 1h
kubectl wait --for=condition=Ready pe/demo --timeout=3m
NS=$(kubectl get pe demo -o jsonpath='{.status.namespace}'); echo "namespace: $NS"
kubectl get deploy,svc,resourcequota,limitrange,networkpolicy -n "$NS" --no-headers | awk '{print $1}'
(kubectl port-forward -n "$NS" svc/app 18080:80 >/dev/null 2>&1 &); sleep 3
curl -s http://localhost:18080 | grep -o "<title>.*</title>"; pkill -f "port-forward -n $NS" || true
step "5. validation by the API server"
./vesperctl create bad1 --image nginxinc/nginx-unprivileged:latest --port 8080; echo "exit $?"
./vesperctl create bad2 --image nginxinc/nginx-unprivileged:1.27-alpine --ttl 30s; echo "exit $?"
./vesperctl create bad3 --image nginxinc/nginx-unprivileged:1.27-alpine --replicas 6; echo "exit $?"
step "6. isolation"
kubectl get ns "$NS" -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}{"\n"}'
kubectl run priv -n "$NS" --image=nginxinc/nginx-unprivileged:1.27-alpine --privileged=true 2>&1 | tail -1
kubectl apply -n "$NS" -f - 2>&1 <<'YAML' | tail -1
apiVersion: v1
kind: Pod
metadata: {name: over-budget}
spec:
  containers:
  - name: app
    image: nginxinc/nginx-unprivileged:1.27-alpine
    resources: {limits: {memory: 5Gi, cpu: 500m}}
    securityContext: {allowPrivilegeEscalation: false, runAsNonRoot: true, capabilities: {drop: ["ALL"]}, seccompProfile: {type: RuntimeDefault}}
YAML
./vesperctl create other --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 1h >/dev/null
kubectl wait --for=condition=Ready pe/other --timeout=3m >/dev/null
O=$(kubectl get pe other -o jsonpath='{.status.namespace}'); sleep 5
kubectl exec -n "$NS" deploy/app -- wget -qO- -T 3 "http://app.$O" >/dev/null 2>&1; echo "preview -> other preview: exit $? (non-zero = blocked)"
kubectl exec -n "$NS" deploy/app -- wget -qO- -T 3 --no-check-certificate https://kubernetes.default/version >/dev/null 2>&1; echo "preview -> API server: exit $? (non-zero = blocked)"
kubectl exec -n "$NS" deploy/app -- wget -qO- -T 5 http://example.com >/dev/null 2>&1; echo "preview -> internet: exit $? (0 = allowed)"
step "7. lifecycle"
kubectl patch pe demo --type=merge -p '{"spec":{"image":"nginxinc/nginx-unprivileged:1.28-alpine"}}' >/dev/null
kubectl rollout status deploy/app -n "$NS" --timeout=3m >/dev/null; kubectl wait --for=condition=Ready pe/demo --timeout=3m >/dev/null
kubectl get deploy app -n "$NS" -o jsonpath='image now: {.spec.template.spec.containers[0].image}{"\n"}'
./vesperctl extend demo --by 2h
./vesperctl create short --image nginxinc/nginx-unprivileged:1.27-alpine --port 8080 --ttl 1m >/dev/null
S=""; for _ in $(seq 1 30); do S=$(kubectl get pe short -o jsonpath='{.status.namespace}' 2>/dev/null); [ -n "$S" ] && break; sleep 2; done; echo "short runs in $S"
kubectl wait --for=delete pe/short --timeout=4m && echo "short expired"
kubectl get ns "$S" 2>&1 | tail -1
./vesperctl delete other --yes
kubectl wait --for=delete ns/"$O" --timeout=3m && echo "namespace of other deleted"
step "8. status and events"
./vesperctl get demo
step "9. CLI outputs and exit codes"
./vesperctl list
./vesperctl list -o json | jq -r '.items[].name'
./vesperctl get demo -o yaml | grep -E '^(name|phase):'
./vesperctl get nope >/dev/null 2>&1; echo "get missing: exit $?"
./vesperctl list -o xml >/dev/null 2>&1; echo "bad flag: exit $?"
step "10. metrics"
kubectl create serviceaccount metrics-check -n default >/dev/null
kubectl create clusterrolebinding metrics-check --clusterrole=vesper-metrics-reader --serviceaccount=default:metrics-check >/dev/null
(kubectl port-forward -n vesper-system svc/vesper-controller-manager-metrics-service 18443:8443 >/dev/null 2>&1 &); sleep 3
curl -sk -o /dev/null -w "without token: HTTP %{http_code}\n" https://localhost:18443/metrics
TOKEN=$(kubectl create token metrics-check -n default)
curl -sk -H "Authorization: Bearer $TOKEN" https://localhost:18443/metrics | grep '^vesper_'
pkill -f "port-forward -n vesper-system" || true
step "11. no orphan namespace after cleanup"
./vesperctl delete demo --yes
kubectl wait --for=delete pe/demo --timeout=3m && echo "demo deleted"
echo "namespaces managed by vesper left: $(kubectl get ns -l app.kubernetes.io/managed-by=vesper --no-headers 2>/dev/null | wc -l | tr -d ' ')"
step "end: the cluster is deleted"
