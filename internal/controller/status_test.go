package controller

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// withRecorder gives r a fake recorder, and returns it.
func withRecorder(r *PreviewEnvironmentReconciler) *events.FakeRecorder {
	rec := events.NewFakeRecorder(100)
	r.Recorder = rec
	return rec
}

// takeEvents returns the events recorded since the last call, as
// "<type> <reason> <note>".
func takeEvents(rec *events.FakeRecorder) []string {
	var got []string
	for {
		select {
		case e := <-rec.Events:
			got = append(got, e)
		default:
			return got
		}
	}
}

// wantCondition checks one condition of the preview.
func wantCondition(t *testing.T, p *vesperv1alpha1.PreviewEnvironment, typ string, status metav1.ConditionStatus, reason string) {
	t.Helper()
	c := meta.FindStatusCondition(p.Status.Conditions, typ)
	if c == nil || c.Status != status || c.Reason != reason {
		t.Errorf("%s condition = %+v, want %s/%s", typ, c, status, reason)
	}
}

// setDeploymentStatus replaces the status of the Deployment of the preview,
// as the deployment controller of the cluster would.
func setDeploymentStatus(t *testing.T, r *PreviewEnvironmentReconciler, status appsv1.DeploymentStatus) {
	t.Helper()
	d := getDeployment(t, r)
	status.ObservedGeneration = d.Generation
	d.Status = status
	if err := r.Status().Update(context.Background(), d); err != nil {
		t.Fatalf("update deployment status: %v", err)
	}
}

func TestAvailableCondition(t *testing.T) {
	quota := appsv1.DeploymentCondition{
		Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate",
		Message: `pods "app-1" is forbidden: exceeded quota: preview`,
	}
	stuck := appsv1.DeploymentCondition{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
	}
	tests := []struct {
		name       string
		generation int64
		status     appsv1.DeploymentStatus
		want       condition
		wantPhase  vesperv1alpha1.Phase
	}{
		{"available", 1, appsv1.DeploymentStatus{
			ObservedGeneration: 1, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1,
		}, condition{metav1.ConditionTrue, "DeploymentAvailable", "1/1 replicas available"}, "Ready"},
		{"starting", 1, appsv1.DeploymentStatus{ObservedGeneration: 1},
			condition{metav1.ConditionFalse, "Provisioning", "0/1 replicas available"}, "Pending"},
		{"pods rejected", 1, appsv1.DeploymentStatus{
			ObservedGeneration: 1, Conditions: []appsv1.DeploymentCondition{quota},
		}, condition{metav1.ConditionFalse, "PodsRejected", quota.Message}, "Failed"},
		{"rollout stuck", 1, appsv1.DeploymentStatus{
			ObservedGeneration: 1, Conditions: []appsv1.DeploymentCondition{stuck},
		}, condition{metav1.ConditionFalse, "RolloutStuck",
			"0/1 replicas available, no progress for 600s: check the pods in namespace ns (image, crashes)"}, "Failed"},
		{"failure of an older spec", 2, appsv1.DeploymentStatus{
			ObservedGeneration: 1, Conditions: []appsv1.DeploymentCondition{quota},
		}, condition{metav1.ConditionFalse, "Provisioning", "0/1 replicas available"}, "Pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Generation: tt.generation},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr.To[int32](1)},
				Status:     tt.status,
			}
			got, phase := availableCondition(d)
			if got != tt.want || phase != tt.wantPhase {
				t.Errorf("availableCondition = %+v, %s, want %+v, %s", got, phase, tt.want, tt.wantPhase)
			}
		})
	}
}

// TestReconcileReportsTransitions takes a preview through its life: it
// starts, its pods are rejected by the quota, then it runs. Each step sets
// the conditions and the phase, and records one event, not one per
// reconciliation.
func TestReconcileReportsTransitions(t *testing.T) {
	r := newReconciler(t, newPreview())
	rec := withRecorder(r)
	nsName := NamespaceName("team-a", "pr-42")

	reconcileOnce(t, r)
	p := getPreview(t, r)
	wantCondition(t, p, "Ready", metav1.ConditionFalse, "Provisioning")
	wantCondition(t, p, "Available", metav1.ConditionFalse, "Provisioning")
	wantCondition(t, p, "Expired", metav1.ConditionFalse, "TTLNotReached")
	wantEvents(t, rec, "Normal NamespaceCreated created namespace "+nsName,
		"Normal Provisioning 0/1 replicas available")

	rejected := `pods "app-1" is forbidden: exceeded quota: preview`
	setDeploymentStatus(t, r, appsv1.DeploymentStatus{Conditions: []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue, Message: rejected,
	}}})
	reconcileOnce(t, r)
	reconcileOnce(t, r)
	p = getPreview(t, r)
	if p.Status.Phase != vesperv1alpha1.PhaseFailed {
		t.Errorf("phase = %s, want Failed", p.Status.Phase)
	}
	wantCondition(t, p, "Ready", metav1.ConditionFalse, "PodsRejected")
	wantCondition(t, p, "Available", metav1.ConditionFalse, "PodsRejected")
	wantEvents(t, rec, "Warning PodsRejected "+rejected)

	setDeploymentStatus(t, r, appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1})
	reconcileOnce(t, r)
	p = getPreview(t, r)
	wantCondition(t, p, "Ready", metav1.ConditionTrue, "DeploymentAvailable")
	wantCondition(t, p, "Available", metav1.ConditionTrue, "DeploymentAvailable")
	wantEvents(t, rec, "Normal DeploymentAvailable 1/1 replicas available")
}

// wantEvents checks the events recorded since the last check.
func wantEvents(t *testing.T, rec *events.FakeRecorder, want ...string) {
	t.Helper()
	got := takeEvents(rec)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestReconcileReportsError makes the reconciliation fail: a Deployment
// named app exists and is not Vesper's. The status and an event say why, and
// the error is returned so that the reconciliation is retried.
func TestReconcileReportsError(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	foreign := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: DeploymentName}}
	r := newReconciler(t, newPreview(), newPreviewNamespace(nsName), foreign)
	rec := withRecorder(r)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("Reconcile succeeded, want the error")
	}
	p := getPreview(t, r)
	if p.Status.Phase != vesperv1alpha1.PhaseFailed {
		t.Errorf("phase = %s, want Failed", p.Status.Phase)
	}
	wantCondition(t, p, "Ready", metav1.ConditionFalse, "ReconcileError")
	ready := meta.FindStatusCondition(p.Status.Conditions, "Ready")
	if ready == nil || !strings.Contains(ready.Message, "was not created by Vesper") {
		t.Errorf("Ready message = %+v, want the error", ready)
	}
	got := takeEvents(rec)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Warning ReconcileError ") {
		t.Errorf("events = %q, want one Warning ReconcileError", got)
	}
}

// TestReconcileHidesConflicts returns a conflict, which a retry resolves: the
// status does not report it.
func TestReconcileHidesConflicts(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "deployments"}, obj.GetName(),
				errors.New("the object has been modified"))
		},
	})
	// A new image, so that the Deployment is updated.
	if err := r.Patch(context.Background(), getPreview(t, r),
		client.RawPatch(types.MergePatchType, []byte(`{"spec":{"image":"ghcr.io/acme/app:pr-42-b"}}`))); err != nil {
		t.Fatalf("patch preview: %v", err)
	}

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	if _, err := r.Reconcile(context.Background(), req); !apierrors.IsConflict(err) {
		t.Fatalf("Reconcile: %v, want the conflict", err)
	}
	wantCondition(t, getPreview(t, r), "Ready", metav1.ConditionFalse, "Provisioning")
}

// TestReconcileRecordsDeletion deletes a preview: an event says its namespace
// is being deleted.
func TestReconcileRecordsDeletion(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	rec := withRecorder(r)
	deletePreview(t, r)
	reconcileOnce(t, r)

	wantEvents(t, rec, "Normal Deleting deleting namespace "+NamespaceName("team-a", "pr-42"))
}

// TestReconcileReportsExpiry checks the Expired condition before and after
// the ttl, and the event of the expiry.
func TestReconcileReportsExpiry(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	expired := meta.FindStatusCondition(getPreview(t, r).Status.Conditions, "Expired")
	if expired == nil || expired.Message != "the preview expires at 2026-11-07T09:00:00Z" {
		t.Errorf("Expired condition = %+v, want the expiry in the message", expired)
	}

	rec := withRecorder(r)
	setTime(r, testCreation.Add(24*time.Hour))
	reconcileOnce(t, r)
	p := getPreview(t, r)
	wantCondition(t, p, "Expired", metav1.ConditionTrue, "TTLPassed")
	wantCondition(t, p, "Ready", metav1.ConditionFalse, "Expired")
	wantEvents(t, rec,
		"Normal Expired ttl passed at 2026-11-07T09:00:00Z: the preview is being deleted")
}

// TestReconcileRetriesStaleStatus makes the status write conflict, as when the
// preview was read from a cache that had not seen the last status: the
// operator comes back shortly, without an error or an event.
func TestReconcileRetriesStaleStatus(t *testing.T) {
	r := newReconciler(t, newPreview())
	rec := withRecorder(r)
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		SubResourcePatch: func(ctx context.Context, c client.Client, subResource string, obj client.Object,
			patch client.Patch, opts ...client.SubResourcePatchOption) error {
			return apierrors.NewConflict(schema.GroupResource{Resource: "previewenvironments"}, obj.GetName(),
				errors.New("the object has been modified"))
		},
	})

	result := reconcileResult(t, r)
	if result.RequeueAfter != time.Second {
		t.Errorf("RequeueAfter = %s, want 1s", result.RequeueAfter)
	}
	wantEvents(t, rec, "Normal NamespaceCreated created namespace "+NamespaceName("team-a", "pr-42"))
}
