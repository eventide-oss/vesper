package controller

import (
	"context"
	"maps"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clocktesting "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// newReconciler returns a reconciler backed by the fake client of
// controller-runtime, an in-memory API server, holding objs.
func newReconciler(t *testing.T, objs ...client.Object) *PreviewEnvironmentReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, vesperv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&vesperv1alpha1.PreviewEnvironment{}).
		Build()
	return &PreviewEnvironmentReconciler{Client: c, Scheme: scheme, Clock: clocktesting.NewFakePassiveClock(testNow)}
}

// testNow is the time of the fake clock of newReconciler: one hour after the
// creation of newPreview, well before it expires.
var (
	testCreation = time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)
	testNow      = testCreation.Add(time.Hour)
)

func newPreview() *vesperv1alpha1.PreviewEnvironment {
	return &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "team-a", Name: "pr-42", Generation: 1,
			CreationTimestamp: metav1.NewTime(testCreation),
		},
		Spec: vesperv1alpha1.PreviewEnvironmentSpec{Image: "ghcr.io/acme/app:pr-42"},
	}
}

func reconcileOnce(t *testing.T, r *PreviewEnvironmentReconciler) {
	t.Helper()
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func getPreview(t *testing.T, r *PreviewEnvironmentReconciler) *vesperv1alpha1.PreviewEnvironment {
	t.Helper()
	var p vesperv1alpha1.PreviewEnvironment
	if err := r.Get(context.Background(), types.NamespacedName{Namespace: "team-a", Name: "pr-42"}, &p); err != nil {
		t.Fatalf("get preview: %v", err)
	}
	return &p
}

func getNamespace(t *testing.T, r *PreviewEnvironmentReconciler, name string) *corev1.Namespace {
	t.Helper()
	var ns corev1.Namespace
	if err := r.Get(context.Background(), types.NamespacedName{Name: name}, &ns); err != nil {
		t.Fatalf("get namespace %s: %v", name, err)
	}
	return &ns
}

// checkStatus checks the phase, the observed generation and the Ready
// condition of the preview.
func checkStatus(t *testing.T, p *vesperv1alpha1.PreviewEnvironment, phase vesperv1alpha1.Phase, reason string) {
	t.Helper()
	if p.Status.Phase != phase {
		t.Errorf("phase = %q, want %q", p.Status.Phase, phase)
	}
	if p.Status.ObservedGeneration != p.Generation {
		t.Errorf("observedGeneration = %d, want %d", p.Status.ObservedGeneration, p.Generation)
	}
	ready := meta.FindStatusCondition(p.Status.Conditions, vesperv1alpha1.ConditionReady)
	if ready == nil {
		t.Fatalf("no Ready condition in %+v", p.Status.Conditions)
	}
	if ready.Status != metav1.ConditionFalse || ready.Reason != reason || ready.Message == "" {
		t.Errorf("Ready condition = %s/%s %q, want False/%s with a message",
			ready.Status, ready.Reason, ready.Message, reason)
	}
}

func TestReconcileCreatesNamespace(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	ns := getNamespace(t, r, NamespaceName("team-a", "pr-42"))
	want := map[string]string{
		LabelManagedBy:                       ManagedByVesper,
		LabelPreviewNamespace:                "team-a",
		LabelPreviewName:                     "pr-42",
		"pod-security.kubernetes.io/enforce": "restricted",
	}
	if !maps.Equal(ns.Labels, want) {
		t.Errorf("namespace labels = %v, want %v", ns.Labels, want)
	}
	checkStatus(t, getPreview(t, r), vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
}

func TestReconcileTwiceChangesNothing(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	nsName := NamespaceName("team-a", "pr-42")
	nsBefore, previewBefore := getNamespace(t, r, nsName), getPreview(t, r)

	reconcileOnce(t, r)

	// No write at all: the resource versions did not move.
	if got := getNamespace(t, r, nsName).ResourceVersion; got != nsBefore.ResourceVersion {
		t.Errorf("namespace was written again: resourceVersion %s, then %s", nsBefore.ResourceVersion, got)
	}
	previewAfter := getPreview(t, r)
	if previewAfter.ResourceVersion != previewBefore.ResourceVersion {
		t.Errorf("preview was written again: resourceVersion %s, then %s",
			previewBefore.ResourceVersion, previewAfter.ResourceVersion)
	}
	if !reflect.DeepEqual(previewAfter.Status, previewBefore.Status) {
		t.Errorf("status changed:\n%+v\nthen\n%+v", previewBefore.Status, previewAfter.Status)
	}
}

func TestReconcileDeletedPreview(t *testing.T) {
	r := newReconciler(t) // the preview does not exist
	reconcileOnce(t, r)

	var ns corev1.Namespace
	err := r.Get(context.Background(), types.NamespacedName{Name: NamespaceName("team-a", "pr-42")}, &ns)
	if !apierrors.IsNotFound(err) {
		t.Errorf("get namespace: got %v, want not found", err)
	}
}

func TestReconcileLeavesForeignNamespaceUntouched(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	tests := map[string]map[string]string{
		"no labels": {"owner": "someone-else"},
		"another preview's labels": {
			LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-b", LabelPreviewName: "pr-42",
		},
	}
	for name, labels := range tests {
		t.Run(name, func(t *testing.T) {
			foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: maps.Clone(labels)}}
			r := newReconciler(t, newPreview(), foreign)
			reconcileOnce(t, r)

			ns := getNamespace(t, r, nsName)
			if !maps.Equal(ns.Labels, labels) {
				t.Errorf("namespace labels = %v, want them unchanged: %v", ns.Labels, labels)
			}
			checkStatus(t, getPreview(t, r), vesperv1alpha1.PhaseFailed, vesperv1alpha1.ReasonNamespaceConflict)
		})
	}
}

func TestPreviewOfNamespace(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-42",
	}}}
	got := previewOfObject(context.Background(), ns)
	want := []ctrl.Request{{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("previewOfObject = %v, want %v", got, want)
	}

	ns.Labels = map[string]string{LabelManagedBy: ManagedByVesper}
	if got := previewOfObject(context.Background(), ns); got != nil {
		t.Errorf("previewOfObject without preview labels = %v, want nil", got)
	}
}
