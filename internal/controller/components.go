// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// exposedComponent returns the component the Ingress of the preview routes
// to. The API server guarantees exactly one (ADR-0008); a preview that gives
// one image has one component, app, which is exposed.
func exposedComponent(preview *vesperv1alpha1.PreviewEnvironment) vesperv1alpha1.Component {
	components := preview.Spec.ComponentList()
	for _, c := range components {
		if c.Expose {
			return c
		}
	}
	return components[0]
}

// ensureComponents creates or updates a Deployment and a Service for each
// component of the preview, in spec order, then deletes those of components
// no longer in the spec. It returns the Deployments, in spec order.
func (r *PreviewEnvironmentReconciler) ensureComponents(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) ([]*appsv1.Deployment, error) {
	components := preview.Spec.ComponentList()
	deployments := make([]*appsv1.Deployment, 0, len(components))
	keep := make(map[string]bool, len(components))
	for _, c := range components {
		keep[c.Name] = true
		d, err := r.ensureDeployment(ctx, preview, namespace, c)
		if err != nil {
			return nil, err
		}
		if err := r.ensureService(ctx, preview, namespace, c); err != nil {
			return nil, err
		}
		deployments = append(deployments, d)
	}
	if err := r.deleteRemovedComponents(ctx, preview, namespace, keep); err != nil {
		return nil, err
	}
	return deployments, nil
}

// deleteRemovedComponents deletes the Deployments and the Services Vesper
// created for the preview whose component is not in keep: a component
// removed from the spec, or renamed. Objects without the labels of the
// preview are left alone.
func (r *PreviewEnvironmentReconciler) deleteRemovedComponents(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string, keep map[string]bool,
) error {
	var deployments appsv1.DeploymentList
	var services corev1.ServiceList
	for kind, list := range map[string]client.ObjectList{"deployments": &deployments, "services": &services} {
		if err := r.List(ctx, list, client.InNamespace(namespace), client.MatchingLabels(previewLabels(preview))); err != nil {
			return fmt.Errorf("list the %s of %s: %w", kind, namespace, err)
		}
	}
	var removed []client.Object
	for i := range deployments.Items {
		removed = append(removed, &deployments.Items[i])
	}
	for i := range services.Items {
		removed = append(removed, &services.Items[i])
	}
	for _, obj := range removed {
		if keep[obj.GetName()] || !ownedBy(obj, preview) {
			continue
		}
		uid := obj.GetUID()
		if err := r.Delete(ctx, obj, client.Preconditions{UID: &uid}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete %T %s/%s: %w", obj, namespace, obj.GetName(), err)
		}
		logf.FromContext(ctx).Info("deleted the object of a removed component",
			"previewNamespace", namespace, "component", obj.GetName(), "kind", fmt.Sprintf("%T", obj))
	}
	return nil
}

// componentsCondition reads the Available condition of the preview, and its
// phase, from the Deployment of each component, in spec order.
//
// With one component, it is the condition of its Deployment, as before
// components existed. With several, the preview is Ready when every
// Deployment is available; otherwise the condition is the one of the first
// component that failed or, if none failed, of the first one not available
// yet, its message prefixed with the name of the component.
func componentsCondition(
	deployments []*appsv1.Deployment, pods []corev1.Pod, previous *metav1.Condition,
) (condition, vesperv1alpha1.Phase) {
	if len(deployments) == 1 {
		return availableCondition(deployments[0], pods, previous)
	}
	var pending *condition
	for _, d := range deployments {
		c, phase := availableCondition(d, pods, previousOf(d.Name, previous))
		c.message = componentPrefix(d.Name) + c.message
		switch phase {
		case vesperv1alpha1.PhaseFailed:
			return c, phase
		case vesperv1alpha1.PhasePending:
			if pending == nil {
				pending = &c
			}
		}
	}
	if pending != nil {
		return *pending, vesperv1alpha1.PhasePending
	}
	return condition{metav1.ConditionTrue, vesperv1alpha1.ReasonDeploymentAvailable,
		fmt.Sprintf("%d/%d components available", len(deployments), len(deployments))}, vesperv1alpha1.PhaseReady
}

func componentPrefix(name string) string {
	return "component " + name + ": "
}

// previousOf returns the previous Available condition as it was for the
// component name, without its prefix, or nil if it was about another
// component.
func previousOf(name string, previous *metav1.Condition) *metav1.Condition {
	if previous == nil {
		return nil
	}
	message, ok := strings.CutPrefix(previous.Message, componentPrefix(name))
	if !ok {
		return nil
	}
	p := *previous
	p.Message = message
	return &p
}
