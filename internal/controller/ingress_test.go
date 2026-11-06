package controller

import (
	"context"
	"maps"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

const testDomain = "preview.example.com"

// newIngressReconciler returns a reconciler with a preview domain and an
// ingress class.
func newIngressReconciler(t *testing.T) *PreviewEnvironmentReconciler {
	t.Helper()
	r := newReconciler(t, newPreview())
	r.PreviewDomain = testDomain
	r.IngressClassName = "traefik"
	return r
}

func getIngress(t *testing.T, r *PreviewEnvironmentReconciler) (*networkingv1.Ingress, error) {
	t.Helper()
	var ing networkingv1.Ingress
	key := types.NamespacedName{Namespace: NamespaceName("team-a", "pr-42"), Name: IngressName}
	return &ing, r.Get(context.Background(), key, &ing)
}

func TestReconcileCreatesIngress(t *testing.T) {
	r := newIngressReconciler(t)
	reconcileOnce(t, r)

	ing, err := getIngress(t, r)
	if err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if !ownedBy(ing, newPreview()) || len(ing.OwnerReferences) != 0 {
		t.Errorf("ingress metadata = %+v, want the preview labels and no owner reference", ing.ObjectMeta)
	}
	if got := ptr.Deref(ing.Spec.IngressClassName, ""); got != "traefik" {
		t.Errorf("ingressClassName = %q, want traefik", got)
	}
	host := HostName("team-a", "pr-42", testDomain)
	want := []networkingv1.IngressRule{{
		Host: host,
		IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
			Paths: []networkingv1.HTTPIngressPath{{
				Path:     "/",
				PathType: ptr.To(networkingv1.PathTypePrefix),
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
					Name: "app", Port: networkingv1.ServiceBackendPort{Name: "http"},
				}},
			}},
		}},
	}}
	if !reflect.DeepEqual(ing.Spec.Rules, want) {
		t.Errorf("rules = %+v, want %+v", ing.Spec.Rules, want)
	}
	if got := getPreview(t, r).Status.URL; got != "http://"+host {
		t.Errorf("url = %q, want http://%s", got, host)
	}
}

func TestReconcileWithoutDomainCreatesNoIngress(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	if _, err := getIngress(t, r); !apierrors.IsNotFound(err) {
		t.Errorf("get ingress: %v, want NotFound", err)
	}
	if got := getPreview(t, r).Status.URL; got != "" {
		t.Errorf("url = %q, want empty", got)
	}
}

// TestReconcileDeletesIngressWhenDomainRemoved restarts the operator without
// a preview domain: the Ingress it created goes, and so does the URL.
func TestReconcileDeletesIngressWhenDomainRemoved(t *testing.T) {
	r := newIngressReconciler(t)
	reconcileOnce(t, r)

	r.PreviewDomain = ""
	reconcileOnce(t, r)
	if _, err := getIngress(t, r); !apierrors.IsNotFound(err) {
		t.Errorf("get ingress: %v, want NotFound", err)
	}
	if got := getPreview(t, r).Status.URL; got != "" {
		t.Errorf("url = %q, want empty", got)
	}
}

// TestReconcileKeepsDefaultIngressClass leaves the class empty: the class the
// cluster fills in is kept, and nothing is written again.
func TestReconcileKeepsDefaultIngressClass(t *testing.T) {
	r := newIngressReconciler(t)
	r.IngressClassName = ""
	reconcileOnce(t, r)

	ing, err := getIngress(t, r)
	if err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if ing.Spec.IngressClassName != nil {
		t.Errorf("ingressClassName = %q, want none", *ing.Spec.IngressClassName)
	}
	ing.Spec.IngressClassName = ptr.To("cluster-default") // as the admission plugin does
	if err := r.Update(context.Background(), ing); err != nil {
		t.Fatalf("update ingress: %v", err)
	}
	ing, _ = getIngress(t, r)
	before := ing.ResourceVersion

	reconcileOnce(t, r)
	ing, _ = getIngress(t, r)
	if ing.ResourceVersion != before || ptr.Deref(ing.Spec.IngressClassName, "") != "cluster-default" {
		t.Errorf("ingress rewritten (class %v, resourceVersion %s then %s), want it kept",
			ing.Spec.IngressClassName, before, ing.ResourceVersion)
	}
}

func TestReconcileLeavesForeignIngressUntouched(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	foreign := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{
		Namespace: nsName, Name: IngressName, Labels: map[string]string{"owner": "someone"},
	}}
	ns := newPreviewNamespace(nsName)
	for _, domain := range []string{testDomain, ""} {
		t.Run("domain "+domain, func(t *testing.T) {
			r := newReconciler(t, newPreview(), ns.DeepCopy(), foreign.DeepCopy())
			r.PreviewDomain = domain
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
			_, err := r.Reconcile(context.Background(), req)
			if domain != "" && err == nil {
				t.Error("Reconcile succeeded, want an error for an ingress Vesper did not create")
			}
			ing, getErr := getIngress(t, r)
			if getErr != nil {
				t.Fatalf("get ingress: %v, want the foreign ingress kept", getErr)
			}
			if !maps.Equal(ing.Labels, foreign.Labels) || len(ing.Spec.Rules) != 0 {
				t.Errorf("foreign ingress modified: %+v", ing)
			}
		})
	}
}

// newPreviewNamespace returns the namespace Vesper creates for newPreview.
func newPreviewNamespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: previewLabels(newPreview())}}
}
