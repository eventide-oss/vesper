package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// executeCreate runs vesperctl with args against client.
func executeCreate(t *testing.T, client *dynamicfake.FakeDynamicClient, args ...string) (int, string, string) {
	t.Helper()
	return executeWith(t, args, func(opts *globalOptions) clientFactory {
		return &fakeFactory{opts: opts, client: client}
	})
}

// createdSpec returns the spec of the preview team-a/pr-42 in client.
func createdSpec(t *testing.T, client *dynamicfake.FakeDynamicClient) map[string]any {
	t.Helper()
	obj, err := client.Resource(previewEnvironmentGVR).Namespace("team-a").
		Get(context.Background(), "pr-42", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get created preview: %v", err)
	}
	spec, _ := obj.Object["spec"].(map[string]any)
	return spec
}

func TestCreate(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantSpec map[string]any
	}{
		{"image only: the API server sets the defaults", nil,
			map[string]any{"image": "ghcr.io/acme/app:pr-42"}},
		{"every flag", []string{"--port", "3000", "--replicas", "2", "--ttl", "90m",
			"--env", "LOG_LEVEL=debug", "--env", "GREETING=a=b", "--env", "EMPTY="},
			map[string]any{
				"image": "ghcr.io/acme/app:pr-42", "port": int64(3000), "replicas": int64(2), "ttl": "1h30m0s",
				"env": []any{
					map[string]any{"name": "LOG_LEVEL", "value": "debug"},
					map[string]any{"name": "GREETING", "value": "a=b"},
					map[string]any{"name": "EMPTY"},
				},
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient()
			args := append([]string{"create", "pr-42", "-n", "team-a", "--image", "ghcr.io/acme/app:pr-42"}, tt.args...)
			code, stdout, stderr := executeCreate(t, client, args...)
			if code != exitOK {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr)
			}
			want := "preview environment \"pr-42\" created in namespace \"team-a\"\n" +
				"Follow it with: vesperctl get pr-42 -n team-a\n"
			if stdout != want {
				t.Errorf("stdout = %q, want %q", stdout, want)
			}
			if got := createdSpec(t, client); !reflect.DeepEqual(got, tt.wantSpec) {
				t.Errorf("spec = %#v, want %#v", got, tt.wantSpec)
			}
		})
	}
}

func TestCreateDryRun(t *testing.T) {
	client := newFakeClient()
	code, stdout, stderr := executeCreate(t, client,
		"create", "pr-42", "-n", "team-a", "--image", "ghcr.io/acme/app:pr-42", "--ttl", "2h", "--dry-run")
	if code != exitOK {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	want := `apiVersion: vesper.eventide.dev/v1alpha1
kind: PreviewEnvironment
metadata:
  name: pr-42
  namespace: team-a
spec:
  image: ghcr.io/acme/app:pr-42
  ttl: 2h0m0s
`
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}
	if actions := client.Actions(); len(actions) != 0 {
		t.Errorf("requests sent: %v, want none", actions)
	}
}

// TestCreateUsageErrors checks the form of the flags: each mistake exits
// with 2 before any request.
func TestCreateUsageErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"no name", []string{"--image", "a:1"}, "accepts 1 arg(s), received 0"},
		{"invalid name", []string{"PR_42", "--image", "a:1"}, `invalid name "PR_42"`},
		{"no image", []string{"pr-42"}, "--image is required"},
		{"port not a number", []string{"pr-42", "--image", "a:1", "--port", "http"}, `invalid argument "http"`},
		{"port zero", []string{"pr-42", "--image", "a:1", "--port", "0"}, "--port must be a positive number, got 0"},
		{"replicas negative", []string{"pr-42", "--image", "a:1", "--replicas", "-1"},
			"--replicas must be a positive number, got -1"},
		{"ttl not a duration", []string{"pr-42", "--image", "a:1", "--ttl", "2 days"}, `invalid argument "2 days"`},
		{"env without =", []string{"pr-42", "--image", "a:1", "--env", "DEBUG"}, `--env must be KEY=VALUE, got "DEBUG"`},
		{"env without key", []string{"pr-42", "--image", "a:1", "--env", "=1"}, `--env must be KEY=VALUE, got "=1"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient()
			code, stdout, stderr := executeCreate(t, client, append([]string{"create"}, tt.args...)...)
			if code != exitUsage || stdout != "" || !strings.Contains(stderr, tt.wantErr) ||
				!strings.Contains(stderr, "Usage:") {
				t.Errorf("exit code %d, stdout %q, stderr %q; want 2 and %q with the usage", code, stdout, stderr, tt.wantErr)
			}
			if actions := client.Actions(); len(actions) != 0 {
				t.Errorf("requests sent: %v, want none", actions)
			}
		})
	}
}

// TestCreateAPIErrors checks the answers of the API server a user can act on.
func TestCreateAPIErrors(t *testing.T) {
	gk := schema.GroupKind{Group: previewEnvironmentGVR.Group, Kind: "PreviewEnvironment"}
	tests := []struct {
		name     string
		err      error
		wantCode int
		want     string
	}{
		{"invalid values", apierrors.NewInvalid(gk, "pr-42", field.ErrorList{
			field.Invalid(field.NewPath("spec", "ttl"), "30s", "ttl must be between 1m and 168h"),
			field.Invalid(field.NewPath("spec", "image"), "app:latest", "image must not use the latest tag"),
		}), exitUsage, "vesperctl: the API server rejected the preview environment: " +
			"spec.ttl: Invalid value: \"30s\": ttl must be between 1m and 168h; " +
			"spec.image: Invalid value: \"app:latest\": image must not use the latest tag\n"},
		{"already exists", apierrors.NewAlreadyExists(previewEnvironmentGVR.GroupResource(), "pr-42"), exitFailure,
			"vesperctl: preview environment \"pr-42\" already exists in namespace \"team-a\": " +
				"delete it first, or choose another name\n"},
		{"namespace not found", apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "team-a"),
			exitFailure, "vesperctl: namespace \"team-a\" not found: create it, or choose another with --namespace\n"},
		{"CRD not installed", apierrors.NewNotFound(previewEnvironmentGVR.GroupResource(), ""), exitFailure,
			errCRDNotInstalled.Error()},
		{"forbidden", apierrors.NewForbidden(previewEnvironmentGVR.GroupResource(), "pr-42", errors.New("denied")),
			exitFailure, "vesperctl: you are not allowed to create preview environment \"pr-42\" in namespace " +
				"\"team-a\": ask a cluster admin for access (RBAC)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient()
			client.PrependReactor("create", "previewenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, tt.err
			})
			code, stdout, stderr := executeCreate(t, client,
				"create", "pr-42", "-n", "team-a", "--image", "ghcr.io/acme/app:pr-42")
			if code != tt.wantCode || stdout != "" || !strings.Contains(stderr, tt.want) {
				t.Errorf("exit code %d, stdout %q, stderr %q; want %d and %q", code, stdout, stderr, tt.wantCode, tt.want)
			}
			if strings.Contains(stderr, "Usage:") {
				t.Errorf("stderr prints the usage for an error of the API server: %q", stderr)
			}
		})
	}
}
