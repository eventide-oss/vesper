// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"maps"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// Labels that tie every object Vesper creates to its preview.
//
// Vesper cannot use owner references for this. The preview lives in the
// user's namespace, its Deployment and Service in the preview namespace, and
// an owner reference cannot point to another namespace. The preview namespace
// is cluster-scoped and cannot have a namespaced owner either. The labels
// record the owner instead, and the preview namespace is the cleanup
// boundary: deleting it deletes everything in it.
const (
	LabelManagedBy        = "app.kubernetes.io/managed-by"
	ManagedByVesper       = "vesper"
	LabelPreviewNamespace = "vesper.eventide.dev/preview-namespace"
	LabelPreviewName      = "vesper.eventide.dev/preview-name"
)

// previewLabels returns the labels that tie an object to its preview.
func previewLabels(preview *vesperv1alpha1.PreviewEnvironment) map[string]string {
	return map[string]string{
		LabelManagedBy:        ManagedByVesper,
		LabelPreviewNamespace: preview.Namespace,
		LabelPreviewName:      preview.Name,
	}
}

// setPreviewLabels adds the preview labels to obj and keeps its other labels.
func setPreviewLabels(obj metav1.Object, preview *vesperv1alpha1.PreviewEnvironment) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	maps.Copy(labels, previewLabels(preview))
	obj.SetLabels(labels)
}

// ownedBy reports whether Vesper created obj for preview. The controller
// checks it before it modifies an object, so it never takes over an object
// someone else created.
func ownedBy(obj metav1.Object, preview *vesperv1alpha1.PreviewEnvironment) bool {
	labels := obj.GetLabels()
	return labels[LabelManagedBy] == ManagedByVesper &&
		labels[LabelPreviewNamespace] == preview.Namespace &&
		labels[LabelPreviewName] == preview.Name
}

// managedByVesper filters the events to the objects Vesper created.
func managedByVesper(obj client.Object) bool {
	return obj.GetLabels()[LabelManagedBy] == ManagedByVesper
}

// previewOfObject maps an object Vesper created to its preview.
func previewOfObject(_ context.Context, obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	namespace, name := labels[LabelPreviewNamespace], labels[LabelPreviewName]
	if namespace == "" || name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: namespace, Name: name}}}
}
