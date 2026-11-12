#!/usr/bin/env bash
# End-to-end test of the operator: builds its image, runs it in a fresh kind
# cluster as it would run in production, and checks the life of previews,
# through the API and through vesperctl. Run it with `make test-e2e`.
#
# The cluster is created for the test and deleted at the end, even when the
# test fails. Set E2E_KEEP_CLUSTER=1 to keep it for debugging. Its credentials
# go to a kubeconfig of their own: your kubeconfig and current context are
# never changed.
#
# E2E_CNI chooses the network plugin of the cluster: kindnet (the default of
# kind), calico or cilium, at the versions pinned below; cilium needs helm.
# E2E_RUN limits the tests to those whose name matches it (go test -run), for
# example TestPreviewNetworkIsolation.
#
# When the test fails, the logs of the cluster are written to
# E2E_ARTIFACTS_DIR (default _artifacts/e2e), which CI uploads. They are
# public for a public repository: the cluster must never hold a secret.
set -euo pipefail

cluster="${E2E_CLUSTER:-vesper-e2e}"
context="kind-${cluster}"
image="vesper-manager:e2e" # must match test/e2e/kustomization.yaml
operator_namespace="vesper-system"
operator_deployment="vesper-controller-manager"
kustomize="${KUSTOMIZE:-bin/kustomize}"
artifacts="${E2E_ARTIFACTS_DIR:-_artifacts/e2e}"
cni="${E2E_CNI:-kindnet}"
run="${E2E_RUN:-.}"
calico_version="v3.33.0"
cilium_version="1.20.4"

case "$cni" in
kindnet | calico | cilium) ;;
*)
	echo "E2E_CNI must be kindnet, calico or cilium, not $cni" >&2
	exit 2
	;;
esac
if [ "$cni" = cilium ] && ! command -v helm >/dev/null; then
	echo "E2E_CNI=cilium needs helm: https://helm.sh/docs/intro/install/" >&2
	exit 2
fi

if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
	echo "kind cluster $cluster already exists: delete it with 'kind delete cluster --name $cluster'" >&2
	exit 1
fi

kubeconfig="$(mktemp -t vesper-e2e-kubeconfig.XXXXXX)"
export KUBECONFIG="$kubeconfig"
vesperctl="$(mktemp -d -t vesper-e2e-bin.XXXXXX)/vesperctl"
kind_config="$(mktemp -t vesper-e2e-kind.XXXXXX)"

k() { kubectl --context "$context" "$@"; }

# save_logs writes what helps to understand a failure to $artifacts.
save_logs() {
	mkdir -p "$artifacts"
	k -n "$operator_namespace" logs "deploy/$operator_deployment" --tail=-1 >"$artifacts/operator.log" 2>&1 || true
	k get previewenvironments -A -o yaml >"$artifacts/previews.yaml" 2>&1 || true
	k get namespaces --show-labels >"$artifacts/namespaces.txt" 2>&1 || true
	k get pods -A -o wide >"$artifacts/pods.txt" 2>&1 || true
	k get events -A --sort-by=.lastTimestamp >"$artifacts/events.txt" 2>&1 || true
	# Node, kubelet and container logs.
	kind export logs "$artifacts/kind" --name "$cluster" >/dev/null 2>&1 || true
	echo "logs saved to $artifacts" >&2
}

finish() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "--- e2e failed: operator logs and previews" >&2
		k -n "$operator_namespace" logs "deploy/$operator_deployment" --tail=200 >&2 || true
		k get previewenvironments -A -o wide >&2 || true
		k get events -A --sort-by=.lastTimestamp 2>&1 | tail -30 >&2 || true
		save_logs
	fi
	rm -rf -- "$(dirname "$vesperctl")" "$kind_config"
	if [ -z "${E2E_KEEP_CLUSTER:-}" ]; then
		kind delete cluster --name "$cluster" || true
		rm -f -- "$kubeconfig"
	else
		echo "keeping kind cluster $cluster: export KUBECONFIG=$kubeconfig" >&2
	fi
	exit "$status"
}
trap finish EXIT

echo "--- build vesperctl"
go build -o "$vesperctl" ./cmd/vesperctl

echo "--- build the operator image $image"
docker build -t "$image" .

echo "--- create the kind cluster $cluster, with $cni"
cp hack/kind-config.yaml "$kind_config"
if [ "$cni" != kindnet ]; then
	# Without kindnet the nodes stay NotReady until a CNI is installed. The
	# pod subnet is the default of Calico.
	printf 'networking:\n  disableDefaultCNI: true\n  podSubnet: 192.168.0.0/16\n' >>"$kind_config"
fi
kind create cluster --name "$cluster" --config "$kind_config"
kind load docker-image "$image" --name "$cluster"

case "$cni" in
calico)
	k apply -f "https://raw.githubusercontent.com/projectcalico/calico/${calico_version}/manifests/calico.yaml" >/dev/null
	k -n kube-system rollout status daemonset/calico-node --timeout=5m
	;;
cilium)
	helm install cilium cilium --repo https://helm.cilium.io --version "$cilium_version" \
		--kube-context "$context" --namespace kube-system \
		--set ipam.mode=kubernetes --set image.pullPolicy=IfNotPresent \
		--set operator.replicas=1 >/dev/null # one node: a second replica never schedules
	k -n kube-system rollout status daemonset/cilium --timeout=5m
	k -n kube-system rollout status deployment/cilium-operator --timeout=5m
	;;
esac
k wait --for=condition=Ready nodes --all --timeout=5m

echo "--- deploy the operator"
"$kustomize" build test/e2e | k apply -f -
k -n "$operator_namespace" rollout status "deploy/$operator_deployment" --timeout=3m

echo "--- run the e2e test"
VESPER_E2E_CONTEXT="$context" VESPER_E2E_VESPERCTL="$vesperctl" go test -count=1 -tags e2e -v -run "$run" ./test/e2e/...
