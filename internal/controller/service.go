// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

const (
	// ServiceName is the name of the Service of a preview that gives one
	// image, in the preview namespace. With components, each Service takes
	// the name of its component, so that they call each other by it.
	ServiceName = vesperv1alpha1.SingleComponentName
	// servicePort is the port clients call for a preview that gives one
	// image. It targets the container port by its name, so it stays the same
	// when spec.port changes.
	servicePort = 80
)

// servicePortOf returns the port of the Service of component c: 80 for a
// preview that gives one image, as in v0.2, and the port of the component
// otherwise, so that the address of a component is the one it would have
// outside Vesper (postgres:5432, api:8080).
func servicePortOf(preview *vesperv1alpha1.PreviewEnvironment, c vesperv1alpha1.Component) int32 {
	if len(preview.Spec.Components) == 0 {
		return servicePort
	}
	return componentPort(c)
}

// internalURL returns the address of the Service of component c, for a
// preview whose namespace is namespace. It assumes the default cluster
// domain.
func internalURL(preview *vesperv1alpha1.PreviewEnvironment, namespace string, c vesperv1alpha1.Component) string {
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local", c.Name, namespace)
	if port := servicePortOf(preview, c); port != servicePort {
		url += fmt.Sprintf(":%d", port)
	}
	return url
}

// ensureService creates or updates the ClusterIP Service of component c in
// namespace, in front of the pods of its Deployment.
func (r *PreviewEnvironmentReconciler) ensureService(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string, c vesperv1alpha1.Component,
) error {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: c.Name, Namespace: namespace}}
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, s, func() error {
		if s.ResourceVersion != "" && !ownedBy(s, preview) { // it already exists
			return fmt.Errorf("service %s/%s exists and was not created by Vesper for this preview",
				namespace, c.Name)
		}
		mutateService(s, preview, c)
		return nil
	})
	if err != nil {
		return fmt.Errorf("create or update service %s/%s: %w", namespace, c.Name, err)
	}
	if result != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info("service "+string(result), "previewNamespace", namespace, "component", c.Name)
	}
	return nil
}

// mutateService sets the fields Vesper owns on s, the Service of component
// c, and only those: the API server fills in the rest (clusterIP, which can
// never change, ipFamilies, sessionAffinity...).
func mutateService(s *corev1.Service, preview *vesperv1alpha1.PreviewEnvironment, c vesperv1alpha1.Component) {
	setPreviewLabels(s, preview)

	// Reachable from inside the cluster only. The public address is the job
	// of an Ingress.
	s.Spec.Type = corev1.ServiceTypeClusterIP
	s.Spec.Selector = podSelector(preview, c)
	s.Spec.Ports = []corev1.ServicePort{{
		Name:       portName,
		Protocol:   corev1.ProtocolTCP,
		Port:       servicePortOf(preview, c),
		TargetPort: intstr.FromString(portName),
	}}
}
