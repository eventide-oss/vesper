// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// The waiting reasons of the kubelet that mean the image of a container
// cannot be pulled, and that it keeps crashing.
var (
	imagePullReasons = []string{"ErrImagePull", "ImagePullBackOff", "InvalidImageName"}
	crashLoopReason  = "CrashLoopBackOff"
)

// maxPodMessage bounds the message of the kubelet kept in a condition.
const maxPodMessage = 512

// podCache keeps only the pods of previews in the cache of the manager: the
// operator reads them to tell why a preview does not start, and caching every
// pod of the cluster would cost memory for nothing.
func podCache() cache.ByObject {
	return cache.ByObject{Label: labels.SelectorFromSet(labels.Set{LabelManagedBy: ManagedByVesper})}
}

// listPods returns the pods of the preview, in its namespace.
func (r *PreviewEnvironmentReconciler) listPods(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) ([]corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(namespace), client.MatchingLabels(previewLabels(preview))); err != nil {
		return nil, fmt.Errorf("list the pods of %s: %w", namespace, err)
	}
	return pods.Items, nil
}

// podFailure tells whether a pod of the current image of d cannot run: its
// image cannot be pulled, or its application exits again and again. The
// Deployment reports neither before its progress deadline, 10 minutes (#107).
// Pods of an older image, still running during a rollout, do not count.
//
// previous is the Available condition the preview shows: while the kubelet
// waits before it pulls again, it gives no cause, so the message of the
// failed pull is kept rather than replaced.
func podFailure(d *appsv1.Deployment, pods []corev1.Pod, previous *metav1.Condition) (condition, bool) {
	image := templateImage(d)
	if image == "" {
		return condition{}, false
	}
	slices.SortFunc(pods, func(a, b corev1.Pod) int { return strings.Compare(a.Name, b.Name) })
	for _, pod := range pods {
		if !pod.DeletionTimestamp.IsZero() || !runsImage(pod, image) {
			continue
		}
		for _, s := range pod.Status.ContainerStatuses {
			if s.Name != containerName || s.State.Waiting == nil {
				continue
			}
			waiting := s.State.Waiting
			switch {
			case slices.Contains(imagePullReasons, waiting.Reason):
				if waiting.Reason == "ImagePullBackOff" && previous != nil &&
					previous.Reason == vesperv1alpha1.ReasonImagePullFailed {
					return condition{metav1.ConditionFalse, vesperv1alpha1.ReasonImagePullFailed, previous.Message}, true
				}
				return condition{metav1.ConditionFalse, vesperv1alpha1.ReasonImagePullFailed, truncate(fmt.Sprintf(
					"pod %s cannot pull image %s: %s", pod.Name, image, waiting.Message), maxPodMessage)}, true
			case waiting.Reason == crashLoopReason:
				return condition{metav1.ConditionFalse, vesperv1alpha1.ReasonCrashLooping,
					crashMessage(pod, s, d.Namespace)}, true
			}
		}
	}
	return condition{}, false
}

// crashMessage says how the application of pod last exited, without the
// restart count or the back-off of the kubelet: they change at each restart,
// and the message would with them.
func crashMessage(pod corev1.Pod, s corev1.ContainerStatus, namespace string) string {
	exit := ""
	if t := s.LastTerminationState.Terminated; t != nil {
		exit = fmt.Sprintf(", last with exit code %d", t.ExitCode)
		if t.Reason != "" {
			exit += " (" + t.Reason + ")"
		}
	}
	return fmt.Sprintf("the application of pod %s keeps exiting%s: see kubectl logs -n %s %s --previous",
		pod.Name, exit, namespace, pod.Name)
}

// templateImage returns the image of the application in the template of d.
func templateImage(d *appsv1.Deployment) string {
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return ""
}

// runsImage reports whether the application container of pod has image.
func runsImage(pod corev1.Pod, image string) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == containerName {
			return c.Image == image
		}
	}
	return false
}

// truncate cuts s to n bytes, on a rune boundary, with an ellipsis if it was
// longer.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
