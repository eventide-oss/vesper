// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// stackSpec is a preview of three components: a frontend, an API and a
// database (ADR-0008).
func stackSpec() vesperv1alpha1.PreviewEnvironmentSpec {
	return vesperv1alpha1.PreviewEnvironmentSpec{Components: []vesperv1alpha1.Component{
		{Name: "web", Image: "ghcr.io/acme/web:pr-42", Port: 3000, Expose: true},
		{Name: "api", Image: "ghcr.io/acme/api:pr-42",
			Env: []vesperv1alpha1.EnvVar{{Name: "DATABASE_HOST", Value: "postgres"}}},
		{Name: "postgres", Image: "postgres:17.2", Port: 5432},
	}}
}

// TestEnvtestAcceptsComponents creates a preview of three components: the
// API server accepts it and fills the defaults of each component.
func TestEnvtestAcceptsComponents(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, stackSpec())

	var preview vesperv1alpha1.PreviewEnvironment
	if err := p.get(&preview); err != nil {
		t.Fatalf("get preview: %v", err)
	}
	got := preview.Spec.Components
	if len(got) != 3 {
		t.Fatalf("components = %+v, want 3", got)
	}
	for _, c := range []struct {
		i              int
		name           string
		port, replicas int32
	}{{0, "web", 3000, 1}, {1, "api", 8080, 1}, {2, "postgres", 5432, 1}} {
		if got[c.i].Name != c.name || got[c.i].Port != c.port || got[c.i].Replicas != c.replicas {
			t.Errorf("component %d = %+v, want %s with port %d and %d replica", c.i, got[c.i], c.name, c.port, c.replicas)
		}
	}
	// The top-level defaults are still filled in, and allowed next to
	// components, since they are only the defaults.
	if preview.Spec.Port != 8080 || preview.Spec.Replicas != 1 {
		t.Errorf("top-level port %d, replicas %d; want the defaults 8080 and 1", preview.Spec.Port, preview.Spec.Replicas)
	}
}

// TestEnvtestComponentsNotSupportedYet checks that the controller reports a
// preview with components as not supported, and creates nothing for it,
// until it runs them.
func TestEnvtestComponentsNotSupportedYet(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, stackSpec())

	preview := p.waitForStatus(t, vesperv1alpha1.PhaseFailed, vesperv1alpha1.ReasonComponentsNotSupported)
	if preview.Status.Namespace != "" || preview.Status.URL != "" {
		t.Errorf("status = %+v, want no namespace and no URL", preview.Status)
	}
	err := envClient.Get(context.Background(), types.NamespacedName{Name: p.previewNamespace}, &corev1.Namespace{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("get preview namespace: %v, want NotFound", err)
	}
}

// TestEnvtestComponentsValidation sends invalid previews with components: the
// API server rejects them with the rules of the CRD schema.
func TestEnvtestComponentsValidation(t *testing.T) {
	requireEnvtest(t)
	namespace := newUserNamespace(t)

	tests := []struct {
		name    string
		mutate  func(*vesperv1alpha1.PreviewEnvironmentSpec)
		message string // part of the error message
	}{
		{"neither image nor components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components = nil
		}, "give image, for one application, or components"},
		{"image and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Image = "ghcr.io/acme/app:pr-42"
		}, "not at the top of spec"},
		{"env and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Env = []vesperv1alpha1.EnvVar{{Name: "LOG_LEVEL"}}
		}, "not at the top of spec"},
		{"port and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Port = 3000
		}, "not at the top of spec"},
		{"replicas and components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Replicas = 2
		}, "not at the top of spec"},
		{"no component exposed", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[0].Expose = false
		}, "exactly one component must have expose: true"},
		{"two components exposed", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Expose = true
		}, "exactly one component must have expose: true"},
		{"duplicate names", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Name = "web"
		}, "spec.components[1]"},
		{"too many components", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			for i := len(s.Components); i <= vesperv1alpha1.MaxComponents; i++ {
				s.Components = append(s.Components,
					vesperv1alpha1.Component{Name: fmt.Sprintf("worker-%d", i), Image: "ghcr.io/acme/worker:1"})
			}
		}, "spec.components"},
		{"name not a DNS label", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Name = "API"
		}, "spec.components[1].name"},
		{"name starts with a digit", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Name = "1api"
		}, "spec.components[1].name"},
		{"latest tag", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[2].Image = "postgres:latest"
		}, "spec.components[2].image"},
		{"no tag", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[2].Image = "postgres"
		}, "spec.components[2].image"},
		{"port too high", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[0].Port = 70000
		}, "spec.components[0].port"},
		{"too many replicas", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[0].Replicas = 6
		}, "spec.components[0].replicas"},
		{"bad env name", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Components[1].Env = []vesperv1alpha1.EnvVar{{Name: "1BAD"}}
		}, "spec.components[1].env"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := stackSpec()
			tt.mutate(&spec)
			preview := &vesperv1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "pr-1"},
				Spec:       spec,
			}
			err := envClient.Create(context.Background(), preview)
			if !apierrors.IsInvalid(err) {
				t.Fatalf("create: %v, want an Invalid error", err)
			}
			if !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error %q does not contain %q", err, tt.message)
			}
		})
	}
}
