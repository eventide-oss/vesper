// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
	"github.com/eventide-oss/vesper/internal/controller"
)

// vesperctlEnv names the vesperctl binary built by hack/test-e2e.sh.
const vesperctlEnv = "VESPER_E2E_VESPERCTL"

// cliPreview is the part of `vesperctl get -o json` these tests read.
type cliPreview struct {
	Phase        string     `json:"phase"`
	RunNamespace string     `json:"runNamespace"`
	CreatedAt    *time.Time `json:"createdAt"`
	ExpiresAt    *time.Time `json:"expiresAt"`
	Spec         struct {
		TTL string `json:"ttl"`
	} `json:"spec"`
}

// vesperctl runs the vesperctl binary against the cluster under test and
// returns its exit code and output.
func vesperctl(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	binary := os.Getenv(vesperctlEnv)
	if binary == "" {
		t.Fatalf("%s is not set: run the e2e tests with `make test-e2e`", vesperctlEnv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var out, errOut bytes.Buffer
	// No shell is involved: the binary was built by hack/test-e2e.sh, and the
	// arguments come from these tests.
	cmd := exec.CommandContext(ctx, binary, append([]string{"--context", os.Getenv(contextEnv)}, args...)...) //nolint:gosec // G204: see above
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("run vesperctl %v: %v", args, err)
	}
	return code, out.String(), errOut.String()
}

// mustVesperctl runs vesperctl and fails the test unless it exits with 0.
func mustVesperctl(t *testing.T, args ...string) string {
	t.Helper()
	code, stdout, stderr := vesperctl(t, args...)
	if code != 0 {
		t.Fatalf("vesperctl %v: exit code %d, stderr %q", args, code, stderr)
	}
	return stdout
}

// getPreview reads a preview with `vesperctl get -o json`.
func getPreview(t *testing.T, namespace, name string) (cliPreview, error) {
	t.Helper()
	code, stdout, stderr := vesperctl(t, "get", name, "-n", namespace, "-o", "json")
	if code != 0 {
		return cliPreview{}, fmt.Errorf("vesperctl get: exit code %d, stderr %q", code, stderr)
	}
	var p cliPreview
	if err := json.Unmarshal([]byte(stdout), &p); err != nil {
		return cliPreview{}, fmt.Errorf("decode vesperctl get: %w", err)
	}
	return p, nil
}

// waitForGone waits until vesperctl no longer finds the preview, and its
// preview namespace is gone.
func waitForGone(t *testing.T, c client.Client, namespace, name string, timeout time.Duration) {
	t.Helper()
	notFound := fmt.Sprintf("vesperctl: preview environment %q not found in namespace %q\n", name, namespace)
	previewNamespace := controller.NamespaceName(namespace, name)
	eventually(t, timeout, func() error {
		code, _, stderr := vesperctl(t, "get", name, "-n", namespace)
		if code != 1 || stderr != notFound {
			return fmt.Errorf("vesperctl get: exit code %d, stderr %q; want 1 and not found", code, stderr)
		}
		err := c.Get(context.Background(), types.NamespacedName{Name: previewNamespace}, &corev1.Namespace{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get preview namespace %s: %v, want NotFound", previewNamespace, err)
		}
		return nil
	})
}

// newTestNamespace creates a namespace for a test, deleted at its end with
// the preview namespace of name.
func newTestNamespace(t *testing.T, c client.Client, name string) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "vesper-e2e-"}}
	if err := c.Create(context.Background(), ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	t.Cleanup(func() { cleanup(t, c, ns.Name, controller.NamespaceName(ns.Name, name)) })
	return ns.Name
}

// TestCLILifecycle runs the life of a preview as a user would, with
// vesperctl only: create, follow until Ready, extend, delete.
func TestCLILifecycle(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	const name = "pr-cli"
	ns := newTestNamespace(t, c, name)

	mustVesperctl(t, "create", name, "-n", ns, "--image", "nginxinc/nginx-unprivileged:1.27", "--ttl", "30m")

	var ready cliPreview
	eventually(t, readyTimeout, func() error {
		p, err := getPreview(t, ns, name)
		if err != nil {
			return err
		}
		if want := controller.NamespaceName(ns, name); p.Phase != "Ready" || p.RunNamespace != want {
			return fmt.Errorf("phase %q, runNamespace %q; want Ready and %q", p.Phase, p.RunNamespace, want)
		}
		ready = p
		return nil
	})

	// The operator moves the expiry with the ttl: 30m + 1h from the creation.
	out := mustVesperctl(t, "extend", name, "-n", ns, "--by", "1h")
	if !strings.Contains(out, "ttl 1h30m0s") {
		t.Errorf("vesperctl extend: %q, want the ttl 1h30m0s", out)
	}
	if ready.CreatedAt == nil {
		t.Fatal("vesperctl get: no createdAt")
	}
	wantExpiry := ready.CreatedAt.Add(90 * time.Minute)
	eventually(t, readyTimeout, func() error {
		p, err := getPreview(t, ns, name)
		if err != nil {
			return err
		}
		if p.ExpiresAt == nil || !p.ExpiresAt.Equal(wantExpiry) {
			return fmt.Errorf("expiresAt %v, want %s", p.ExpiresAt, wantExpiry)
		}
		return nil
	})

	out = mustVesperctl(t, "delete", name, "-n", ns, "--yes")
	if !strings.Contains(out, "is being deleted") {
		t.Errorf("vesperctl delete: %q, want the deletion", out)
	}
	waitForGone(t, c, ns, name, readyTimeout)
}

// TestCLIExpiry creates a preview with the shortest ttl, one minute, and
// waits until the operator deletes it and its namespace. It runs in parallel
// with TestCLILifecycle, to shorten the wait.
func TestCLIExpiry(t *testing.T) {
	t.Parallel()
	c := newClient(t)
	const name = "pr-expire"
	ns := newTestNamespace(t, c, name)

	mustVesperctl(t, "create", name, "-n", ns, "--image", "nginxinc/nginx-unprivileged:1.27", "--ttl", "1m")
	waitForGone(t, c, ns, name, time.Minute+readyTimeout)

	// The preview is gone; the event of its expiry stays in its namespace.
	// Events are written in the background, so it may come a little later.
	eventually(t, 30*time.Second, func() error {
		var list eventsv1.EventList
		if err := c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
			return err
		}
		for _, e := range list.Items {
			if e.Regarding.Name == name && e.Reason == vesperv1alpha1.ReasonExpired {
				return nil
			}
		}
		return fmt.Errorf("no %s event for the preview", vesperv1alpha1.ReasonExpired)
	})
}
