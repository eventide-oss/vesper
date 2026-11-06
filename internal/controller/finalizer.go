package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// FinalizerName is the finalizer Vesper puts on every preview. While it is
// there, deleting the preview only marks it as being deleted: the controller
// first deletes the preview namespace, then removes the finalizer, and the API
// server removes the preview. The objects of a preview cannot have it as owner
// (ADR-0002), so the garbage collector would never remove them.
const FinalizerName = "vesper.eventide.dev/cleanup"

// ensureFinalizer adds the finalizer to the preview if it is missing. It is
// called before anything is created for the preview, so that nothing is ever
// created without a guaranteed cleanup.
func (r *PreviewEnvironmentReconciler) ensureFinalizer(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment,
) error {
	if controllerutil.ContainsFinalizer(preview, FinalizerName) {
		return nil
	}
	return r.patchFinalizers(ctx, preview, func() { controllerutil.AddFinalizer(preview, FinalizerName) })
}

// cleanup runs while the preview is being deleted. It deletes the preview
// namespace, which deletes everything in it, and removes the finalizer once
// the namespace is gone. It can run any number of times: the watch on
// namespaces brings the preview back here when its namespace disappears, and
// an error is retried with a backoff, the finalizer kept.
func (r *PreviewEnvironmentReconciler) cleanup(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment,
) error {
	if !controllerutil.ContainsFinalizer(preview, FinalizerName) {
		return nil // cleaned up already, or never set up by Vesper
	}
	log := logf.FromContext(ctx)
	name := NamespaceName(preview.Namespace, preview.Name)

	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: name}, &ns)
	switch {
	case apierrors.IsNotFound(err):
		// Gone: nothing is left of the preview.
	case err != nil:
		return fmt.Errorf("get namespace %s: %w", name, err)
	case !ownedBy(&ns, preview):
		// A namespace Vesper did not create for this preview (a
		// NamespaceConflict): never delete it.
		log.Info("namespace was not created by Vesper for this preview: not deleting it", "previewNamespace", name)
	case ns.DeletionTimestamp.IsZero():
		if err := r.Delete(ctx, &ns, client.Preconditions{UID: &ns.UID}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete namespace %s: %w", name, err)
		}
		log.Info("deleting namespace", "previewNamespace", name)
		return nil // wait until it is gone
	default:
		return nil // still terminating: wait until it is gone
	}

	if err := r.patchFinalizers(ctx, preview, func() { controllerutil.RemoveFinalizer(preview, FinalizerName) }); err != nil {
		return err
	}
	log.Info("cleanup done", "previewNamespace", name)
	return nil
}

// patchFinalizers applies change to the finalizers of the preview with a
// merge patch. A merge patch replaces the whole list, so the patch carries
// the resourceVersion read: if the preview changed in between, for example
// another tool added its own finalizer, the API server rejects the patch
// instead of losing that change, and the reconciliation runs again.
func (r *PreviewEnvironmentReconciler) patchFinalizers(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, change func(),
) error {
	before := preview.DeepCopy()
	change()
	if err := r.Patch(ctx, preview, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("update finalizers of %s/%s: %w", preview.Namespace, preview.Name, err)
	}
	return nil
}
