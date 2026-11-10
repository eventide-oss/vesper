// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// The EndpointSlices of the Service "kubernetes" in "default" list the
// addresses of the API server.
const (
	apiServerNamespace = metav1.NamespaceDefault
	apiServerService   = "kubernetes"
)

// hostAddresses are the addresses of the cluster itself that a preview may
// not reach, beyond the private ranges: the addresses of the nodes, where
// the kubelet and NodePort Services listen, and those of the API server.
// They matter when they are public, as on a server with k3s: the egress rule
// allows the public internet, and a connection to the "kubernetes" Service
// reaches the API server through the public address of the node.
type hostAddresses struct{ ipv4, ipv6 []string }

// hostAddresses reads the addresses of the nodes and of the API server, and
// returns those outside the private ranges, as /32 and /128 blocks, sorted
// and without duplicates, so that the policy does not change from one read
// to the next.
func (r *PreviewEnvironmentReconciler) hostAddresses(ctx context.Context) (hostAddresses, error) {
	var addrs []string

	var nodes corev1.NodeList
	if err := r.List(ctx, &nodes); err != nil {
		return hostAddresses{}, fmt.Errorf("list nodes: %w", err)
	}
	for _, node := range nodes.Items {
		for _, a := range node.Status.Addresses {
			if a.Type == corev1.NodeInternalIP || a.Type == corev1.NodeExternalIP {
				addrs = append(addrs, a.Address)
			}
		}
	}

	var slicesList discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slicesList, client.InNamespace(apiServerNamespace),
		client.MatchingLabels{discoveryv1.LabelServiceName: apiServerService}); err != nil {
		return hostAddresses{}, fmt.Errorf("list the endpoints of the API server: %w", err)
	}
	for _, s := range slicesList.Items {
		for _, ep := range s.Endpoints {
			addrs = append(addrs, ep.Addresses...)
		}
	}

	return publicHostAddresses(addrs), nil
}

// privatePrefixes are privateNetworks, parsed once.
var privatePrefixes = func() []netip.Prefix {
	var ps []netip.Prefix
	for _, s := range slices.Concat(privateNetworks.ipv4, privateNetworks.ipv6) {
		ps = append(ps, netip.MustParsePrefix(s))
	}
	return ps
}()

// publicHostAddresses keeps the addresses that no private range already
// covers, as single-address blocks, sorted and without duplicates. Addresses
// that do not parse are skipped: the API server validates them, so it does
// not happen.
func publicHostAddresses(addrs []string) hostAddresses {
	var h hostAddresses
	for _, s := range addrs {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		// The loopback address of a pod is its own: no policy applies to it.
		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if slices.ContainsFunc(privatePrefixes, func(p netip.Prefix) bool { return p.Contains(ip) }) {
			continue
		}
		block := netip.PrefixFrom(ip, ip.BitLen()).String()
		if ip.Is4() {
			h.ipv4 = append(h.ipv4, block)
		} else {
			h.ipv6 = append(h.ipv6, block)
		}
	}
	slices.Sort(h.ipv4)
	slices.Sort(h.ipv6)
	h.ipv4 = slices.Compact(h.ipv4)
	h.ipv6 = slices.Compact(h.ipv6)
	return h
}

// allPreviews maps any event to a reconciliation of every preview: the host
// addresses are in the policy of each of them.
func (r *PreviewEnvironmentReconciler) allPreviews(ctx context.Context, _ client.Object) []reconcile.Request {
	var list vesperv1alpha1.PreviewEnvironmentList
	if err := r.List(ctx, &list); err != nil {
		// The next change of a preview reconciles it anyway; nothing to
		// return the error to.
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for _, p := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&p)})
	}
	return reqs
}

// nodeAddressesChanged lets through the creation and deletion of a node, and
// the updates that change its addresses: the kubelet updates the status of
// its node every few seconds, and that must not reconcile every preview.
var nodeAddressesChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldNode, okOld := e.ObjectOld.(*corev1.Node)
		newNode, okNew := e.ObjectNew.(*corev1.Node)
		return !okOld || !okNew || !equality.Semantic.DeepEqual(oldNode.Status.Addresses, newNode.Status.Addresses)
	},
}

// isAPIServerSlice selects the EndpointSlices of the API server.
func isAPIServerSlice(o client.Object) bool {
	return o.GetNamespace() == apiServerNamespace && o.GetLabels()[discoveryv1.LabelServiceName] == apiServerService
}

// apiServerEndpointsChanged lets through the updates of an EndpointSlice of
// the API server that change its endpoints.
var apiServerEndpointsChanged = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		oldSlice, okOld := e.ObjectOld.(*discoveryv1.EndpointSlice)
		newSlice, okNew := e.ObjectNew.(*discoveryv1.EndpointSlice)
		return !okOld || !okNew || !equality.Semantic.DeepEqual(oldSlice.Endpoints, newSlice.Endpoints)
	},
}

// CacheByObject restricts the cache of the manager to the objects the
// operator reads: of the EndpointSlices of the cluster, only those of the API
// server. Pass it as cache.Options.ByObject.
func CacheByObject() map[client.Object]cache.ByObject {
	return map[client.Object]cache.ByObject{
		&discoveryv1.EndpointSlice{}: {
			Namespaces: map[string]cache.Config{apiServerNamespace: {}},
			Label:      labels.SelectorFromSet(labels.Set{discoveryv1.LabelServiceName: apiServerService}),
		},
	}
}
