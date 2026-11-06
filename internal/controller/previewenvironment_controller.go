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
	"maps"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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
	// Recorder records the events of previews. Nil records none.
	Recorder events.EventRecorder
	// Resources bounds the resources of each preview. Nil means
	// DefaultResources.
	Resources *Resources
}

// +kubebuilder:rbac:groups=vesper.eventide.dev,resources=previewenvironments,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=vesper.eventide.dev,resources=previewenvironments/status,verbs=patch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups="",resources=limitranges;resourcequotas,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile brings the cluster to the state a PreviewEnvironment asks for. It
// only knows the preview's namespace and name, not what changed, so it reads
// everything again and is safe to call any number of times.
func (r *PreviewEnvironmentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
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

	update, err := r.ensureObjects(ctx, &preview, expiry)
	if err != nil {
		return ctrl.Result{}, r.reportError(ctx, &preview, expiry, err)
	}
	if err := r.setStatus(ctx, &preview, update); apierrors.IsConflict(err) {
		// The cache had not seen the last status yet: come back once it has.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	} else if err != nil {
		return ctrl.Result{}, err
	}
	return result, nil
}

// ensureObjects creates or updates every object of the preview, in its
// namespace, and returns the status that follows.
func (r *PreviewEnvironmentReconciler) ensureObjects(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, expiry time.Time,
) (statusUpdate, error) {
	name := NamespaceName(preview.Namespace, preview.Name)
	owned, err := r.ensureNamespace(ctx, preview, name)
	if err != nil {
		return statusUpdate{}, err
	}
	if !owned {
		logf.FromContext(ctx).Info(
			"namespace exists and was not created by Vesper for this preview: leaving it untouched",
			"previewNamespace", name)
		conflict := condition{metav1.ConditionFalse, vesperv1alpha1.ReasonNamespaceConflict,
			fmt.Sprintf("namespace %s already exists and was not created by Vesper for this preview", name)}
		return statusUpdate{
			expiresAt: expiry, phase: vesperv1alpha1.PhaseFailed, ready: conflict, available: &conflict,
		}, nil
	}

	if err := r.ensureLimits(ctx, preview, name); err != nil {
		return statusUpdate{}, err
	}
	if err := r.ensureNetworkPolicy(ctx, preview, name); err != nil {
		return statusUpdate{}, err
	}
	deployment, err := r.ensureDeployment(ctx, preview, name)
	if err != nil {
		return statusUpdate{}, err
	}
	if err := r.ensureService(ctx, preview, name); err != nil {
		return statusUpdate{}, err
	}
	url, err := r.ensureIngress(ctx, preview, name)
	if err != nil {
		return statusUpdate{}, err
	}
	// The Deployment's status changes as its pods start: its watch brings the
	// preview back here, and the status follows.
	available, phase := availableCondition(deployment)
	return statusUpdate{
		url:         url,
		internalURL: internalURL(name),
		expiresAt:   expiry,
		phase:       phase,
		ready:       available,
		available:   &available,
	}, nil
}

// ensureNamespace creates the namespace of the preview if it does not exist,
// and restores its labels if they were changed. It reports false when a
// namespace of that name exists but does not belong to the preview; it never
// modifies such a namespace.
func (r *PreviewEnvironmentReconciler) ensureNamespace(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, name string,
) (bool, error) {
	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: name}, &ns)
	if err == nil {
		if !ownedBy(&ns, preview) {
			return false, nil
		}
		return true, r.restoreNamespaceLabels(ctx, &ns, preview)
	}
	if !apierrors.IsNotFound(err) {
		return false, fmt.Errorf("get namespace %s: %w", name, err)
	}

	ns = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	setNamespaceLabels(&ns, preview)
	if err := r.Create(ctx, &ns); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// The cache had not seen it yet. Reconcile again to read it and
			// check who owns it.
			return false, fmt.Errorf("namespace %s was created concurrently: %w", name, err)
		}
		return false, fmt.Errorf("create namespace %s: %w", name, err)
	}
	logf.FromContext(ctx).Info("created namespace", "previewNamespace", name)
	r.event(preview, corev1.EventTypeNormal, EventNamespaceCreated, "CreateNamespace", "created namespace "+name)
	return true, nil
}

// setNamespaceLabels sets the labels Vesper owns on the namespace of the
// preview: the preview labels, and the restricted Pod Security Standard, which
// makes the API server reject privileged, root or extra-capability pods.
func setNamespaceLabels(ns *corev1.Namespace, preview *vesperv1alpha1.PreviewEnvironment) {
	setPreviewLabels(ns, preview)
	ns.Labels[labelPodSecurity] = "restricted"
}

// restoreNamespaceLabels puts back the labels of setNamespaceLabels, for
// example after someone lowered the Pod Security level by hand, and keeps the
// other labels.
func (r *PreviewEnvironmentReconciler) restoreNamespaceLabels(
	ctx context.Context, ns *corev1.Namespace, preview *vesperv1alpha1.PreviewEnvironment,
) error {
	before := ns.DeepCopy()
	setNamespaceLabels(ns, preview)
	if maps.Equal(before.Labels, ns.Labels) {
		return nil
	}
	if err := r.Patch(ctx, ns, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("restore labels of namespace %s: %w", ns.Name, err)
	}
	logf.FromContext(ctx).Info("restored namespace labels", "previewNamespace", ns.Name)
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
