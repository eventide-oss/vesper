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
	// ServiceName is the name of the Service of a preview, in the preview
	// namespace.
	ServiceName = "app"
	// servicePort is the port clients call. It targets the container port by
	// its name, so it stays the same when spec.port changes.
	servicePort = 80
)

// internalURL returns the address of the Service of a preview whose
// namespace is namespace. It assumes the default cluster domain.
func internalURL(namespace string) string {
	return fmt.Sprintf("http://%s.%s.svc.cluster.local", ServiceName, namespace)
}

// ensureService creates or updates the ClusterIP Service of the preview in
// namespace, in front of the pods of its Deployment.
func (r *PreviewEnvironmentReconciler) ensureService(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) error {
	s := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ServiceName, Namespace: namespace}}
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, s, func() error {
		if s.ResourceVersion != "" && !ownedBy(s, preview) { // it already exists
			return fmt.Errorf("service %s/%s exists and was not created by Vesper for this preview",
				namespace, ServiceName)
		}
		mutateService(s, preview)
		return nil
	})
	if err != nil {
		return fmt.Errorf("create or update service %s/%s: %w", namespace, ServiceName, err)
	}
	if result != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info("service "+string(result), "previewNamespace", namespace)
	}
	return nil
}

// mutateService sets the fields Vesper owns on s, and only those: the API
// server fills in the rest (clusterIP, which can never change, ipFamilies,
// sessionAffinity...).
func mutateService(s *corev1.Service, preview *vesperv1alpha1.PreviewEnvironment) {
	setPreviewLabels(s, preview)

	// Reachable from inside the cluster only. The public address is the job
	// of an Ingress.
	s.Spec.Type = corev1.ServiceTypeClusterIP
	s.Spec.Selector = map[string]string{appLabel: appLabelValue}
	s.Spec.Ports = []corev1.ServicePort{{
		Name:       portName,
		Protocol:   corev1.ProtocolTCP,
		Port:       servicePort,
		TargetPort: intstr.FromString(portName),
	}}
}
