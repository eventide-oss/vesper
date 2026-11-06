package controller

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// defaultTTL is the ttl of a preview whose spec has none. The API server
// fills in the same default, so it only matters for objects it did not see.
const defaultTTL = 24 * time.Hour

// clock returns the clock of the reconciler: the real one unless a test set
// another.
func (r *PreviewEnvironmentReconciler) clock() clock.PassiveClock {
	if r.Clock == nil {
		return clock.RealClock{}
	}
	return r.Clock
}

// expiresAt returns when the preview expires: its creation time, set by the
// API server, plus its ttl (ADR-0004). It is rounded up to the second, the
// precision of a timestamp in the API, so that the status written is the
// value computed and a preview never expires early.
func expiresAt(preview *vesperv1alpha1.PreviewEnvironment) time.Time {
	ttl := defaultTTL
	if preview.Spec.TTL != nil {
		ttl = preview.Spec.TTL.Duration
	}
	t := preview.CreationTimestamp.Add(ttl)
	if rounded := t.Truncate(time.Second); !rounded.Equal(t) {
		return rounded.Add(time.Second)
	}
	return t
}

// expire marks the preview Expired and deletes it. The finalizer then deletes
// its namespace, and the preview disappears (#39).
func (r *PreviewEnvironmentReconciler) expire(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, expiry time.Time,
) error {
	logf.FromContext(ctx).Info("ttl passed: deleting the preview", "expiresAt", expiry.UTC().Format(time.RFC3339))
	err := r.setStatus(ctx, preview, statusUpdate{
		url:         preview.Status.URL,
		internalURL: preview.Status.InternalURL,
		expiresAt:   expiry,
		phase:       vesperv1alpha1.PhaseExpired,
		ready: condition{metav1.ConditionFalse, vesperv1alpha1.ReasonExpired,
			"ttl passed at " + expiry.UTC().Format(time.RFC3339) + ": the preview is being deleted"},
	})
	if err != nil {
		return err
	}
	// The UID precondition deletes this preview, not a new one of the same name.
	if err := r.Delete(ctx, preview, client.Preconditions{UID: &preview.UID}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete expired preview %s/%s: %w", preview.Namespace, preview.Name, err)
	}
	return nil
}
