package controller

import (
	"context"
	"errors"
	"maps"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// deletePreview deletes the preview: with the finalizer, the fake client, like
// the API server, only sets its deletionTimestamp.
func deletePreview(t *testing.T, r *PreviewEnvironmentReconciler) {
	t.Helper()
	if err := r.Delete(context.Background(), getPreview(t, r)); err != nil {
		t.Fatalf("delete preview: %v", err)
	}
}

func previewExists(t *testing.T, r *PreviewEnvironmentReconciler) bool {
	t.Helper()
	err := r.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "pr-42"}, newPreview())
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get preview: %v", err)
	}
	return err == nil
}

func namespaceExists(t *testing.T, r *PreviewEnvironmentReconciler, name string) bool {
	t.Helper()
	err := r.Get(context.Background(), types.NamespacedName{Name: name}, &corev1.Namespace{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Fatalf("get namespace %s: %v", name, err)
	}
	return err == nil
}

func TestReconcileAddsFinalizer(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	if p := getPreview(t, r); !controllerutil.ContainsFinalizer(p, FinalizerName) {
		t.Errorf("finalizers = %v, want %s", p.Finalizers, FinalizerName)
	}
}

// TestDeletionDeletesNamespaceThenPreview follows a deletion: the first
// reconciliation deletes the preview namespace and keeps the finalizer, the
// next one sees the namespace gone and removes it, and the preview goes.
func TestDeletionDeletesNamespaceThenPreview(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	nsName := NamespaceName("team-a", "pr-42")

	deletePreview(t, r)
	reconcileOnce(t, r)
	if namespaceExists(t, r, nsName) {
		t.Fatal("preview namespace still exists, want it deleted")
	}
	if p := getPreview(t, r); !controllerutil.ContainsFinalizer(p, FinalizerName) {
		t.Fatal("finalizer removed in the same reconciliation as the namespace deletion: want it kept until the namespace is gone")
	}

	reconcileOnce(t, r) // brought back by the watch on namespaces
	if previewExists(t, r) {
		t.Errorf("preview still exists: %+v", getPreview(t, r).ObjectMeta)
	}
}

// TestDeletionWaitsForTerminatingNamespace checks that the finalizer stays
// while the preview namespace is still being deleted.
func TestDeletionWaitsForTerminatingNamespace(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	preview := newPreview()
	preview.Finalizers = []string{FinalizerName}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: nsName, Labels: previewLabels(preview),
		// Another finalizer keeps the namespace in the Terminating state.
		Finalizers: []string{"example.com/still-deleting"},
	}}
	r := newReconciler(t, preview, ns)
	if err := r.Delete(context.Background(), ns); err != nil {
		t.Fatalf("delete namespace: %v", err)
	}
	deletePreview(t, r)

	for range 2 { // idempotent: nothing changes while the namespace terminates
		reconcileOnce(t, r)
		if !previewExists(t, r) || !controllerutil.ContainsFinalizer(getPreview(t, r), FinalizerName) {
			t.Fatal("preview gone or finalizer removed while its namespace is still terminating")
		}
	}
}

func TestDeletionKeepsForeignNamespace(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: nsName, Labels: map[string]string{"owner": "someone"},
	}}
	r := newReconciler(t, newPreview(), foreign)
	reconcileOnce(t, r) // NamespaceConflict, with the finalizer

	deletePreview(t, r)
	reconcileOnce(t, r)
	if previewExists(t, r) {
		t.Error("preview still exists, want it deleted")
	}
	ns := getNamespace(t, r, nsName)
	if !maps.Equal(ns.Labels, foreign.Labels) || !ns.DeletionTimestamp.IsZero() {
		t.Errorf("foreign namespace changed or deleted: %+v", ns.ObjectMeta)
	}
}

// TestDeletionErrorKeepsFinalizer makes the namespace deletion fail: the
// error is returned, so the reconciliation is retried, and the preview keeps
// its finalizer.
func TestDeletionErrorKeepsFinalizer(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	deletePreview(t, r)

	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Delete: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			if _, ok := obj.(*corev1.Namespace); ok {
				return errors.New("API server unavailable")
			}
			return c.Delete(ctx, obj, opts...)
		},
	})
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("Reconcile succeeded, want the deletion error so that it is retried")
	}
	if !controllerutil.ContainsFinalizer(getPreview(t, r), FinalizerName) {
		t.Error("finalizer removed although the cleanup failed")
	}
	if !namespaceExists(t, r, NamespaceName("team-a", "pr-42")) {
		t.Error("namespace gone, want it kept")
	}
}

// TestDeletionDoesNotRecreateObjects checks that a preview being deleted is
// only cleaned up: nothing is created for it again.
func TestDeletionDoesNotRecreateObjects(t *testing.T) {
	preview := newPreview()
	preview.Finalizers = []string{"example.com/other"} // keeps it after Vesper's cleanup
	r := newReconciler(t, preview)
	reconcileOnce(t, r)
	deletePreview(t, r)
	reconcileOnce(t, r)
	reconcileOnce(t, r)

	if namespaceExists(t, r, NamespaceName("team-a", "pr-42")) {
		t.Error("preview namespace exists, want it deleted and not created again")
	}
	p := getPreview(t, r)
	if controllerutil.ContainsFinalizer(p, FinalizerName) || !controllerutil.ContainsFinalizer(p, "example.com/other") {
		t.Errorf("finalizers = %v, want only example.com/other", p.Finalizers)
	}
}
