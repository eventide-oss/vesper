// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"maps"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func getNetworkPolicy(t *testing.T, r *PreviewEnvironmentReconciler) *networkingv1.NetworkPolicy {
	t.Helper()
	var np networkingv1.NetworkPolicy
	key := types.NamespacedName{Namespace: NamespaceName("team-a", "pr-42"), Name: NetworkPolicyName}
	if err := r.Get(context.Background(), key, &np); err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	return &np
}

// wantNetworkPolicySpec is written out in full, rather than built with the
// code under test, so that a change to the policy shows in the test.
func wantNetworkPolicySpec() networkingv1.NetworkPolicySpec {
	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	return networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{},
		PolicyTypes: []networkingv1.PolicyType{"Ingress", "Egress"},
		// Any port, from the pods of the preview only.
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}},
		}},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
			{Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: &udp, Port: ptr.To(intstr.FromInt32(53))},
				{Protocol: &tcp, Port: ptr.To(intstr.FromInt32(53))},
			}},
			{To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: []string{
					"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10",
				}}},
				{IPBlock: &networkingv1.IPBlock{CIDR: "::/0", Except: []string{"fc00::/7", "fe80::/10"}}},
			}},
		},
	}
}

// wantExposeNetworkPolicySpec is the policy of the exposed component, whose
// pods have the labels podLabels.
func wantExposeNetworkPolicySpec(podLabels map[string]string) networkingv1.NetworkPolicySpec {
	tcp := corev1.ProtocolTCP
	return networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{MatchLabels: podLabels},
		PolicyTypes: []networkingv1.PolicyType{"Ingress"},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr.To(intstr.FromString("http"))}},
			From: []networkingv1.NetworkPolicyPeer{
				{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: "app.kubernetes.io/managed-by", Operator: "NotIn", Values: []string{"vesper"},
				}}}},
			},
		}},
	}
}

func getExposeNetworkPolicy(t *testing.T, r *PreviewEnvironmentReconciler) *networkingv1.NetworkPolicy {
	t.Helper()
	var np networkingv1.NetworkPolicy
	key := types.NamespacedName{Namespace: NamespaceName("team-a", "pr-42"), Name: ExposeNetworkPolicyName}
	if err := r.Get(context.Background(), key, &np); err != nil {
		t.Fatalf("get networkpolicy %s: %v", ExposeNetworkPolicyName, err)
	}
	return &np
}

func TestReconcileCreatesNetworkPolicy(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	if got, want := getNetworkPolicy(t, r).Spec, wantNetworkPolicySpec(); !reflect.DeepEqual(got, want) {
		t.Errorf("networkpolicy spec = %+v, want %+v", got, want)
	}
	// A preview that gives one image exposes its pods, selected as in v0.2.
	want := wantExposeNetworkPolicySpec(map[string]string{"app.kubernetes.io/name": "preview-app"})
	np := getExposeNetworkPolicy(t, r)
	if !reflect.DeepEqual(np.Spec, want) {
		t.Errorf("expose networkpolicy spec = %+v, want %+v", np.Spec, want)
	}
	if !ownedBy(np, newPreview()) {
		t.Errorf("expose networkpolicy labels = %v, want the preview labels", np.Labels)
	}
}

// TestReconcileExposesOneComponent: with components, only the pods of the
// exposed component are let in from outside the preview, and the policy
// follows when another component is exposed.
func TestReconcileExposesOneComponent(t *testing.T) {
	r := newReconciler(t, newStackPreview())
	reconcileOnce(t, r)

	if got, want := getNetworkPolicy(t, r).Spec, wantNetworkPolicySpec(); !reflect.DeepEqual(got, want) {
		t.Errorf("networkpolicy spec = %+v, want %+v", got, want)
	}
	want := wantExposeNetworkPolicySpec(map[string]string{"vesper.eventide.dev/component": "web"})
	if got := getExposeNetworkPolicy(t, r).Spec; !reflect.DeepEqual(got, want) {
		t.Errorf("expose networkpolicy spec = %+v, want %+v", got, want)
	}

	preview := getPreview(t, r)
	preview.Spec.Components[0].Expose, preview.Spec.Components[1].Expose = false, true
	if err := r.Update(context.Background(), preview); err != nil {
		t.Fatalf("update preview: %v", err)
	}
	reconcileOnce(t, r)
	want = wantExposeNetworkPolicySpec(map[string]string{"vesper.eventide.dev/component": "api"})
	if got := getExposeNetworkPolicy(t, r).Spec; !reflect.DeepEqual(got, want) {
		t.Errorf("expose networkpolicy spec after exposing api = %+v, want %+v", got, want)
	}
}

// TestReconcileRestoresChangedNetworkPolicy opens the policies by hand, as a
// user who wants to reach a preview from another one might: the operator
// closes them again.
func TestReconcileRestoresChangedNetworkPolicy(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	np := getNetworkPolicy(t, r)
	np.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{}} // from anywhere, to any port
	np.Spec.Egress = nil
	if err := r.Update(context.Background(), np); err != nil {
		t.Fatalf("update networkpolicy: %v", err)
	}
	expose := getExposeNetworkPolicy(t, r)
	expose.Spec.PodSelector = metav1.LabelSelector{} // every pod reachable from outside
	if err := r.Update(context.Background(), expose); err != nil {
		t.Fatalf("update networkpolicy: %v", err)
	}
	reconcileOnce(t, r)
	if got, want := getNetworkPolicy(t, r).Spec, wantNetworkPolicySpec(); !reflect.DeepEqual(got, want) {
		t.Errorf("networkpolicy spec = %+v, want it restored to %+v", got, want)
	}
	want := wantExposeNetworkPolicySpec(map[string]string{"app.kubernetes.io/name": "preview-app"})
	if got := getExposeNetworkPolicy(t, r).Spec; !reflect.DeepEqual(got, want) {
		t.Errorf("expose networkpolicy spec = %+v, want it restored to %+v", got, want)
	}
}

func TestReconcileLeavesForeignNetworkPolicyUntouched(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	foreign := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{
		Namespace: nsName, Name: NetworkPolicyName, Labels: map[string]string{"owner": "someone"},
	}}
	r := newReconciler(t, newPreview(), newPreviewNamespace(nsName), foreign)
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Error("Reconcile succeeded, want an error for a networkpolicy Vesper did not create")
	}
	var got networkingv1.NetworkPolicy
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: nsName, Name: NetworkPolicyName}, &got); err != nil {
		t.Fatalf("get networkpolicy: %v, want the foreign one kept", err)
	}
	if !maps.Equal(got.Labels, foreign.Labels) || len(got.Spec.PolicyTypes) != 0 {
		t.Errorf("foreign networkpolicy modified: %+v", got)
	}
	// Without its policy, the preview must not run: no Deployment.
	err := r.Get(context.Background(), types.NamespacedName{Namespace: nsName, Name: DeploymentName}, &appsv1.Deployment{})
	if !apierrors.IsNotFound(err) {
		t.Error("deployment created although the networkpolicy is not Vesper's")
	}
}
