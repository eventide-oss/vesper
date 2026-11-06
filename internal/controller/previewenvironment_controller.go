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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// labelPodSecurity makes the namespace enforce the restricted Pod Security
// Standard: no privileged, root or extra-capability pods in it.
const labelPodSecurity = "pod-security.kubernetes.io/enforce"

// PreviewEnvironmentReconciler runs each PreviewEnvironment in its own
// namespace: a Deployment of its image, behind a Service.
type PreviewEnvironmentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// Clock tells the time to compare with the expiry of a preview. Nil means
	// the real clock; tests set a fake one.
	Clock clock.PassiveClock
	// PreviewDomain is the base domain of the public addresses of previews,
	// for example preview.example.com. Empty means no public address: no
	// Ingress is created.
	PreviewDomain string
	// IngressClassName selects the ingress controller that serves the
	// Ingresses. Empty means the default class of the cluster.
	IngressClassName string
	// Resources bounds the resources of each preview. Nil means
	// DefaultResources.
	Resources *Resources
}

// +kubebuilder:rbac:groups=vesper.eventide.dev,resources=previewenvironments,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=vesper.eventide.dev,resources=previewenvironments/status,verbs=get;patch
// +kubebuilder:rbac:groups=vesper.eventide.dev,resources=previewenvironments/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=limitranges;resourcequotas,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update

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
		return ctrl.Result{}, r.cleanup(ctx, &preview)
	}
	if err := r.ensureFinalizer(ctx, &preview); err != nil {
		return ctrl.Result{}, err
	}

	// Checked before anything is created: an expired preview is only deleted.
	expiry := expiresAt(&preview)
	now := r.clock().Now()
	if !now.Before(expiry) {
		return ctrl.Result{}, r.expire(ctx, &preview, expiry)
	}
	// Come back when the ttl passes, rather than polling (ADR-0004). A change
	// of spec.ttl reconciles the preview again and computes a new delay.
	result := ctrl.Result{RequeueAfter: expiry.Sub(now)}

	name := NamespaceName(preview.Namespace, preview.Name)
	owned, err := r.ensureNamespace(ctx, &preview, name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !owned {
		log.Info("namespace exists and was not created by Vesper for this preview: leaving it untouched",
			"previewNamespace", name)
		return result, r.setStatus(ctx, &preview, statusUpdate{
			expiresAt: expiry,
			phase:     vesperv1alpha1.PhaseFailed,
			ready:     metav1.ConditionFalse,
			reason:    vesperv1alpha1.ReasonNamespaceConflict,
			message:   fmt.Sprintf("namespace %s already exists and was not created by Vesper for this preview", name),
		})
	}

	if err := r.ensureLimits(ctx, &preview, name); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureNetworkPolicy(ctx, &preview, name); err != nil {
		return ctrl.Result{}, err
	}
	deployment, err := r.ensureDeployment(ctx, &preview, name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.ensureService(ctx, &preview, name); err != nil {
		return ctrl.Result{}, err
	}
	url, err := r.ensureIngress(ctx, &preview, name)
	if err != nil {
		return ctrl.Result{}, err
	}
	address := internalURL(name)
	// The Deployment's status changes as its pods start: its watch brings the
	// preview back here, and the status follows.
	update := statusUpdate{
		url:         url,
		internalURL: address,
		expiresAt:   expiry,
		phase:       vesperv1alpha1.PhasePending,
		ready:       metav1.ConditionFalse,
		reason:      vesperv1alpha1.ReasonProvisioning,
	}
	available, message := deploymentAvailable(deployment)
	update.message = message
	if available {
		update.phase, update.ready, update.reason =
			vesperv1alpha1.PhaseReady, metav1.ConditionTrue, vesperv1alpha1.ReasonDeploymentAvailable
	}
	return result, r.setStatus(ctx, &preview, update)
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
		Name:   name,
		Labels: map[string]string{labelPodSecurity: "restricted"},
	}}
	setPreviewLabels(&ns, preview)
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

// statusUpdate is what a reconciliation writes to the status of a preview.
type statusUpdate struct {
	url         string
	internalURL string
	expiresAt   time.Time
	phase       vesperv1alpha1.Phase
	// ready, reason and message make the Ready condition.
	ready           metav1.ConditionStatus
	reason, message string
}

// setStatus writes the URLs, the expiry, the phase and the Ready condition. It does not write when nothing changed, so a status update does
// not cause another one.
func (r *PreviewEnvironmentReconciler) setStatus(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, u statusUpdate,
) error {
	before := preview.DeepCopy()
	preview.Status.URL = u.url
	preview.Status.InternalURL = u.internalURL
	preview.Status.ExpiresAt = &metav1.Time{Time: u.expiresAt}
	preview.Status.Phase = u.phase
	preview.Status.ObservedGeneration = preview.Generation
	meta.SetStatusCondition(&preview.Status.Conditions, metav1.Condition{
		Type:               vesperv1alpha1.ConditionReady,
		Status:             u.ready,
		Reason:             u.reason,
		Message:            u.message,
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
// spec changes, and when an object Vesper created for it changes or is
// deleted, so that what was changed or deleted by hand is restored and the
// status follows the Deployment.
func (r *PreviewEnvironmentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Status updates do not change the generation, so the operator does
		// not wake itself up when it writes the status. Deleting a preview
		// that has a finalizer increases it, so the cleanup runs.
		For(&vesperv1alpha1.PreviewEnvironment{},
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&corev1.Namespace{},
			handler.EnqueueRequestsFromMapFunc(previewOfObject),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		Watches(&appsv1.Deployment{},
			handler.EnqueueRequestsFromMapFunc(previewOfObject),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		Watches(&corev1.Service{},
			handler.EnqueueRequestsFromMapFunc(previewOfObject),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		Watches(&networkingv1.Ingress{},
			handler.EnqueueRequestsFromMapFunc(previewOfObject),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		Watches(&corev1.LimitRange{},
			handler.EnqueueRequestsFromMapFunc(previewOfObject),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		Watches(&corev1.ResourceQuota{},
			handler.EnqueueRequestsFromMapFunc(previewOfObject),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		Watches(&networkingv1.NetworkPolicy{},
			handler.EnqueueRequestsFromMapFunc(previewOfObject),
			builder.WithPredicates(predicate.NewPredicateFuncs(managedByVesper))).
		// The objects are tied to their preview by labels, not owner
		// references: an owner reference cannot point to another namespace.
		Named("previewenvironment").
		Complete(r)
}
