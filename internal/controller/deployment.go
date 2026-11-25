// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

const (
	// DeploymentName is the name of the Deployment of a preview that gives
	// one image, in the preview namespace. With components, each Deployment
	// takes the name of its component.
	DeploymentName = vesperv1alpha1.SingleComponentName
	portName       = "http"

	// appLabel selects the pods of a preview that gives one image. A
	// Deployment's selector cannot change after its creation, so it stays
	// fixed and minimal, as v0.2 created it.
	appLabel      = "app.kubernetes.io/name"
	appLabelValue = "preview-app"
	// LabelComponent selects the pods of one component, for a preview that
	// gives components.
	LabelComponent = "vesper.eventide.dev/component"
)

// podSelector returns the labels that select the pods of component c. A
// preview that gives one image keeps the selector of v0.2, so that its
// Deployment is not recreated when Vesper is upgraded.
func podSelector(preview *vesperv1alpha1.PreviewEnvironment, c vesperv1alpha1.Component) map[string]string {
	if len(preview.Spec.Components) == 0 {
		return map[string]string{appLabel: appLabelValue}
	}
	return map[string]string{LabelComponent: c.Name}
}

// ensureDeployment creates or updates the Deployment of component c in
// namespace, and returns it as read or written.
//
// The selector of a Deployment cannot change. When the preview switches
// between one image and components, the selector of a Deployment it keeps
// (the component app) changes: the Deployment is deleted and created again.
func (r *PreviewEnvironmentReconciler) ensureDeployment(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string, c vesperv1alpha1.Component,
) (*appsv1.Deployment, error) {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: namespace}}
	if err := r.Get(ctx, client.ObjectKeyFromObject(d), d); err == nil && ownedBy(d, preview) &&
		!selectorIs(d, podSelector(preview, c)) {
		if err := r.Delete(ctx, d, client.Preconditions{UID: &d.UID}); err != nil && !apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("delete deployment %s/%s to change its selector: %w", namespace, c.Name, err)
		}
		logf.FromContext(ctx).Info("deployment deleted to change its selector",
			"previewNamespace", namespace, "component", c.Name)
		d = &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: namespace}}
		mutateDeployment(d, preview, c)
		// The cache may still hold the old Deployment: create it directly.
		// If it is not gone yet, the error makes the reconciliation retry.
		if err := r.Create(ctx, d); err != nil {
			return nil, fmt.Errorf("create deployment %s/%s: %w", namespace, c.Name, err)
		}
		return d, nil
	} else if err != nil && !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get deployment %s/%s: %w", namespace, c.Name, err)
	}

	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, d, func() error {
		if d.ResourceVersion != "" && !ownedBy(d, preview) { // it already exists
			return fmt.Errorf("deployment %s/%s exists and was not created by Vesper for this preview",
				namespace, c.Name)
		}
		mutateDeployment(d, preview, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create or update deployment %s/%s: %w", namespace, c.Name, err)
	}
	if result != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info("deployment "+string(result),
			"previewNamespace", namespace, "component", c.Name, "image", c.Image)
	}
	return d, nil
}

// selectorIs reports whether the selector of d is exactly matchLabels.
func selectorIs(d *appsv1.Deployment, matchLabels map[string]string) bool {
	s := d.Spec.Selector
	return s != nil && len(s.MatchExpressions) == 0 && maps.Equal(s.MatchLabels, matchLabels)
}

// mutateDeployment sets the fields Vesper owns on d, the Deployment of
// component c, and only those: the API server adds defaults to the rest
// (imagePullPolicy, strategy...), and overwriting them would make every
// reconciliation update the Deployment.
func mutateDeployment(d *appsv1.Deployment, preview *vesperv1alpha1.PreviewEnvironment, c vesperv1alpha1.Component) {
	setPreviewLabels(d, preview)

	selector := podSelector(preview, c)
	d.Spec.Replicas = ptr.To(max(c.Replicas, 1))
	if d.Spec.Selector == nil { // immutable once created
		d.Spec.Selector = &metav1.LabelSelector{MatchLabels: selector}
	}

	tmpl := &d.Spec.Template
	setPreviewLabels(&tmpl.ObjectMeta, preview)
	maps.Copy(tmpl.Labels, selector)

	// The restricted Pod Security Standard, which the namespace enforces.
	pod := &tmpl.Spec
	pod.AutomountServiceAccountToken = ptr.To(false)
	if pod.SecurityContext == nil {
		pod.SecurityContext = &corev1.PodSecurityContext{}
	}
	pod.SecurityContext.RunAsNonRoot = ptr.To(true)
	pod.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}

	container := namedContainer(pod, c.Name)
	container.Image = c.Image
	container.Ports = []corev1.ContainerPort{{Name: portName, ContainerPort: componentPort(c), Protocol: corev1.ProtocolTCP}}
	container.Env = nil
	for _, e := range c.Env {
		container.Env = append(container.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
	}
	container.SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
	}
}

// namedContainer returns the container called name in pod, adding it if
// needed, so that the defaults of an existing container are kept. The
// container takes the name of its component: app for a preview that gives
// one image.
func namedContainer(pod *corev1.PodSpec, name string) *corev1.Container {
	for i := range pod.Containers {
		if pod.Containers[i].Name == name {
			return &pod.Containers[i]
		}
	}
	pod.Containers = append(pod.Containers, corev1.Container{Name: name})
	return &pod.Containers[len(pod.Containers)-1]
}

// componentPort returns the port of c. The API server sets its default, so 0
// only happens for an object it did not validate.
func componentPort(c vesperv1alpha1.Component) int32 {
	if c.Port == 0 {
		return 8080
	}
	return c.Port
}

// deploymentAvailable reports whether d runs its latest version on every
// replica, and how many replicas are available. While a rollout is in
// progress, the message says so: the old pods stay available, so a bare
// "N/N replicas available" reads as if nothing were pending (#155).
// A rollout is in progress when pods of an older version still exist, that
// is when there are more pods than updated ones. On a first start no pod
// exists yet, so that case keeps the plain message.
func deploymentAvailable(d *appsv1.Deployment) (bool, string) {
	want := ptr.Deref(d.Spec.Replicas, 1)
	s := d.Status
	message := fmt.Sprintf("%d/%d replicas available", s.AvailableReplicas, want)
	if s.Replicas > s.UpdatedReplicas {
		message = fmt.Sprintf("rolling out: %d/%d replicas updated, %d/%d available",
			s.UpdatedReplicas, want, s.AvailableReplicas, want)
	}
	available := s.ObservedGeneration >= d.Generation &&
		s.UpdatedReplicas == want && s.Replicas == want && s.AvailableReplicas == want
	return available, message
}
