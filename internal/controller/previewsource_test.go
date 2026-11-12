// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// validSourceSpec is a PreviewSource the API server accepts, as in
// config/samples.
func validSourceSpec() vesperv1alpha1.PreviewSourceSpec {
	return vesperv1alpha1.PreviewSourceSpec{
		Repository:    "acme/app",
		Image:         "ghcr.io/acme/app:pr-{{number}}-{{sha}}",
		BuildWorkflow: "preview.yml",
		Namespace:     "previews-acme",
	}
}

// createSource creates a PreviewSource named name, and deletes it at the end
// of the test: it is cluster-scoped, so names must not collide between tests.
func createSource(t *testing.T, name string, spec vesperv1alpha1.PreviewSourceSpec) (*vesperv1alpha1.PreviewSource, error) {
	t.Helper()
	source := &vesperv1alpha1.PreviewSource{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: spec}
	err := envClient.Create(context.Background(), source)
	if err == nil {
		t.Cleanup(func() {
			if err := envClient.Delete(context.Background(), source); err != nil && !apierrors.IsNotFound(err) {
				t.Errorf("delete source %s: %v", name, err)
			}
		})
	}
	return source, err
}

// TestEnvtestPreviewSourceDefaults creates a minimal PreviewSource: the API
// server fills in the trigger, the fork policy and the defaults of previews.
func TestEnvtestPreviewSourceDefaults(t *testing.T) {
	requireEnvtest(t)
	spec := validSourceSpec()
	spec.BuildWorkflow = ""
	source, err := createSource(t, "defaults", spec)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got := source.Spec
	if got.Trigger != vesperv1alpha1.TriggerAll || got.ForkPullRequests != vesperv1alpha1.ForkDeny {
		t.Errorf("trigger %q, forks %q; want All, Deny", got.Trigger, got.ForkPullRequests)
	}
	d := got.Defaults
	if d.Port != 8080 || d.Replicas != 1 || d.TTL == nil || d.TTL.Duration != 24*time.Hour {
		t.Errorf("defaults: port %d, replicas %d, ttl %v; want 8080, 1, 24h", d.Port, d.Replicas, d.TTL)
	}
}

// TestEnvtestPreviewSourceAccepts creates the forms a PreviewSource may take.
func TestEnvtestPreviewSourceAccepts(t *testing.T) {
	requireEnvtest(t)
	ttl := metav1.Duration{Duration: 168 * time.Hour}

	tests := []struct {
		name   string
		mutate func(*vesperv1alpha1.PreviewSourceSpec)
	}{
		{"sample", nil},
		{"sha only, on Docker Hub", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Image = "acme/app:{{sha}}" }},
		{"registry with a port", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Image = "registry.example.com:5000/team/app:pr-{{number}}-{{sha}}"
		}},
		{"repository with dots and underscores", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Repository = "Acme-Corp/my_app.web" }},
		{"opt-in with a label, forks with a label", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Trigger, s.ForkPullRequests = vesperv1alpha1.TriggerLabel, vesperv1alpha1.ForkRequireLabel
		}},
		{"workflow in yaml", func(s *vesperv1alpha1.PreviewSourceSpec) { s.BuildWorkflow = "build-preview.yaml" }},
		{"longest ttl", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Defaults.TTL = &ttl }},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validSourceSpec()
			if tt.mutate != nil {
				tt.mutate(&spec)
			}
			if _, err := createSource(t, fmt.Sprintf("accepts-%d", i), spec); err != nil {
				t.Errorf("create: %v, want it accepted", err)
			}
		})
	}
}

// TestEnvtestPreviewSourceValidation sends invalid sources: the API server
// rejects them with the rules of the CRD schema, with a message that names
// the field or says what is wrong.
func TestEnvtestPreviewSourceValidation(t *testing.T) {
	requireEnvtest(t)
	ttl := func(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

	tests := []struct {
		name    string
		source  string // the name of the source, if not the default
		mutate  func(*vesperv1alpha1.PreviewSourceSpec)
		message string // part of the error message
	}{
		{"repository without owner", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Repository = "app" }, "spec.repository"},
		{"repository as a URL", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Repository = "https://github.com/acme/app"
		}, "spec.repository"},
		{"repository named ..", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Repository = "acme/.." }, "cannot be . or .."},
		{"owner ending with a hyphen", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Repository = "acme-/app" }, "spec.repository"},
		{"image without sha", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Image = "ghcr.io/acme/app:pr-{{number}}"
		}, "must contain {{sha}}"},
		{"image with latest", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Image = "ghcr.io/acme/app-{{sha}}:latest"
		}, "must not use the tag latest"},
		{"image without tag", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Image = "ghcr.io/acme/app-{{sha}}"
		}, "valid image reference"},
		{"unknown placeholder", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Image = "ghcr.io/acme/app:{{branch}}-{{sha}}"
		}, "the only placeholders"},
		{"uppercase image", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Image = "ghcr.io/Acme/app:{{sha}}"
		}, "valid image reference"},
		{"workflow not yaml", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.BuildWorkflow = "preview.sh" }, "spec.buildWorkflow"},
		{"workflow with a path", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.BuildWorkflow = ".github/workflows/preview.yml"
		}, "spec.buildWorkflow"},
		{"no namespace", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Namespace = "" }, "spec.namespace"},
		{"invalid namespace", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Namespace = "Previews" }, "spec.namespace"},
		{"unknown trigger", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Trigger = "Sometimes" }, "spec.trigger"},
		{"unknown fork policy", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.ForkPullRequests = "Allow" }, "spec.forkPullRequests"},
		{"port too high", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Defaults.Port = 70000 }, "spec.defaults.port"},
		{"too many replicas", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Defaults.Replicas = 6 }, "spec.defaults.replicas"},
		{"ttl too long", "", func(s *vesperv1alpha1.PreviewSourceSpec) { s.Defaults.TTL = ttl(200 * time.Hour) }, "spec.defaults.ttl"},
		{"bad env name", "", func(s *vesperv1alpha1.PreviewSourceSpec) {
			s.Defaults.Env = []vesperv1alpha1.EnvVar{{Name: "1BAD"}}
		}, "spec.defaults.env"},
		{"name over 40 characters", strings.Repeat("a", 41), nil, "at most 40"},
		{"name with a dot", "acme.app", nil, "at most 40"},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := validSourceSpec()
			if tt.mutate != nil {
				tt.mutate(&spec)
			}
			name := fmt.Sprintf("invalid-%d", i)
			if tt.source != "" {
				name = tt.source
			}
			_, err := createSource(t, name, spec)
			if !apierrors.IsInvalid(err) {
				t.Fatalf("create: %v, want an Invalid error", err)
			}
			if !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error %q does not contain %q", err, tt.message)
			}
		})
	}
}
