// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/cache"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

const (
	currentImage = "ghcr.io/acme/app:pr-42"
	olderImage   = "ghcr.io/acme/app:pr-41"
	pullMessage  = `failed to pull and unpack image "ghcr.io/acme/app:pr-42": not found`
)

// testPod returns a pod of the application with image, waiting for reason.
func testPod(name, image, reason, message string) corev1.Pod {
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns", Labels: map[string]string{appLabel: appLabelValue}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: DeploymentName, Image: image}}},
	}
	status := corev1.ContainerStatus{Name: DeploymentName}
	if reason != "" {
		status.State.Waiting = &corev1.ContainerStateWaiting{Reason: reason, Message: message}
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{status}
	return pod
}

func testDeployment(image string) *appsv1.Deployment {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: DeploymentName, Generation: 1}}
	d.Spec.Replicas = ptr.To[int32](1)
	d.Spec.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{appLabel: appLabelValue}}
	d.Spec.Template.Spec.Containers = []corev1.Container{{Name: DeploymentName, Image: image}}
	d.Status.ObservedGeneration = 1
	return d
}

func TestPodFailure(t *testing.T) {
	crashing := testPod("app-b", currentImage, "CrashLoopBackOff", "back-off 40s restarting failed container")
	crashing.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{
		ExitCode: 137, Reason: "OOMKilled",
	}
	deleting := testPod("app-c", currentImage, "ErrImagePull", pullMessage)
	deleting.DeletionTimestamp = ptr.To(metav1.Now())
	previousPull := &metav1.Condition{Reason: vesperv1alpha1.ReasonImagePullFailed, Message: "pod app-a cannot pull: not found"}

	tests := []struct {
		name       string
		pods       []corev1.Pod
		previous   *metav1.Condition
		wantOK     bool
		wantReason string
		wantMsg    string // part of the message
	}{
		{"no pod yet", nil, nil, false, "", ""},
		{"starting", []corev1.Pod{testPod("app-a", currentImage, "ContainerCreating", "")}, nil, false, "", ""},
		{"running", []corev1.Pod{testPod("app-a", currentImage, "", "")}, nil, false, "", ""},
		{"image not found", []corev1.Pod{testPod("app-a", currentImage, "ErrImagePull", pullMessage)}, nil,
			true, "ImagePullFailed", "pod app-a cannot pull image " + currentImage + ": " + pullMessage},
		{"back-off keeps the cause of the last pull",
			[]corev1.Pod{testPod("app-a", currentImage, "ImagePullBackOff", `Back-off pulling image "x"`)}, previousPull,
			true, "ImagePullFailed", "pod app-a cannot pull: not found"},
		{"back-off without a cause shown yet",
			[]corev1.Pod{testPod("app-a", currentImage, "ImagePullBackOff", `Back-off pulling image "x"`)}, nil,
			true, "ImagePullFailed", `Back-off pulling image "x"`},
		{"invalid image name", []corev1.Pod{testPod("app-a", currentImage, "InvalidImageName", "bad")}, nil,
			true, "ImagePullFailed", "bad"},
		{"crash loop", []corev1.Pod{crashing}, nil, true, "CrashLooping",
			"the application of pod app-b keeps exiting, last with exit code 137 (OOMKilled): see kubectl logs -n ns app-b --previous"},
		{"an older image during a rollout does not count",
			[]corev1.Pod{testPod("app-a", olderImage, "CrashLoopBackOff", "")}, nil, false, "", ""},
		{"a pod being deleted does not count", []corev1.Pod{deleting}, nil, false, "", ""},
		{"the first failing pod by name", []corev1.Pod{crashing, testPod("app-a", currentImage, "ErrImagePull", pullMessage)},
			nil, true, "ImagePullFailed", "pod app-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := podFailure(testDeployment(currentImage), tt.pods, tt.previous)
			if ok != tt.wantOK {
				t.Fatalf("podFailure = %+v, %t, want %t", got, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got.status != metav1.ConditionFalse || got.reason != tt.wantReason || !strings.Contains(got.message, tt.wantMsg) {
				t.Errorf("podFailure = %+v, want False/%s with %q", got, tt.wantReason, tt.wantMsg)
			}
		})
	}
}

func TestPodFailureMessageBounded(t *testing.T) {
	pods := []corev1.Pod{testPod("app-a", currentImage, "ErrImagePull", strings.Repeat("é", 1000))}
	got, _ := podFailure(testDeployment(currentImage), pods, nil)
	if len(got.message) > maxPodMessage || !strings.HasSuffix(got.message, "…") {
		t.Errorf("message of %d bytes, want at most %d, ending with an ellipsis", len(got.message), maxPodMessage)
	}
}

// TestAvailableConditionPrefersPods: the pods tell the cause before the
// Deployment gives up, and a running preview ignores them.
func TestAvailableConditionPrefersPods(t *testing.T) {
	pods := []corev1.Pod{testPod("app-a", currentImage, "ErrImagePull", pullMessage)}
	d := testDeployment(currentImage)
	d.Status.Conditions = []appsv1.DeploymentCondition{{
		Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded",
	}}
	if got, phase := availableCondition(d, pods, nil); got.reason != "ImagePullFailed" || phase != vesperv1alpha1.PhaseFailed {
		t.Errorf("availableCondition = %+v, %s, want ImagePullFailed, Failed", got, phase)
	}
	d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas = 1, 1, 1
	if got, _ := availableCondition(d, pods, nil); got.reason != "DeploymentAvailable" {
		t.Errorf("availableCondition = %+v, want DeploymentAvailable", got)
	}
}

// TestReconcileReportsImagePullFailed: a pod of the preview that cannot pull
// its image fails the preview at once, with an event, and the preview comes
// back once the pod runs.
func TestReconcileReportsImagePullFailed(t *testing.T) {
	r := newReconciler(t, newPreview())
	rec := withRecorder(r)
	reconcileOnce(t, r)
	takeEvents(rec)

	nsName := NamespaceName("team-a", "pr-42")
	pod := testPod("app-1", currentImage, "ErrImagePull", pullMessage)
	pod.Namespace, pod.Labels = nsName, previewLabels(newPreview())
	pod.Labels[appLabel] = appLabelValue // the pod template labels
	if err := r.Create(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	// A pod of another preview in the same namespace is not read.
	other := testPod("other", currentImage, "CrashLoopBackOff", "")
	other.Namespace = nsName
	other.Labels = map[string]string{LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-7"}
	if err := r.Create(context.Background(), &other); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(t, r)
	reconcileOnce(t, r)
	p := getPreview(t, r)
	if p.Status.Phase != vesperv1alpha1.PhaseFailed {
		t.Errorf("phase = %s, want Failed", p.Status.Phase)
	}
	wantCondition(t, p, "Ready", metav1.ConditionFalse, "ImagePullFailed")
	wantCondition(t, p, "Available", metav1.ConditionFalse, "ImagePullFailed")
	wantEvents(t, rec, "Warning ImagePullFailed pod app-1 cannot pull image "+currentImage+": "+pullMessage)

	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	if err := r.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	reconcileOnce(t, r)
	wantCondition(t, getPreview(t, r), "Ready", metav1.ConditionFalse, "Provisioning")
}

// TestPodCache: the cache of the manager holds only the pods of previews.
func TestPodCache(t *testing.T) {
	var selector cache.ByObject
	for obj, by := range CacheByObject() {
		if _, ok := obj.(*corev1.Pod); ok {
			selector = by
		}
	}
	if selector.Label == nil {
		t.Fatal("no label selector for pods")
	}
	if !selector.Label.Matches(labels.Set{LabelManagedBy: ManagedByVesper}) || selector.Label.Matches(labels.Set{"app": "other"}) {
		t.Errorf("selector %s, want only %s=%s", selector.Label, LabelManagedBy, ManagedByVesper)
	}
}
