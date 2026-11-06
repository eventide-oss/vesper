package controller

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// IngressName is the name of the Ingress of a preview, in the preview
// namespace.
const IngressName = "app"

// ensureIngress gives the preview its public address: an Ingress that routes
// its host name to its Service. It returns the URL, or "" when the operator
// has no preview domain, in which case the preview has no Ingress.
//
// An Ingress only describes the route: the ingress controller of the cluster
// (Traefik, HAProxy...) serves it, and a wildcard DNS record for the preview
// domain must point to that controller.
func (r *PreviewEnvironmentReconciler) ensureIngress(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) (string, error) {
	if r.PreviewDomain == "" {
		return "", r.deleteIngress(ctx, preview, namespace)
	}
	host := HostName(preview.Namespace, preview.Name, r.PreviewDomain)

	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: IngressName, Namespace: namespace}}
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, ing, func() error {
		if ing.ResourceVersion != "" && !ownedBy(ing, preview) { // it already exists
			return fmt.Errorf("ingress %s/%s exists and was not created by Vesper for this preview",
				namespace, IngressName)
		}
		mutateIngress(ing, preview, host, r.IngressClassName)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("create or update ingress %s/%s: %w", namespace, IngressName, err)
	}
	if result != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info("ingress "+string(result), "previewNamespace", namespace, "host", host)
	}
	// HTTPS is #100.
	return "http://" + host, nil
}

// mutateIngress sets the fields Vesper owns on ing: one rule from host to the
// Service of the preview.
func mutateIngress(ing *networkingv1.Ingress, preview *vesperv1alpha1.PreviewEnvironment, host, class string) {
	setPreviewLabels(ing, preview)

	// Without a class, the cluster may fill in its default one: keep it rather
	// than erase it at every reconciliation.
	if class != "" {
		ing.Spec.IngressClassName = ptr.To(class)
	}
	ing.Spec.Rules = []networkingv1.IngressRule{{
		Host: host,
		IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{
				Path:     "/",
				PathType: ptr.To(networkingv1.PathTypePrefix),
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
					Name: ServiceName,
					Port: networkingv1.ServiceBackendPort{Name: portName},
				}},
			}},
		}},
	}}
}

// deleteIngress deletes the Ingress Vesper created for the preview, for
// example after the preview domain was removed from the operator. An Ingress
// Vesper did not create is left alone.
func (r *PreviewEnvironmentReconciler) deleteIngress(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) error {
	var ing networkingv1.Ingress
	err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: IngressName}, &ing)
	if apierrors.IsNotFound(err) || (err == nil && !ownedBy(&ing, preview)) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get ingress %s/%s: %w", namespace, IngressName, err)
	}
	if err := r.Delete(ctx, &ing, client.Preconditions{UID: &ing.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete ingress %s/%s: %w", namespace, IngressName, err)
	}
	logf.FromContext(ctx).Info("ingress deleted: no preview domain", "previewNamespace", namespace)
	return nil
}
