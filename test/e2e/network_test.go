// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
	"github.com/eventide-oss/vesper/internal/controller"
)

const (
	// probeTimeout bounds one connection attempt of a probe.
	probeTimeout = 5 * time.Second
	// probeDelay is how long a probe waits before it connects. kindnet
	// matches a packet to its pod through a cache of pods: a pod that
	// connects as soon as it starts is not in it yet, and its packets are not
	// filtered.
	probeDelay = 5 * time.Second
	// policyTimeout bounds the wait for the CNI to apply a change: kindnet
	// enforces NetworkPolicies, but not at once.
	policyTimeout = time.Minute
	// kubeletPort is the port of the API of the kubelet, on every node.
	kubeletPort = 10250
	// internetAddress is an address on the internet that answers on 443
	// (Cloudflare's public DNS): previews must still reach the internet.
	internetAddress = "1.1.1.1"
)

// TestPreviewNetworkIsolation checks that the NetworkPolicy of a preview is
// enforced (ADR-0005): the preview is reachable from a namespace that is not
// a preview, such as the one of an ingress controller, and from itself, but
// not from another preview; it cannot reach the private network of the
// cluster, nor the API server and the kubelets through the addresses of the
// nodes (GHSA-m437-fj8p-6x8h), and it reaches the internet. Each blocked
// target is first reached from the namespace of the test, so that a probe
// that cannot connect at all does not pass for a blocked one.
//
// It runs with every CNI plugin of the isolation job of CI (E2E_CNI in
// hack/test-e2e.sh): each plugin enforces the policy in its own way.
func TestPreviewNetworkIsolation(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "vesper-e2e-"}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	preview := &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "pr-net"},
		Spec:       vesperv1alpha1.PreviewEnvironmentSpec{Image: "nginxinc/nginx-unprivileged:1.27", Port: 8080},
	}
	previewNamespace := controller.NamespaceName(preview.Namespace, preview.Name)
	t.Cleanup(func() { cleanup(t, c, ns.Name, previewNamespace) })
	if err := c.Create(ctx, preview); err != nil {
		t.Fatalf("create preview: %v", err)
	}
	waitForReady(t, c, preview)

	app := controller.ServiceName + "." + previewNamespace + ".svc.cluster.local"
	expectReachable(t, c, ns.Name, app, 80, true)
	// The pods of the preview reach each other, and resolve names.
	expectReachable(t, c, previewNamespace, app, 80, true)
	// The rest of the cluster is on private addresses: the API server is
	// reachable from the namespace of the test, not from the preview.
	const apiServer = "kubernetes.default.svc.cluster.local"
	expectReachable(t, c, ns.Name, apiServer, 443, true)
	expectReachable(t, c, previewNamespace, apiServer, 443, false)

	// The API server and the kubelet also answer on the addresses of the
	// nodes, which may be public; a CNI may match them by identity rather
	// than by address.
	endpoint, endpointPort := apiServerEndpoint(t, c)
	expectReachable(t, c, ns.Name, endpoint, endpointPort, true)
	expectReachable(t, c, previewNamespace, endpoint, endpointPort, false)
	node := nodeAddress(t, c)
	expectReachable(t, c, ns.Name, node, kubeletPort, true)
	expectReachable(t, c, previewNamespace, node, kubeletPort, false)

	// The internet stays reachable.
	expectReachable(t, c, previewNamespace, internetAddress, 443, true)

	// The namespace of the test now looks like another preview, with no
	// NetworkPolicy of its own: only the policy of the preview stops it.
	ns.Labels = map[string]string{controller.LabelManagedBy: controller.ManagedByVesper}
	if err := c.Update(ctx, ns); err != nil {
		t.Fatalf("label namespace: %v", err)
	}
	expectReachable(t, c, ns.Name, app, 80, false)
}

// apiServerEndpoint returns an address of the API server and its port, as the
// EndpointSlices of the kubernetes Service list them: the real address, not
// the virtual one of the Service.
func apiServerEndpoint(t *testing.T, c client.Client) (string, int) {
	t.Helper()
	var slices discoveryv1.EndpointSliceList
	if err := c.List(context.Background(), &slices, client.InNamespace(metav1.NamespaceDefault),
		client.MatchingLabels{discoveryv1.LabelServiceName: "kubernetes"}); err != nil {
		t.Fatalf("list the endpoints of the API server: %v", err)
	}
	for _, slice := range slices.Items {
		if len(slice.Ports) == 0 || slice.Ports[0].Port == nil {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if len(endpoint.Addresses) > 0 {
				return endpoint.Addresses[0], int(*slice.Ports[0].Port)
			}
		}
	}
	t.Fatal("the kubernetes Service has no endpoint")
	return "", 0
}

// nodeAddress returns the internal address of a node of the cluster.
func nodeAddress(t *testing.T, c client.Client) string {
	t.Helper()
	var nodes corev1.NodeList
	if err := c.List(context.Background(), &nodes); err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == corev1.NodeInternalIP {
				return address.Address
			}
		}
	}
	t.Fatal("no node has an internal address")
	return ""
}

// expectReachable runs probes in namespace until one connects to host:port,
// or fails to, as want says, within policyTimeout.
func expectReachable(t *testing.T, c client.Client, namespace, host string, port int, want bool) {
	t.Helper()
	eventually(t, policyTimeout, func() error {
		got, err := probe(t, c, namespace, host, port)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("from namespace %s, %s:%d reachable = %v, want %v", namespace, host, port, got, want)
		}
		return nil
	})
}

// probe runs a pod in namespace that opens a TCP connection to host:port, and
// reports whether it connected. The pod meets the restricted Pod Security
// profile, which preview namespaces enforce.
func probe(t *testing.T, c client.Client, namespace, host string, port int) (bool, error) {
	t.Helper()
	ctx := context.Background()
	script := fmt.Sprintf("sleep %d; nc -z -w %d %s %d",
		int(probeDelay.Seconds()), int(probeTimeout.Seconds()), host, port)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, GenerateName: "probe-"},
		Spec: corev1.PodSpec{
			RestartPolicy:                corev1.RestartPolicyNever,
			AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   ptr.To(true),
				RunAsUser:      ptr.To[int64](65534),
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{{
				Name:    "probe",
				Image:   "busybox:1.36",
				Command: []string{"sh", "-c", script},
				SecurityContext: &corev1.SecurityContext{
					AllowPrivilegeEscalation: ptr.To(false),
					Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				},
			}},
		},
	}
	if err := c.Create(ctx, pod); err != nil {
		return false, fmt.Errorf("create probe: %w", err)
	}
	defer func() {
		if err := c.Delete(ctx, pod); err != nil {
			t.Logf("delete probe %s/%s: %v", namespace, pod.Name, err)
		}
	}()

	var phase corev1.PodPhase
	deadline := time.Now().Add(readyTimeout) // the node may pull the image
	for {
		if err := c.Get(ctx, client.ObjectKeyFromObject(pod), pod); err != nil {
			return false, err
		}
		phase = pod.Status.Phase
		if phase == corev1.PodSucceeded || phase == corev1.PodFailed {
			return phase == corev1.PodSucceeded, nil
		}
		if time.Now().After(deadline) {
			return false, fmt.Errorf("probe %s/%s still %s after %s", namespace, pod.Name, phase, readyTimeout)
		}
		time.Sleep(pollInterval)
	}
}
