// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func TestPublicHostAddresses(t *testing.T) {
	got := publicHostAddresses([]string{
		"203.0.113.20", "10.0.0.5", "203.0.113.7", "203.0.113.20", // public, private, public, duplicate
		"192.168.1.1", "172.20.0.1", "100.64.0.1", "169.254.169.254", // private and link-local ranges
		"127.0.0.1", "::1", "0.0.0.0", // loopback and unspecified
		"2001:db8::1", "fd00::1", "fe80::1", // public IPv6, ULA, link-local
		"::ffff:198.51.100.9", // IPv4-mapped: an IPv4 address
		"not-an-ip", "",
	})
	want := hostAddresses{
		ipv4: []string{"198.51.100.9/32", "203.0.113.20/32", "203.0.113.7/32"},
		ipv6: []string{"2001:db8::1/128"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("publicHostAddresses = %+v, want %+v", got, want)
	}
	if empty := publicHostAddresses(nil); empty.ipv4 != nil || empty.ipv6 != nil {
		t.Errorf("publicHostAddresses(nil) = %+v, want nothing", empty)
	}
}

func newNode(name string, addrs ...corev1.NodeAddress) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     corev1.NodeStatus{Addresses: addrs},
	}
}

func newAPIServerSlice(name string, addrs ...string) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default", Name: name,
			Labels: map[string]string{discoveryv1.LabelServiceName: "kubernetes"},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   []discoveryv1.Endpoint{{Addresses: addrs}},
	}
}

// TestReconcileDeniesHostAddresses runs a preview on a cluster whose node and
// API server have public addresses, as on a server with k3s: the policy
// denies them, next to the private ranges. The Hostname address, the private
// one and the endpoints of other Services are left out.
func TestReconcileDeniesHostAddresses(t *testing.T) {
	other := newAPIServerSlice("web", "198.51.100.50")
	other.Labels[discoveryv1.LabelServiceName] = "web"
	r := newReconciler(t, newPreview(),
		newNode("server",
			corev1.NodeAddress{Type: corev1.NodeHostName, Address: "server"},
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "10.0.0.5"},
			corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.20"},
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: "2001:db8::20"},
		),
		newAPIServerSlice("kubernetes", "203.0.113.7"),
		other,
	)
	reconcileOnce(t, r)

	want := wantNetworkPolicySpec()
	egress := want.Egress[2].To
	egress[0].IPBlock.Except = append(egress[0].IPBlock.Except, "203.0.113.20/32", "203.0.113.7/32")
	egress[1].IPBlock.Except = append(egress[1].IPBlock.Except, "2001:db8::20/128")
	if got := getNetworkPolicy(t, r).Spec; !reflect.DeepEqual(got, want) {
		t.Errorf("networkpolicy spec = %+v, want %+v", got, want)
	}
}

// TestReconcileFollowsNodeAddresses changes the public address of the node:
// the next reconciliation replaces it in the policy.
func TestReconcileFollowsNodeAddresses(t *testing.T) {
	node := newNode("server", corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.20"})
	r := newReconciler(t, newPreview(), node)
	reconcileOnce(t, r)

	node.Status.Addresses[0].Address = "203.0.113.99"
	// The addresses are in the status of the node, written by the kubelet.
	if err := r.Status().Update(context.Background(), node); err != nil {
		t.Fatalf("update node status: %v", err)
	}
	reconcileOnce(t, r)

	except := getNetworkPolicy(t, r).Spec.Egress[2].To[0].IPBlock.Except
	if !slices.Contains(except, "203.0.113.99/32") || slices.Contains(except, "203.0.113.20/32") {
		t.Errorf("except = %v, want the new address of the node and not the old one", except)
	}
}

func TestAllPreviews(t *testing.T) {
	second := newPreview()
	second.Namespace, second.Name = "team-b", "pr-7"
	r := newReconciler(t, newPreview(), second)

	var got []types.NamespacedName
	for _, req := range r.allPreviews(context.Background(), nil) {
		got = append(got, req.NamespacedName)
	}
	slices.SortFunc(got, func(a, b types.NamespacedName) int { return strings.Compare(a.String(), b.String()) })
	want := []types.NamespacedName{{Namespace: "team-a", Name: "pr-42"}, {Namespace: "team-b", Name: "pr-7"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("allPreviews = %v, want %v", got, want)
	}
}

// TestNodeAddressesChanged: the kubelet updates the status of its node every
// few seconds; only a change of its addresses reconciles the previews.
func TestNodeAddressesChanged(t *testing.T) {
	old := newNode("server", corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: "203.0.113.20"})
	heartbeat := old.DeepCopy()
	heartbeat.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	moved := old.DeepCopy()
	moved.Status.Addresses[0].Address = "203.0.113.99"

	if nodeAddressesChanged.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: heartbeat}) {
		t.Error("a heartbeat of the node reconciles the previews, want it ignored")
	}
	if !nodeAddressesChanged.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: moved}) {
		t.Error("a new address of the node is ignored, want the previews reconciled")
	}
	if !nodeAddressesChanged.Create(event.CreateEvent{Object: old}) || !nodeAddressesChanged.Delete(event.DeleteEvent{Object: old}) {
		t.Error("the creation or deletion of a node is ignored, want the previews reconciled")
	}
}

func TestAPIServerEndpointsChanged(t *testing.T) {
	old := newAPIServerSlice("kubernetes", "203.0.113.7")
	relabelled := old.DeepCopy()
	relabelled.Annotations = map[string]string{"note": "x"}
	moved := newAPIServerSlice("kubernetes", "203.0.113.8")

	if apiServerEndpointsChanged.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: relabelled}) {
		t.Error("an update that keeps the endpoints reconciles the previews, want it ignored")
	}
	if !apiServerEndpointsChanged.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: moved}) {
		t.Error("new endpoints of the API server are ignored, want the previews reconciled")
	}
	if !isAPIServerSlice(old) {
		t.Error("the EndpointSlice of the API server is not selected")
	}
	web := newAPIServerSlice("web", "198.51.100.50")
	web.Labels[discoveryv1.LabelServiceName] = "web"
	elsewhere := newAPIServerSlice("kubernetes", "203.0.113.7")
	elsewhere.Namespace = "team-a"
	if isAPIServerSlice(web) || isAPIServerSlice(elsewhere) {
		t.Error("an EndpointSlice of another Service is selected")
	}
}
