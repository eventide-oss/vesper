// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Package integration runs the vesperctl binary against a real API server: a
// kind cluster with the PreviewEnvironment CRD generated in config/crd.
//
// Run it with `make test-integration`. It installs the CRD unless one is
// already installed, creates a namespace with a random name and two previews,
// and deletes what it created, even when a test fails. A CRD it did not
// install is never modified or deleted.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

const (
	// contextEnv names the kubeconfig context to test against.
	contextEnv     = "VESPER_IT_CONTEXT"
	defaultContext = "kind-vesper-dev"

	// managedBy labels what the tests create, so a leftover of an
	// interrupted run is recognized and removed.
	managedByLabel = "app.kubernetes.io/managed-by"
	managedBy      = "vesper-integration-tests"

	crdName = "previewenvironments.vesper.eventide.dev"

	// crdFile is the generated CRD (make manifests), relative to this package.
	crdFile = "../../config/crd/bases/vesper.eventide.dev_previewenvironments.yaml"

	// waitTimeout bounds every wait on the cluster.
	waitTimeout = time.Minute
)

var (
	crdGVR       = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	namespaceGVR = schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	previewGVR   = schema.GroupVersionResource{Group: "vesper.eventide.dev", Version: "v1alpha1", Resource: "previewenvironments"}
)

// Set by TestMain.
var (
	kubeContext string
	binary      string
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	kubeContext = os.Getenv(contextEnv)
	if kubeContext == "" {
		kubeContext = defaultContext
	}
	// The tests create and delete a cluster-wide CRD: never run them against
	// a cluster that is not a disposable kind cluster.
	if !strings.HasPrefix(kubeContext, "kind-") {
		fmt.Fprintf(os.Stderr, "refusing to run against context %q: %s must name a kind context (kind-...)\n",
			kubeContext, contextEnv)
		return 1
	}

	dir, err := os.MkdirTemp("", "vesperctl-it-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = os.RemoveAll(dir) }()

	// Build the binary once, as a user would get it.
	binary = filepath.Join(dir, "vesperctl")
	// No shell is involved, and binary is a path in our own temporary directory.
	build := exec.Command("go", "build", "-o", binary, "github.com/eventide-oss/vesper/cmd/vesperctl") //nolint:gosec // G204: see above
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "build vesperctl: %v\n", err)
		return 1
	}
	return m.Run()
}

func TestCLI(t *testing.T) {
	client := newClient(t)

	crdInstalled := prepareCRD(t, client)
	if crdInstalled {
		t.Log("a PreviewEnvironment CRD not created by these tests is installed: " +
			"skipping the not-installed cases and leaving it in place")
	} else {
		// The real 404 of an unknown resource type, which unit tests can only fake.
		t.Run("CRD not installed", func(t *testing.T) {
			for _, args := range [][]string{{"list"}, {"get", "pr-1"}} {
				code, stdout, stderr := vesperctl(t, args...)
				checkResult(t, code, stdout, stderr, 1,
					"vesperctl: the PreviewEnvironment API (previewenvironments.vesper.eventide.dev) "+
						"is not installed in this cluster: install Vesper, or check --context\n")
			}
		})
		installCRD(t, client)
	}

	ns := createNamespace(t, client)
	createPreview(t, client, ns, "pr-42", map[string]any{
		"spec": map[string]any{
			"image": "ghcr.io/eventide/app:pr-42", "port": 8080, "replicas": 1, "ttl": "24h",
			"env": []any{
				map[string]any{"name": "API_TOKEN", "value": "fake-integration-value"},
				map[string]any{"name": "LOG_LEVEL", "value": "debug"},
			},
		},
		"status": map[string]any{
			"phase":     "Ready",
			"namespace": "vesper-pr-42-3f9c2a1b",
			"url":       "https://pr-42.preview.example.com",
			"expiresAt": "2099-01-01T00:00:00Z",
			"conditions": []any{map[string]any{
				"type": "Available", "status": "True", "reason": "DeploymentReady",
				"message": "1/1 replicas available", "lastTransitionTime": "2026-11-05T10:30:00Z",
			}},
		},
	})
	// Every spec field is set, so the API server adds no default.
	createPreview(t, client, ns, "pr-12", map[string]any{
		"spec": map[string]any{"image": "nginx:1.27", "port": 80, "replicas": 2, "ttl": "2h"},
	})

	t.Run("list table", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "list", "-n", ns)
		checkResult(t, code, stdout, stderr, 0, "")
		checkLines(t, stdout,
			`^NAMESPACE\s+NAME\s+PHASE\s+AGE$`,
			`^`+ns+`\s+pr-12\s+<none>\s+\d+s$`,
			`^`+ns+`\s+pr-42\s+Ready\s+\d+s$`,
		)
	})

	t.Run("list json", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "list", "-n", ns, "-o", "json")
		checkResult(t, code, stdout, stderr, 0, "")
		var got struct{ Items []map[string]any }
		decode(t, stdout, &got)
		if len(got.Items) != 2 {
			t.Fatalf("got %d items, want 2:\n%s", len(got.Items), stdout)
		}
		checkSummary(t, got.Items[0], pendingSummary(ns))
		checkSummary(t, got.Items[1], readySummary(ns))
	})

	t.Run("list all namespaces", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "list", "-A", "-o", "json")
		checkResult(t, code, stdout, stderr, 0, "")
		var got struct{ Items []map[string]any }
		decode(t, stdout, &got)
		var names []string
		for _, item := range got.Items {
			if item["namespace"] == ns {
				names = append(names, item["name"].(string))
			}
		}
		if want := []string{"pr-12", "pr-42"}; !reflect.DeepEqual(names, want) {
			t.Errorf("previews of %s = %q, want %q", ns, names, want)
		}
	})

	t.Run("list empty namespace", func(t *testing.T) {
		empty := ns + "-empty" // does not exist: the API server returns an empty list
		code, stdout, stderr := vesperctl(t, "list", "-n", empty)
		checkResult(t, code, stdout, stderr, 0,
			fmt.Sprintf("No preview environments found in namespace %q.\n", empty))
		if stdout != "" {
			t.Errorf("stdout = %q, want it empty", stdout)
		}
	})

	t.Run("get table", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "get", "pr-42", "-n", ns)
		checkResult(t, code, stdout, stderr, 0, "")
		checkLines(t, stdout,
			`^Name:        pr-42$`,
			`^Namespace:   `+ns+`$`,
			`^Runs in:     vesper-pr-42-3f9c2a1b$`,
			`^Created:     \S+Z \(\d+s ago\)$`,
			`^Phase:       Ready$`,
			`^Expires:     2099-01-01T00:00:00Z \(in \d+y\)$`,
			`^  Port:      8080$`,
			`^  Env:       2 variables$`,
			`^  Available\s+True\s+DeploymentReady\s+\S+\s+1/1 replicas available$`,
		)
	})

	t.Run("get json", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "get", "pr-42", "-n", ns, "-o", "json")
		checkResult(t, code, stdout, stderr, 0, "")
		var got map[string]any
		decode(t, stdout, &got)
		checkSummary(t, got, readySummary(ns))
	})

	t.Run("get yaml", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "get", "pr-12", "-n", ns, "-o", "yaml")
		checkResult(t, code, stdout, stderr, 0, "")
		var got map[string]any
		if err := yaml.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("stdout is not valid YAML: %v\n%s", err, stdout)
		}
		checkSummary(t, got, pendingSummary(ns))
	})

	t.Run("get not found", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "get", "pr-404", "-n", ns)
		checkResult(t, code, stdout, stderr, 1,
			fmt.Sprintf("vesperctl: preview environment \"pr-404\" not found in namespace %q\n", ns))
	})

	// The real CRD validates and completes what create sends.
	t.Run("create", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "create", "pr-7", "-n", ns,
			"--image", "ghcr.io/eventide/app:pr-7", "--ttl", "2h", "--env", "LOG_LEVEL=debug")
		checkResult(t, code, stdout, stderr, 0, "")

		code, stdout, stderr = vesperctl(t, "get", "pr-7", "-n", ns, "-o", "json")
		checkResult(t, code, stdout, stderr, 0, "")
		var got map[string]any
		decode(t, stdout, &got)
		// The port and the replicas are the defaults of the API server.
		want := map[string]any{
			"image": "ghcr.io/eventide/app:pr-7", "port": 8080.0, "replicas": 1.0, "ttl": "2h0m0s", "envCount": 1.0,
		}
		if !reflect.DeepEqual(got["spec"], want) {
			t.Errorf("spec = %v, want %v", got["spec"], want)
		}
	})

	// The real API server accepts the ttl extend writes. pr-7 was created
	// with a ttl of 2h.
	t.Run("extend", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "extend", "pr-7", "-n", ns, "--by", "90m")
		if code != 0 || stderr != "" ||
			!strings.HasPrefix(stdout, `preview environment "pr-7" extended by 1h30m0s: ttl 3h30m0s, expires at `) {
			t.Errorf("exit code %d, stdout %q, stderr %q; want 0 and the new ttl", code, stdout, stderr)
		}
		code, stdout, stderr = vesperctl(t, "extend", "pr-7", "-n", ns, "--by", "165h")
		checkResult(t, code, stdout, stderr, 2, "vesperctl: cannot extend \"pr-7\" by 165h0m0s: its ttl would be "+
			"168h30m0s, over the maximum of 168h0m0s; it can be extended by 164h30m0s at most\n")

		code, stdout, stderr = vesperctl(t, "get", "pr-7", "-n", ns, "-o", "json")
		checkResult(t, code, stdout, stderr, 0, "")
		var got struct{ Spec struct{ TTL string } }
		decode(t, stdout, &got)
		if got.Spec.TTL != "3h30m0s" {
			t.Errorf("spec.ttl = %q, want 3h30m0s", got.Spec.TTL)
		}
	})

	// No operator runs here, so no finalizer delays the deletion.
	t.Run("delete", func(t *testing.T) {
		question := fmt.Sprintf("Delete preview environment \"pr-7\" in namespace %q, "+
			"with everything in its preview namespace? [y/N] ", ns)
		code, stdout, stderr := vesperctlWithInput(t, "n\n", "delete", "pr-7", "-n", ns)
		checkResult(t, code, stdout, stderr, 1, question+"vesperctl: not deleted\n")
		code, stdout, stderr = vesperctl(t, "get", "pr-7", "-n", ns)
		checkResult(t, code, stdout, stderr, 0, "")

		code, stdout, stderr = vesperctlWithInput(t, "y\n", "delete", "pr-7", "-n", ns)
		checkResult(t, code, stdout, stderr, 0, question)
		code, stdout, stderr = vesperctl(t, "delete", "pr-7", "-n", ns, "--yes")
		checkResult(t, code, stdout, stderr, 1,
			fmt.Sprintf("vesperctl: preview environment \"pr-7\" not found in namespace %q\n", ns))
	})

	t.Run("create existing", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "create", "pr-42", "-n", ns, "--image", "ghcr.io/eventide/app:pr-42")
		checkResult(t, code, stdout, stderr, 1, fmt.Sprintf("vesperctl: preview environment \"pr-42\" "+
			"already exists in namespace %q: delete it first, or choose another name\n", ns))
	})

	t.Run("create rejected by the API server", func(t *testing.T) {
		code, stdout, stderr := vesperctl(t, "create", "pr-8", "-n", ns,
			"--image", "ghcr.io/eventide/app:latest", "--ttl", "30s")
		if code != 2 || stdout != "" ||
			!strings.HasPrefix(stderr, "vesperctl: the API server rejected the preview environment: ") ||
			!strings.Contains(stderr, "spec.ttl: Invalid value: \"30s\": ttl must be a duration") ||
			!strings.Contains(stderr, "image must be pinned") {
			t.Errorf("exit code %d, stdout %q, stderr %q; want 2 with the ttl and image errors", code, stdout, stderr)
		}
	})
}

// readySummary and pendingSummary are the expected -o json documents,
// without createdAt, which the API server sets.
func readySummary(ns string) map[string]any {
	return map[string]any{
		"name": "pr-42", "namespace": ns, "runNamespace": "vesper-pr-42-3f9c2a1b", "phase": "Ready",
		"url": "https://pr-42.preview.example.com", "expiresAt": "2099-01-01T00:00:00Z",
		"spec": map[string]any{
			"image": "ghcr.io/eventide/app:pr-42", "port": 8080.0, "replicas": 1.0, "ttl": "24h", "envCount": 2.0,
		},
		"conditions": []any{map[string]any{
			"type": "Available", "status": "True", "reason": "DeploymentReady",
			"message": "1/1 replicas available", "lastTransitionTime": "2026-11-05T10:30:00Z",
		}},
	}
}

func pendingSummary(ns string) map[string]any {
	return map[string]any{
		"name": "pr-12", "namespace": ns,
		"spec": map[string]any{"image": "nginx:1.27", "port": 80.0, "replicas": 2.0, "ttl": "2h", "envCount": 0.0},
	}
}

// checkSummary compares a decoded summary with want, after checking that
// createdAt is a recent RFC 3339 time.
func checkSummary(t *testing.T, got, want map[string]any) {
	t.Helper()
	raw, _ := got["createdAt"].(string)
	created, err := time.Parse(time.RFC3339, raw)
	if err != nil || time.Since(created) > 10*time.Minute {
		t.Errorf("createdAt = %q, want a recent RFC 3339 time", raw)
	}
	got = maps.Clone(got)
	delete(got, "createdAt")
	if !reflect.DeepEqual(got, want) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Errorf("summary =\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

// vesperctl runs the binary against the test context and returns its exit
// code, stdout and stderr.
func vesperctl(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return vesperctlWithInput(t, "", args...)
}

// vesperctlWithInput runs vesperctl with stdin as its standard input.
func vesperctlWithInput(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), waitTimeout)
	defer cancel()

	var stdout, stderr bytes.Buffer
	// No shell is involved: binary was built by TestMain, args come from the
	// tests, and kubeContext was checked to be a kind context.
	cmd := exec.CommandContext(ctx, binary, append([]string{"--context", kubeContext}, args...)...) //nolint:gosec // G204: see above
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &stdout, &stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return 0, stdout.String(), stderr.String()
	case errors.As(err, &exitErr):
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	default:
		t.Fatalf("run vesperctl %v: %v", args, err)
		return 0, "", ""
	}
}

// checkResult checks the exit code and the exact stderr, that stdout is empty
// on failure, and that no output contains spec.env data.
func checkResult(t *testing.T, code int, stdout, stderr string, wantCode int, wantStderr string) {
	t.Helper()
	if code != wantCode {
		t.Errorf("exit code = %d, want %d (stderr: %q)", code, wantCode, stderr)
	}
	if stderr != wantStderr {
		t.Errorf("stderr = %q, want %q", stderr, wantStderr)
	}
	if wantCode != 0 && stdout != "" {
		t.Errorf("stdout = %q, want it empty", stdout)
	}
	for _, out := range []string{stdout, stderr} {
		if strings.Contains(out, "fake-integration-value") || strings.Contains(out, "API_TOKEN") {
			t.Errorf("output contains spec.env data: %q", out)
		}
	}
}

// checkLines checks that every pattern matches a line of out, in order.
func checkLines(t *testing.T, out string, patterns ...string) {
	t.Helper()
	lines := strings.Split(out, "\n")
	i := 0
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		for i < len(lines) && !re.MatchString(lines[i]) {
			i++
		}
		if i == len(lines) {
			t.Errorf("no line matches %q after the previous pattern in:\n%s", p, out)
			return
		}
		i++
	}
}

func decode(t *testing.T, out string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(out), v); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out)
	}
}

func newClient(t *testing.T) dynamic.Interface {
	t.Helper()
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(),
		&clientcmd.ConfigOverrides{CurrentContext: kubeContext},
	).ClientConfig()
	if err != nil {
		t.Fatalf("load kubeconfig context %q (create the cluster with `make kind-up`): %v", kubeContext, err)
	}
	cfg.Timeout = 30 * time.Second
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return client
}

// cleanupContext is for t.Cleanup functions: t.Context() is already canceled
// when they run.
func cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), waitTimeout)
}

// prepareCRD removes a CRD left by an interrupted run, and reports whether a
// CRD the tests did not install is installed. The tests recognize their own
// CRD by the managed-by label they add when they install it.
func prepareCRD(t *testing.T, client dynamic.Interface) bool {
	t.Helper()
	crd, err := client.Resource(crdGVR).Get(t.Context(), crdName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		return false
	case err != nil:
		t.Fatalf("get CRD: %v", err)
	case crd.GetLabels()[managedByLabel] != managedBy:
		return true
	}
	t.Log("removing the CRD left by a previous run")
	deleteCRD(t, client, t.Context())
	return false
}

// installCRD creates the generated CRD with the managed-by label, waits until
// the API server serves it, and registers its deletion.
func installCRD(t *testing.T, client dynamic.Interface) {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(crdFile))
	if err != nil {
		t.Fatalf("read CRD: %v", err)
	}
	crd := &unstructured.Unstructured{}
	if err := yaml.Unmarshal(raw, &crd.Object); err != nil {
		t.Fatalf("parse CRD: %v", err)
	}
	// Added here, not in the file: a CRD installed with `make install` never
	// carries it, so the tests never delete it.
	labels := crd.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[managedByLabel] = managedBy
	crd.SetLabels(labels)
	if _, err := client.Resource(crdGVR).Create(t.Context(), crd, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()
		deleteCRD(t, client, ctx)
	})

	// Registration is asynchronous: wait for the Established condition.
	waitFor(t, t.Context(), "the CRD to be established", func(ctx context.Context) (bool, error) {
		crd, err := client.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		conditions, _, _ := unstructured.NestedSlice(crd.Object, "status", "conditions")
		for _, c := range conditions {
			if m, ok := c.(map[string]any); ok && m["type"] == "Established" && m["status"] == "True" {
				return true, nil
			}
		}
		return false, nil
	})
}

// deleteCRD deletes the CRD the tests installed, with every
// PreviewEnvironment, and waits until it is gone so that the next run starts
// clean.
func deleteCRD(t *testing.T, client dynamic.Interface, ctx context.Context) {
	t.Helper()
	err := client.Resource(crdGVR).Delete(ctx, crdName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		t.Errorf("delete CRD: %v", err)
		return
	}
	waitFor(t, ctx, "the CRD to be deleted", func(ctx context.Context) (bool, error) {
		_, err := client.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

// createNamespace creates a namespace with a random name and registers its
// deletion, which deletes the previews in it.
func createNamespace(t *testing.T, client dynamic.Interface) string {
	t.Helper()
	ns := &unstructured.Unstructured{}
	ns.SetAPIVersion("v1")
	ns.SetKind("Namespace")
	ns.SetGenerateName("vesper-it-") // the API server adds a random suffix
	ns.SetLabels(map[string]string{managedByLabel: managedBy})

	created, err := client.Resource(namespaceGVR).Create(t.Context(), ns, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	name := created.GetName()
	t.Cleanup(func() {
		ctx, cancel := cleanupContext()
		defer cancel()
		err := client.Resource(namespaceGVR).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete namespace %s: %v", name, err)
		}
	})
	return name
}

// createPreview creates a preview, then writes its status, if fields has one,
// as the operator would. The status subresource ignores a status sent with
// the object, so it takes a second request.
func createPreview(t *testing.T, client dynamic.Interface, ns, name string, fields map[string]any) {
	t.Helper()
	fields = maps.Clone(fields)
	status, hasStatus := fields["status"]
	delete(fields, "status")

	obj := &unstructured.Unstructured{Object: fields}
	obj.SetAPIVersion("vesper.eventide.dev/v1alpha1")
	obj.SetKind("PreviewEnvironment")
	obj.SetName(name)
	previews := client.Resource(previewGVR).Namespace(ns)
	created, err := previews.Create(t.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create preview %s/%s: %v", ns, name, err)
	}
	if !hasStatus {
		return
	}
	// The created object carries the resourceVersion the update must send.
	created.Object["status"] = status
	if _, err := previews.UpdateStatus(t.Context(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update status of preview %s/%s: %v", ns, name, err)
	}
}

// waitFor polls done until it returns true, an error, or waitTimeout elapses.
func waitFor(t *testing.T, ctx context.Context, what string, done func(context.Context) (bool, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	for {
		ok, err := done(ctx)
		if err != nil {
			t.Fatalf("wait for %s: %v", what, err)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", what)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
