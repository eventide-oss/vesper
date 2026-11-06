#!/usr/bin/env bash
# End-to-end test of the operator: builds its image, runs it in a fresh kind
# cluster as it would run in production, and checks that a preview becomes
# Ready. Run it with `make test-e2e`.
#
# The cluster is created for the test and deleted at the end, even when the
# test fails. Set E2E_KEEP_CLUSTER=1 to keep it for debugging. Its credentials
# go to a kubeconfig of their own: your kubeconfig and current context are
# never changed.
set -euo pipefail

cluster="${E2E_CLUSTER:-vesper-e2e}"
context="kind-${cluster}"
image="vesper-manager:e2e" # must match test/e2e/kustomization.yaml
operator_namespace="vesper-system"
operator_deployment="vesper-controller-manager"
kustomize="${KUSTOMIZE:-bin/kustomize}"

if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
	echo "kind cluster $cluster already exists: delete it with 'kind delete cluster --name $cluster'" >&2
	exit 1
fi

kubeconfig="$(mktemp -t vesper-e2e-kubeconfig.XXXXXX)"
export KUBECONFIG="$kubeconfig"

k() { kubectl --context "$context" "$@"; }

finish() {
	status=$?
	if [ "$status" -ne 0 ]; then
		echo "--- e2e failed: operator logs and previews" >&2
		k -n "$operator_namespace" logs "deploy/$operator_deployment" --tail=200 >&2 || true
		k get previewenvironments -A -o wide >&2 || true
		k get events -A --sort-by=.lastTimestamp 2>&1 | tail -30 >&2 || true
	fi
	if [ -z "${E2E_KEEP_CLUSTER:-}" ]; then
		kind delete cluster --name "$cluster" || true
		rm -f -- "$kubeconfig"
	else
		echo "keeping kind cluster $cluster: export KUBECONFIG=$kubeconfig" >&2
	fi
	exit "$status"
}
trap finish EXIT

echo "--- build the operator image $image"
docker build -t "$image" .

echo "--- create the kind cluster $cluster"
kind create cluster --name "$cluster" --config hack/kind-config.yaml
kind load docker-image "$image" --name "$cluster"

echo "--- deploy the operator"
"$kustomize" build test/e2e | k apply -f -
k -n "$operator_namespace" rollout status "deploy/$operator_deployment" --timeout=3m

echo "--- run the e2e test"
VESPER_E2E_CONTEXT="$context" go test -count=1 -tags e2e -v ./test/e2e/...
