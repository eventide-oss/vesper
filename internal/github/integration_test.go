// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
	"github.com/eventide-oss/vesper/internal/github/githubtest"
)

// TestIntegration runs the integration as vesper-github runs it, against a
// real API server (envtest) and a fake GitHub: signed webhooks turn pull
// requests into PreviewEnvironment objects, and back. The operator does not
// run: the previews are only created and deleted.
func TestIntegration(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("KUBEBUILDER_ASSETS is not set: run `make test` to start envtest")
		}
		t.Skip("KUBEBUILDER_ASSETS is not set: run `make test` to start envtest")
	}
	ctrl.SetLogger(zap.New(zap.WriteTo(io.Discard)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, vesperv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := mgr.GetClient()

	// The cluster side: the namespace of the previews, and the source.
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "previews-acme"}}); err != nil {
		t.Fatal(err)
	}
	source := testSource(vesperv1alpha1.TriggerAll)
	if err := c.Create(ctx, source); err != nil {
		t.Fatal(err)
	}

	// The GitHub side: the App installed on acme/app.
	gh := githubtest.New(t)
	gh.AddRepo("acme/app", 7)
	app, err := NewApp(gh.AppID, gh.KeyPEM, gh.URL, "vesper-github/test")
	if err != nil {
		t.Fatal(err)
	}

	// vesper-github: a long resync, so that the webhooks do the work.
	queue := NewQueue(logr.Discard())
	if err := SetupController(mgr, &Reconciler{Client: c, API: NewClient(app), Enqueue: queue.Enqueue}, queue); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Add(&Resync{Client: c, Queue: queue, Interval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	receiver, err := NewReceiver(func() ([]byte, error) { return []byte(testSecret), nil }, logr.Discard(),
		prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	receiver.OnEvent(queue.Dispatch)
	hooks := httptest.NewServer(receiver)
	t.Cleanup(hooks.Close)
	go func() { _ = mgr.Start(ctx) }()

	send := func(event, body string) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, hooks.URL, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(headerEvent, event)
		req.Header.Set(headerSignature, sign(testSecret, body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("%s delivery: %d, want 202", event, resp.StatusCode)
		}
	}
	preview := func() *vesperv1alpha1.PreviewEnvironment {
		p := &vesperv1alpha1.PreviewEnvironment{}
		err := c.Get(ctx, types.NamespacedName{Namespace: "previews-acme", Name: "acme-app-pr-43"}, p)
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	waitFor := func(what string, ok func(*vesperv1alpha1.PreviewEnvironment) bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ok(preview()) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; preview = %+v", what, preview())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	const pullRequest = `{"action":"%s","number":43,"repository":{"full_name":"acme/app"}}`

	// A pull request opens; CI has not built its image: no preview.
	gh.SetPullRequest("acme/app", githubtest.PullRequest{Number: 43, HeadSHA: sha1})
	send("pull_request", strings.Replace(pullRequest, "%s", "opened", 1))
	time.Sleep(time.Second)
	if p := preview(); p != nil {
		t.Fatalf("preview %+v before the image is built", p)
	}

	// CI succeeds: the workflow_run creates the preview.
	gh.AddRun("acme/app", githubtest.Run{File: "preview.yml", HeadSHA: sha1, Conclusion: "success"})
	send("workflow_run", `{"action":"completed","repository":{"full_name":"acme/app"},"workflow_run":{"pull_requests":[{"number":43}]}}`)
	waitFor("the preview", func(p *vesperv1alpha1.PreviewEnvironment) bool {
		return p != nil && p.Spec.Image == "ghcr.io/acme/app:pr-43-"+sha1
	})

	// A new commit, built: the preview moves to it.
	gh.SetPullRequest("acme/app", githubtest.PullRequest{Number: 43, HeadSHA: sha2})
	gh.AddRun("acme/app", githubtest.Run{File: "preview.yml", HeadSHA: sha2, Conclusion: "success"})
	send("pull_request", strings.Replace(pullRequest, "%s", "synchronize", 1))
	waitFor("the new image", func(p *vesperv1alpha1.PreviewEnvironment) bool {
		return p != nil && p.Spec.Image == "ghcr.io/acme/app:pr-43-"+sha2
	})

	// The pull request closes: the preview goes.
	gh.SetPullRequest("acme/app", githubtest.PullRequest{Number: 43, HeadSHA: sha2, State: "closed"})
	send("pull_request", strings.Replace(pullRequest, "%s", "closed", 1))
	waitFor("the deletion", func(p *vesperv1alpha1.PreviewEnvironment) bool { return p == nil })
}
