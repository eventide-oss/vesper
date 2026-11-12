// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// readyPreview returns a fully populated PreviewEnvironment, created two
// hours before now and expiring 22 hours after it.
func readyPreview(now time.Time) *unstructured.Unstructured {
	obj := newPreview("team-a", "pr-42", "Ready", 0)
	obj.SetCreationTimestamp(metav1.NewTime(now.Add(-2 * time.Hour)))
	obj.Object["spec"] = map[string]any{
		"image":    "ghcr.io/eventide/app:pr-42",
		"port":     int64(8080),
		"replicas": int64(1),
		"ttl":      "24h",
		"env": []any{
			map[string]any{"name": "API_TOKEN", "value": "s3cr3t-value"},
			map[string]any{"name": "LOG_LEVEL", "value": "debug"},
		},
	}
	obj.Object["status"] = map[string]any{
		"phase":     "Ready",
		"namespace": "vesper-team-a-pr-42-3f9c2a1b",
		"url":       "https://pr-42.preview.example.com",
		"expiresAt": now.Add(22 * time.Hour).UTC().Format(time.RFC3339),
		"conditions": []any{
			map[string]any{
				"type": "Available", "status": "True", "reason": "DeploymentReady",
				"message":            "1/1 replicas available",
				"lastTransitionTime": now.Add(-90 * time.Minute).UTC().Format(time.RFC3339),
			},
			map[string]any{
				"type": "Progressing", "status": "False", "reason": "Complete",
				"lastTransitionTime": now.Add(-90 * time.Minute).UTC().Format(time.RFC3339),
			},
		},
	}
	return obj
}

func TestPrintPreviewDetails(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		item *unstructured.Unstructured
		want []string
	}{
		{
			name: "all fields",
			item: readyPreview(now),
			want: []string{
				"Name:        pr-42",
				"Namespace:   team-a",
				"Runs in:     vesper-team-a-pr-42-3f9c2a1b",
				"Created:     2026-11-05T10:00:00Z (120m ago)",
				"Phase:       Ready",
				"URL:         https://pr-42.preview.example.com",
				"Expires:     2026-11-06T10:00:00Z (in 22h)",
				"Spec:",
				"  Image:     ghcr.io/eventide/app:pr-42",
				"  Port:      8080",
				"  Replicas:  1",
				"  TTL:       24h",
				"  Env:       2 variables",
				"Conditions:",
				"  TYPE          STATUS   REASON            AGE   MESSAGE",
				"  Available     True     DeploymentReady   90m   1/1 replicas available",
				"  Progressing   False    Complete          90m   <none>",
			},
		},
		{
			name: "status not set yet",
			item: func() *unstructured.Unstructured {
				obj := newPreview("team-a", "pr-12", "", 0)
				obj.SetCreationTimestamp(metav1.NewTime(now.Add(-5 * time.Minute)))
				obj.Object["spec"] = map[string]any{"image": "nginx:1.27"}
				return obj
			}(),
			want: []string{
				"Name:        pr-12",
				"Namespace:   team-a",
				"Runs in:     <none>",
				"Created:     2026-11-05T11:55:00Z (5m ago)",
				"Phase:       <none>",
				"URL:         <none>",
				"Expires:     <none>",
				"Spec:",
				"  Image:     nginx:1.27",
				"  Port:      <none>",
				"  Replicas:  <none>",
				"  TTL:       <none>",
				"  Env:       <none>",
				"Conditions:  <none>",
			},
		},
		{
			name: "expired, not deleted yet",
			item: func() *unstructured.Unstructured {
				obj := newPreview("team-a", "pr-1", "Expired", 0)
				obj.SetCreationTimestamp(metav1.NewTime(now.Add(-25 * time.Hour)))
				obj.Object["status"] = map[string]any{
					"phase":     "Expired",
					"expiresAt": now.Add(-time.Hour).UTC().Format(time.RFC3339),
				}
				return obj
			}(),
			want: []string{"Expires:     2026-11-05T11:00:00Z (expired 60m ago)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := printPreviewDetails(&out, newPreviewSummary(*tt.item), now); err != nil {
				t.Fatalf("printPreviewDetails: %v", err)
			}
			got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
			if len(tt.want) == len(got) {
				if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
					t.Errorf("output =\n%s\nwant\n%s", out.String(), strings.Join(tt.want, "\n"))
				}
				return
			}
			// A partial expectation: every wanted line must be present.
			for _, line := range tt.want {
				if !strings.Contains(out.String(), line+"\n") {
					t.Errorf("output =\n%s\nwant it to contain %q", out.String(), line)
				}
			}
		})
	}
}

func TestPrintPreviewDetailsHidesEnvValues(t *testing.T) {
	var out bytes.Buffer
	if err := printPreviewDetails(&out, newPreviewSummary(*readyPreview(time.Now())), time.Now()); err != nil {
		t.Fatalf("printPreviewDetails: %v", err)
	}
	for _, secret := range []string{"s3cr3t-value", "API_TOKEN"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("output contains %q from spec.env:\n%s", secret, out.String())
		}
	}
}

func TestGet(t *testing.T) {
	client := newFakeClient(readyPreview(testNow))

	code, stdout, stderr := executeWith(t, []string{"get", "pr-42", "-n", "team-a"},
		func(opts *globalOptions) clientFactory {
			return &fakeFactory{opts: opts, client: client}
		})

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want it empty", stderr)
	}
	for _, want := range []string{"Name:        pr-42\n", "Phase:       Ready\n", "Conditions:\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout =\n%s\nwant it to contain %q", stdout, want)
		}
	}
}

func TestGetErrors(t *testing.T) {
	// missingCRD makes the fake client answer like an API server without the
	// PreviewEnvironment CRD: 404 without the object name in the details.
	missingCRD := func() *dynamicfake.FakeDynamicClient {
		client := newFakeClient()
		client.PrependReactor("get", "previewenvironments",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewNotFound(previewEnvironmentGVR.GroupResource(), "")
			})
		return client
	}

	tests := []struct {
		name       string
		args       []string
		client     *dynamicfake.FakeDynamicClient
		wantCode   int
		wantStderr string
	}{
		{
			name:       "not found",
			args:       []string{"get", "pr-404"},
			client:     newFakeClient(newPreview("team-a", "pr-404", "Ready", time.Minute)),
			wantCode:   exitFailure,
			wantStderr: "vesperctl: preview environment \"pr-404\" not found in namespace \"default\"\n",
		},
		{
			name:     "missing CRD",
			args:     []string{"get", "pr-1"},
			client:   missingCRD(),
			wantCode: exitFailure,
			wantStderr: "vesperctl: the PreviewEnvironment API (previewenvironments.vesper.eventide.dev) " +
				"is not installed in this cluster: install Vesper, or check --context\n",
		},
		{
			name:       "missing name",
			args:       []string{"get"},
			client:     newFakeClient(),
			wantCode:   exitUsage,
			wantStderr: "vesperctl: accepts 1 arg(s), received 0",
		},
		{
			name:       "too many names",
			args:       []string{"get", "a", "b"},
			client:     newFakeClient(),
			wantCode:   exitUsage,
			wantStderr: "vesperctl: accepts 1 arg(s), received 2",
		},
		{
			name:       "invalid name",
			args:       []string{"get", "../secrets"},
			client:     newFakeClient(),
			wantCode:   exitUsage,
			wantStderr: `vesperctl: invalid name "../secrets": a lowercase RFC 1123 subdomain`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := executeWith(t, tt.args, func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: tt.client}
			})

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (stderr: %q)", code, tt.wantCode, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want it empty", stdout)
			}
			if !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantStderr)
			}
		})
	}
}

func TestGetStdoutWriteError(t *testing.T) {
	client := newFakeClient(newPreview("default", "pr-1", "Ready", time.Minute))
	opts := newGlobalOptions(emptyEnv)
	root := newRootCmd(failingWriter{}, io.Discard, opts, &fakeFactory{opts: opts, client: client})

	if code := execute(root, []string{"get", "pr-1"}, io.Discard); code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
}
