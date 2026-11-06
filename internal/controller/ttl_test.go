package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// reconcileResult reconciles the preview team-a/pr-42 and returns the result.
func reconcileResult(t *testing.T, r *PreviewEnvironmentReconciler) ctrl.Result {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return result
}

// setTime moves the clock of the reconciler to t.
func setTime(r *PreviewEnvironmentReconciler, t time.Time) {
	r.Clock = clocktesting.NewFakePassiveClock(t)
}

func TestExpiresAt(t *testing.T) {
	tests := []struct {
		name string
		ttl  *metav1.Duration
		want time.Time
	}{
		{"default", nil, testCreation.Add(24 * time.Hour)},
		{"90 minutes", &metav1.Duration{Duration: 90 * time.Minute}, testCreation.Add(90 * time.Minute)},
		{"fraction of a second, rounded up", &metav1.Duration{Duration: time.Minute + 500*time.Millisecond},
			testCreation.Add(time.Minute + time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPreview()
			p.Spec.TTL = tt.ttl
			if got := expiresAt(p); !got.Equal(tt.want) {
				t.Errorf("expiresAt = %s, want %s", got, tt.want)
			}
		})
	}
}

// TestReconcileRequeuesAtExpiry checks the expiry in the status and the
// delay before the next reconciliation: the time left, not a polling period.
func TestReconcileRequeuesAtExpiry(t *testing.T) {
	r := newReconciler(t, newPreview()) // created at testCreation, now is one hour later
	result := reconcileResult(t, r)

	want := testCreation.Add(24 * time.Hour)
	if got := getPreview(t, r).Status.ExpiresAt; got == nil || !got.Time.Equal(want) {
		t.Errorf("expiresAt = %v, want %s", got, want)
	}
	if result.RequeueAfter != 23*time.Hour {
		t.Errorf("RequeueAfter = %s, want 23h", result.RequeueAfter)
	}
}

// TestReconcileFollowsTTLChange extends then shortens the ttl: the expiry and
// the delay follow.
func TestReconcileFollowsTTLChange(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	for _, tc := range []struct {
		ttl, wantRequeue time.Duration
	}{
		{48 * time.Hour, 47 * time.Hour},
		{2 * time.Hour, time.Hour},
	} {
		preview := getPreview(t, r)
		preview.Spec.TTL = &metav1.Duration{Duration: tc.ttl}
		if err := r.Update(context.Background(), preview); err != nil {
			t.Fatalf("update preview: %v", err)
		}
		result := reconcileResult(t, r)

		want := testCreation.Add(tc.ttl)
		if got := getPreview(t, r).Status.ExpiresAt; got == nil || !got.Time.Equal(want) {
			t.Errorf("ttl %s: expiresAt = %v, want %s", tc.ttl, got, want)
		}
		if result.RequeueAfter != tc.wantRequeue {
			t.Errorf("ttl %s: RequeueAfter = %s, want %s", tc.ttl, result.RequeueAfter, tc.wantRequeue)
		}
	}
}

// TestReconcileExpiresPreview runs a preview until its ttl passes: it is
// marked Expired and deleted, then the finalizer removes its namespace and the
// preview goes.
func TestReconcileExpiresPreview(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	nsName := NamespaceName("team-a", "pr-42")

	setTime(r, testCreation.Add(24*time.Hour)) // exactly the expiry
	if result := reconcileResult(t, r); result.RequeueAfter != 0 {
		t.Errorf("RequeueAfter = %s after expiry, want none", result.RequeueAfter)
	}
	p := getPreview(t, r)
	if p.DeletionTimestamp.IsZero() {
		t.Fatal("expired preview not deleted")
	}
	ready := meta.FindStatusCondition(p.Status.Conditions, vesperv1alpha1.ConditionReady)
	if p.Status.Phase != vesperv1alpha1.PhaseExpired || ready == nil ||
		ready.Status != metav1.ConditionFalse || ready.Reason != vesperv1alpha1.ReasonExpired {
		t.Errorf("status = %+v, want phase Expired with the Ready condition False/Expired", p.Status)
	}

	reconcileOnce(t, r) // the deletion: the namespace goes
	reconcileOnce(t, r) // the namespace is gone: the finalizer goes
	if namespaceExists(t, r, nsName) || previewExists(t, r) {
		t.Error("preview or its namespace still exists after expiry")
	}
}

// TestReconcileExpiredPreviewCreatesNothing reconciles a preview whose ttl
// passed before the operator saw it, for example while it was stopped: it is
// deleted without creating anything.
func TestReconcileExpiredPreviewCreatesNothing(t *testing.T) {
	r := newReconciler(t, newPreview())
	setTime(r, testCreation.Add(48*time.Hour))
	reconcileOnce(t, r)

	if namespaceExists(t, r, NamespaceName("team-a", "pr-42")) {
		t.Error("namespace created for an expired preview")
	}
	if getPreview(t, r).DeletionTimestamp.IsZero() {
		t.Error("expired preview not deleted")
	}
	reconcileOnce(t, r)
	if previewExists(t, r) {
		t.Error("expired preview still exists")
	}
}

// TestReconcileConflictedPreviewExpires checks that a preview in
// NamespaceConflict also comes back at its expiry.
func TestReconcileConflictedPreviewExpires(t *testing.T) {
	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: NamespaceName("team-a", "pr-42"), Labels: map[string]string{"owner": "someone"},
	}}
	r := newReconciler(t, newPreview(), foreign)
	if result := reconcileResult(t, r); result.RequeueAfter != 23*time.Hour {
		t.Errorf("RequeueAfter = %s, want 23h", result.RequeueAfter)
	}
	if got := getPreview(t, r).Status.ExpiresAt; got == nil {
		t.Error("no expiresAt in the status of a conflicted preview")
	}

	setTime(r, testCreation.Add(24*time.Hour))
	reconcileOnce(t, r)
	reconcileOnce(t, r)
	if previewExists(t, r) {
		t.Error("conflicted preview still exists after expiry")
	}
	if !namespaceExists(t, r, foreign.Name) {
		t.Error("foreign namespace deleted")
	}
}
