package controller

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// Reasons of the events that do not come from a condition. The other events
// take the reason of the Ready condition they report.
const (
	// EventNamespaceCreated: the operator created the namespace of a preview.
	EventNamespaceCreated = "NamespaceCreated"
	// EventDeleting: the preview is being deleted, and the operator deletes
	// its namespace.
	EventDeleting = "Deleting"
)

// condition is the status, reason and message of a condition.
type condition struct {
	status          metav1.ConditionStatus
	reason, message string
}

// statusUpdate is what a reconciliation writes to the status of a preview.
type statusUpdate struct {
	// namespace is the namespace of the preview, once it exists.
	namespace   string
	url         string
	internalURL string
	expiresAt   time.Time
	phase       vesperv1alpha1.Phase
	ready       condition
	// available is the Available condition; nil leaves it as it is.
	available *condition
}

// availableCondition reads the Available condition of a preview, and its
// phase, from its Deployment. The Deployment reports pods the API server
// rejects (ReplicaFailure) and a rollout past its progress deadline
// (Progressing False) itself.
func availableCondition(d *appsv1.Deployment) (condition, vesperv1alpha1.Phase) {
	available, message := deploymentAvailable(d)
	if available {
		return condition{metav1.ConditionTrue, vesperv1alpha1.ReasonDeploymentAvailable, message}, vesperv1alpha1.PhaseReady
	}
	// Conditions of an older spec say nothing about the current one.
	if d.Status.ObservedGeneration >= d.Generation {
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue {
				return condition{metav1.ConditionFalse, vesperv1alpha1.ReasonPodsRejected, c.Message},
					vesperv1alpha1.PhaseFailed
			}
			if c.Type == appsv1.DeploymentProgressing && c.Status == corev1.ConditionFalse &&
				c.Reason == "ProgressDeadlineExceeded" {
				return condition{metav1.ConditionFalse, vesperv1alpha1.ReasonRolloutStuck, fmt.Sprintf(
						"%s, no progress for %ds: check the pods in namespace %s (image, crashes)",
						message, deref(d.Spec.ProgressDeadlineSeconds, 600), d.Namespace)},
					vesperv1alpha1.PhaseFailed
			}
		}
	}
	return condition{metav1.ConditionFalse, vesperv1alpha1.ReasonProvisioning, message}, vesperv1alpha1.PhasePending
}

func deref(p *int32, def int32) int32 {
	if p == nil {
		return def
	}
	return *p
}

// setStatus writes the URLs, the expiry, the phase and the conditions. It
// does not write when nothing changed, so a status update does not cause
// another one, and it records an event when the Ready condition changes.
func (r *PreviewEnvironmentReconciler) setStatus(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, u statusUpdate,
) error {
	before := preview.DeepCopy()
	preview.Status.Namespace = u.namespace
	preview.Status.URL = u.url
	preview.Status.InternalURL = u.internalURL
	preview.Status.ExpiresAt = &metav1.Time{Time: u.expiresAt}
	preview.Status.Phase = u.phase
	preview.Status.ObservedGeneration = preview.Generation

	expired := condition{metav1.ConditionFalse, vesperv1alpha1.ReasonTTLNotReached,
		"the preview expires at " + u.expiresAt.UTC().Format(time.RFC3339)}
	if u.phase == vesperv1alpha1.PhaseExpired {
		expired = condition{metav1.ConditionTrue, vesperv1alpha1.ReasonTTLPassed,
			"the ttl passed at " + u.expiresAt.UTC().Format(time.RFC3339)}
	}
	conditions := map[string]condition{vesperv1alpha1.ConditionReady: u.ready, vesperv1alpha1.ConditionExpired: expired}
	if u.available != nil {
		conditions[vesperv1alpha1.ConditionAvailable] = *u.available
	}
	for typ, c := range conditions {
		meta.SetStatusCondition(&preview.Status.Conditions, metav1.Condition{
			Type: typ, Status: c.status, Reason: c.reason, Message: c.message,
			ObservedGeneration: preview.Generation,
		})
	}

	if equality.Semantic.DeepEqual(before.Status, preview.Status) {
		return nil
	}
	// A merge patch of the status only: fields the operator does not set are
	// kept. It carries the resourceVersion read: a reconciliation that read
	// the preview from a cache that had not seen the last status yet fails
	// with a conflict, rather than write an older status and repeat events.
	patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
	if err := r.Status().Patch(ctx, preview, patch); err != nil {
		return fmt.Errorf("update status of %s/%s: %w", preview.Namespace, preview.Name, err)
	}

	old := meta.FindStatusCondition(before.Status.Conditions, vesperv1alpha1.ConditionReady)
	if old == nil || old.Status != u.ready.status || old.Reason != u.ready.reason {
		eventType := corev1.EventTypeNormal
		if u.phase == vesperv1alpha1.PhaseFailed {
			eventType = corev1.EventTypeWarning
		}
		r.event(preview, eventType, u.ready.reason, "Reconcile", u.ready.message)
	}
	return nil
}

// reportError shows err in the status of the preview, as the Ready reason
// ReconcileError, and returns it so that the reconciliation is retried.
// Conflicts are not shown: another writer changed an object in the meantime,
// and the retry resolves them.
//
// namespace is the namespace of the preview if the reconciliation got as far
// as creating it; empty keeps the one in the status.
func (r *PreviewEnvironmentReconciler) reportError(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, expiry time.Time, namespace string, err error,
) error {
	if apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) {
		return err
	}
	statusErr := r.setStatus(ctx, preview, statusUpdate{
		namespace:   cmp.Or(namespace, preview.Status.Namespace),
		url:         preview.Status.URL,
		internalURL: preview.Status.InternalURL,
		expiresAt:   expiry,
		phase:       vesperv1alpha1.PhaseFailed,
		ready:       condition{metav1.ConditionFalse, vesperv1alpha1.ReasonReconcileError, err.Error()},
	})
	return errors.Join(err, statusErr)
}

// event records an event on the preview, shown by kubectl describe. Without a
// recorder, as in most unit tests, it does nothing.
func (r *PreviewEnvironmentReconciler) event(
	preview *vesperv1alpha1.PreviewEnvironment, eventType, reason, action, note string,
) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(preview, nil, eventType, reason, action, "%s", note)
}
