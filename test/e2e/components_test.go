// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
	"github.com/eventide-oss/vesper/internal/controller"
)

// TestPreviewComponents runs a preview of two components (ADR-0008): it is
// Ready once both run, and a pod of the preview reaches each component by
// the name of its Service alone, on the port of the component, as the
// components reach each other.
func TestPreviewComponents(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "vesper-e2e-"}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	const image = "nginxinc/nginx-unprivileged:1.27"
	preview := &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "pr-stack"},
		Spec: vesperv1alpha1.PreviewEnvironmentSpec{Components: []vesperv1alpha1.Component{
			{Name: "web", Image: image, Port: 8080, Expose: true},
			{Name: "api", Image: image, Port: 8080},
		}},
	}
	previewNamespace := controller.NamespaceName(preview.Namespace, preview.Name)
	t.Cleanup(func() { cleanup(t, c, ns.Name, previewNamespace) })
	if err := c.Create(ctx, preview); err != nil {
		t.Fatalf("create preview: %v", err)
	}
	waitForReady(t, c, preview)

	expectReachable(t, c, previewNamespace, "api", 8080, true)
	expectReachable(t, c, previewNamespace, "web", 8080, true)
}
