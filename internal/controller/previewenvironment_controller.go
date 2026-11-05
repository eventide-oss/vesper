/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// Labels on the namespaces Vesper creates. A namespace is cluster-scoped and
// cannot have a namespaced owner, so these labels record which preview it
// belongs to.
const (
	LabelManagedBy        = "app.kubernetes.io/managed-by"
	ManagedByVesper       = "vesper"
	LabelPreviewNamespace = "vesper.eventide.dev/preview-namespace"
	LabelPreviewName      = "vesper.eventide.dev/preview-name"

	// labelPodSecurity makes the namespace enforce the restricted Pod Security
	// Standard: no privileged, root or extra-capability pods in it.
	labelPodSecurity = "pod-security.kubernetes.io/enforce"
)

// PreviewEnvironmentReconciler gives each PreviewEnvironment its own namespace.
type PreviewEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=vesper.eventide.dev,resources=previewenvironments,verbs=get;list;watch
// +kubebuilder:rbac:groups=vesper.eventide.dev,resources=previewenvironments/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create

// Reconcile brings the cluster to the state a PreviewEnvironment asks for. It
// only knows the preview's namespace and name, not what changed, so it reads
// everything again and is safe to call any number of times.
func (r *PreviewEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var preview vesperv1alpha1.PreviewEnvironment
	if err := r.Get(ctx, req.NamespacedName, &preview); err != nil {
		// Not found: the preview was deleted, there is nothing to do.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !preview.DeletionTimestamp.IsZero() {
		// Cleanup on deletion comes with a finalizer (P3).
		return ctrl.Result{}, nil
	}

	name := NamespaceName(preview.Namespace, preview.Name)
	owned, err := r.ensureNamespace(ctx, &preview, name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !owned {
		log.Info("namespace exists and was not created by Vesper for this preview: leaving it untouched",
			"previewNamespace", name)
		return ctrl.Result{}, r.setStatus(ctx, &preview, vesperv1alpha1.PhaseFailed,
			vesperv1alpha1.ReasonNamespaceConflict,
			fmt.Sprintf("namespace %s already exists and was not created by Vesper for this preview", name))
	}
	return ctrl.Result{}, r.setStatus(ctx, &preview, vesperv1alpha1.PhasePending,
		vesperv1alpha1.ReasonProvisioning,
		fmt.Sprintf("namespace %s is ready; the application is not deployed yet", name))
}

// ensureNamespace creates the namespace of the preview if it does not exist.
// It reports false when a namespace of that name exists but does not belong
// to the preview; it never modifies such a namespace.
func (r *PreviewEnvironmentReconciler) ensureNamespace(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, name string,
) (bool, error) {
	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: name}, &ns)
	if err == nil {
		return ownedBy(&ns, preview), nil
	}
	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get namespace %s: %w", name, err)
	}

	ns = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			LabelManagedBy:        ManagedByVesper,
			LabelPreviewNamespace: preview.Namespace,
			LabelPreviewName:      preview.Name,
			labelPodSecurity:      "restricted",
		},
	}}
	if err := r.Create(ctx, &ns); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The cache had not seen it yet. Reconcile again to read it and
			// check who owns it.
			return false, fmt.Errorf("namespace %s was created concurrently: %w", name, err)
		}
		return false, fmt.Errorf("create namespace %s: %w", name, err)
	}
	logf.FromContext(ctx).Info("created namespace", "previewNamespace", name)
	return true, nil
}

// ownedBy reports whether Vesper created ns for preview.
func ownedBy(ns *corev1.Namespace, preview *vesperv1alpha1.PreviewEnvironment) bool {
	labels := ns.GetLabels()
	return labels[LabelManagedBy] == ManagedByVesper &&
		labels[LabelPreviewNamespace] == preview.Namespace &&
		labels[LabelPreviewName] == preview.Name
}

// setStatus writes the phase and the Ready condition, which stays False until
// the application runs. It does not write when nothing changed, so a status
// update does not cause another one.
func (r *PreviewEnvironmentReconciler) setStatus(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment,
	phase vesperv1alpha1.Phase, reason, message string,
) error {
	before := preview.DeepCopy()
	preview.Status.Phase = phase
	preview.Status.ObservedGeneration = preview.Generation
	meta.SetStatusCondition(&preview.Status.Conditions, metav1.Condition{
		Type:               vesperv1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: preview.Generation,
	})
	if equality.Semantic.DeepEqual(before.Status, preview.Status) {
		return nil
	}
	// A merge patch of the status only: no conflict with spec changes, and
	// fields the operator does not set are kept.
	if err := r.Status().Patch(ctx, preview, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("update status of %s/%s: %w", preview.Namespace, preview.Name, err)
	}
	return nil
}

// SetupWithManager registers the controller: it reconciles a preview when its
// spec changes, and when the namespace Vesper created for it changes or is
// deleted, so that a namespace deleted by hand is created again.
func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Status updates do not change the generation, so the operator does
		// not wake itself up when it writes the status.
		For(&vesperv1alpha1.PreviewEnvironment{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(previewOfNamespace),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		Named("previewenvironment").
		Complete(r)
}

// managedByVesper filters the namespace events to those Vesper created.
func managedByVesper(obj client.Object) bool {
	return obj.GetLabels()[LabelManagedBy] == ManagedByVesper
}

// previewOfNamespace maps a namespace Vesper created to its preview.
func previewOfNamespace(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	namespace, name := labels[LabelPreviewNamespace], labels[LabelPreviewName]
	if namespace == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}}
}
