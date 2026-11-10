// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"slices"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// NetworkPolicyName is the name of the NetworkPolicy of a preview, in the
// preview namespace.
const NetworkPolicyName = "preview"

// privateNetworks are the destinations a preview may not reach (ADR-0005):
// the private ranges, where the pods, the Services and the nodes of the
// cluster live, the link-local ranges, where clouds serve the credentials of
// the nodes (169.254.169.254), and the shared address space of carriers.
var privateNetworks = struct{ ipv4, ipv6 []string }{
	ipv4: []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10"},
	ipv6: []string{"fc00::/7", "fe80::/10"},
}

// ensureNetworkPolicy isolates the preview on the network (ADR-0005). Like
// the limits, it applies to pods as they start, so it is created before the
// Deployment: no pod of the preview runs without it.
func (r *PreviewEnvironmentReconciler) ensureNetworkPolicy(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) error {
	hosts, err := r.hostAddresses(ctx)
	if err != nil {
		// Without the addresses of the cluster, the policy could let the
		// preview reach them: no policy, so no Deployment either.
		return err
	}
	np := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: NetworkPolicyName, Namespace: namespace}}
	return r.ensureOwned(ctx, preview, np, "networkpolicy", func() { mutateNetworkPolicy(np, preview, hosts) })
}

// mutateNetworkPolicy denies all traffic to and from the pods of the preview,
// then allows:
//   - in: the port of the application, from the pods of the preview and from
//     every namespace that is not a preview, such as the one of the ingress
//     controller. Previews cannot reach each other;
//   - out: the pods of the preview, DNS, and the public internet, but not the
//     private networks, nor the public addresses of the nodes and of the API
//     server (hosts).
func mutateNetworkPolicy(np *networkingv1.NetworkPolicy, preview *vesperv1alpha1.PreviewEnvironment, hosts hostAddresses) {
	setPreviewLabels(np, preview)

	tcp, udp := corev1.ProtocolTCP, corev1.ProtocolUDP
	dns := intstr.FromInt32(53)
	np.Spec = networkingv1.NetworkPolicySpec{
		PodSelector: metav1.LabelSelector{}, // every pod of the namespace
		PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		Ingress: []networkingv1.NetworkPolicyIngressRule{{
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: ptr.To(intstr.FromString(portName))}},
			From: []networkingv1.NetworkPolicyPeer{
				{PodSelector: &metav1.LabelSelector{}},
				{NamespaceSelector: &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key: LabelManagedBy, Operator: metav1.LabelSelectorOpNotIn, Values: []string{ManagedByVesper},
				}}}},
			},
		}},
		Egress: []networkingv1.NetworkPolicyEgressRule{
			// The pods of the preview: their addresses are private.
			{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{}}}},
			// To any address: the DNS server of the cluster has a private one.
			{Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}, {Protocol: &tcp, Port: &dns}}},
			{To: []networkingv1.NetworkPolicyPeer{
				{IPBlock: &networkingv1.IPBlock{CIDR: "0.0.0.0/0", Except: slices.Concat(privateNetworks.ipv4, hosts.ipv4)}},
				{IPBlock: &networkingv1.IPBlock{CIDR: "::/0", Except: slices.Concat(privateNetworks.ipv6, hosts.ipv6)}},
			}},
		},
	}
}
