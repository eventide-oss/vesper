package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// fakeFactory is a clientFactory for tests. It returns client, and resolves
// the namespace like kubeFactory: the --namespace flag, else "default".
type fakeFactory struct {
	opts   *globalOptions
	client dynamic.Interface
	err    error
}

func (f *fakeFactory) DynamicClient() (dynamic.Interface, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

func (f *fakeFactory) Namespace() (string, error) {
	if f.opts.Namespace != "" {
		return f.opts.Namespace, nil
	}
	return "default", nil
}

// newFakeClient returns a fake dynamic client that knows the
// PreviewEnvironment resource and holds objs.
func newFakeClient(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{
		previewEnvironmentGVR: "PreviewEnvironmentList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objs...)
}

// newPreview returns a PreviewEnvironment created age ago. An empty phase
// leaves the status unset.
func newPreview(namespace, name, phase string, age time.Duration) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion("vesper.eventide.dev/v1alpha1")
	obj.SetKind("PreviewEnvironment")
	obj.SetNamespace(namespace)
	obj.SetName(name)
	obj.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-age)))
	if phase != "" {
		obj.Object["status"] = map[string]any{"phase": phase}
	}
	return obj
}

// executeWith runs vesperctl with args against factory, and returns the exit
// code, stdout and stderr.
func executeWith(t *testing.T, args []string, newFactory func(*globalOptions) clientFactory) (int, string, string) {
	t.Helper()

	var stdout, stderr bytes.Buffer
	opts := newGlobalOptions(emptyEnv)
	root := newRootCmd(&stdout, &stderr, opts, newFactory(opts))
	code := execute(root, args, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestList(t *testing.T) {
	previews := []runtime.Object{
		newPreview("team-b", "pr-7", "Ready", 3*time.Hour),
		newPreview("team-a", "pr-42", "Ready", 90*time.Minute),
		newPreview("team-a", "pr-12", "", 5*time.Minute),
	}

	tests := []struct {
		name       string
		args       []string
		objs       []runtime.Object
		wantStdout []string // lines expected on stdout, in order
		wantStderr string
	}{
		{
			name:       "empty namespace",
			args:       []string{"list"},
			wantStderr: "No preview environments found in namespace \"default\".\n",
		},
		{
			name:       "empty cluster with --all-namespaces",
			args:       []string{"list", "-A"},
			wantStderr: "No preview environments found.\n",
		},
		{
			name: "one item",
			args: []string{"list", "-n", "team-b"},
			objs: previews,
			wantStdout: []string{
				"NAMESPACE   NAME   PHASE   AGE",
				"team-b      pr-7   Ready   3h",
			},
		},
		{
			name: "many items, sorted by name, missing phase",
			args: []string{"list", "--namespace", "team-a"},
			objs: previews,
			wantStdout: []string{
				"NAMESPACE   NAME    PHASE    AGE",
				"team-a      pr-12   <none>   5m",
				"team-a      pr-42   Ready    90m",
			},
		},
		{
			name: "all namespaces, sorted by namespace then name",
			args: []string{"list", "--all-namespaces", "-n", "ignored"},
			objs: previews,
			wantStdout: []string{
				"NAMESPACE   NAME    PHASE    AGE",
				"team-a      pr-12   <none>   5m",
				"team-a      pr-42   Ready    90m",
				"team-b      pr-7    Ready    3h",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newFakeClient(tt.objs...)

			code, stdout, stderr := executeWith(t, tt.args, func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: client}
			})

			if code != exitOK {
				t.Fatalf("exit code = %d, want %d (stderr: %q)", code, exitOK, stderr)
			}
			wantStdout := ""
			if len(tt.wantStdout) > 0 {
				wantStdout = strings.Join(tt.wantStdout, "\n") + "\n"
			}
			if stdout != wantStdout {
				t.Errorf("stdout =\n%s\nwant\n%s", stdout, wantStdout)
			}
			if stderr != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr, tt.wantStderr)
			}
		})
	}
}

func TestListErrors(t *testing.T) {
	// missingCRD makes the fake client answer like an API server without the
	// PreviewEnvironment CRD.
	missingCRD := func() dynamic.Interface {
		client := newFakeClient()
		client.PrependReactor("list", "previewenvironments",
			func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewNotFound(previewEnvironmentGVR.GroupResource(), "")
			})
		return client
	}

	tests := []struct {
		name       string
		args       []string
		factory    func(*globalOptions) clientFactory
		wantCode   int
		wantStderr string
	}{
		{
			name: "missing CRD",
			args: []string{"list"},
			factory: func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: missingCRD()}
			},
			wantCode: exitFailure,
			wantStderr: "vesperctl: the PreviewEnvironment API (previewenvironments.vesper.eventide.dev) " +
				"is not installed in this cluster: install Vesper, or check --context\n",
		},
		{
			name: "client error",
			args: []string{"list"},
			factory: func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, err: errors.New("no kubeconfig found")}
			},
			wantCode:   exitFailure,
			wantStderr: "vesperctl: no kubeconfig found\n",
		},
		{
			name: "list rejects arguments",
			args: []string{"list", "extra"},
			factory: func(opts *globalOptions) clientFactory {
				return &fakeFactory{opts: opts, client: newFakeClient()}
			},
			wantCode:   exitUsage,
			wantStderr: `unknown command "extra" for "vesperctl list"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := executeWith(t, tt.args, tt.factory)

			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want it empty", stdout)
			}
			if !strings.Contains(stderr, tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tt.wantStderr)
			}
			if strings.Contains(stderr, "goroutine ") {
				t.Errorf("stderr contains a stack trace: %q", stderr)
			}
		})
	}
}

func TestListStdoutWriteError(t *testing.T) {
	client := newFakeClient(newPreview("default", "pr-1", "Ready", time.Minute))
	opts := newGlobalOptions(emptyEnv)
	root := newRootCmd(failingWriter{}, io.Discard, opts, &fakeFactory{opts: opts, client: client})

	if code := execute(root, []string{"list"}, io.Discard); code != exitFailure {
		t.Errorf("exit code = %d, want %d", code, exitFailure)
	}
}
