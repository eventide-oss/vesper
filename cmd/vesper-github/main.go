// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

// vesper-github is the GitHub integration of Vesper (ADR-0007): the only
// part of Vesper that faces the Internet. It receives the webhooks of the
// GitHub App, and turns pull requests into PreviewEnvironment objects; the
// operator does the rest.
package main

import (
	"crypto/tls"
	"flag"
	"os"
	"time"

	// Kubernetes client auth plugins, for out-of-cluster runs.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
	"github.com/eventide-oss/vesper/internal/github"
)

// version is set at build time with -ldflags "-X main.version=<value>".
var version = "dev"

func main() {
	var webhookAddr, secretFile, metricsAddr, probeAddr string
	var appIDFile, privateKeyFile, apiURL string
	var resyncInterval time.Duration
	var secureMetrics bool
	flag.StringVar(&webhookAddr, "webhook-bind-address", ":8080",
		"The address the webhook endpoint binds to, in plain HTTP: the ingress controller terminates TLS.")
	flag.StringVar(&secretFile, "webhook-secret-file", "/etc/vesper-github/webhook-secret",
		"The file that holds the webhook secret of the GitHub App, from a mounted Secret.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8443",
		"The address the metrics endpoint binds to, or 0 to disable it.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"Serve metrics over HTTPS, for authenticated and authorized clients only.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&appIDFile, "app-id-file", "/etc/vesper-github/app-id",
		"The file that holds the ID of the GitHub App, from a mounted Secret.")
	flag.StringVar(&privateKeyFile, "private-key-file", "/etc/vesper-github/private-key",
		"The file that holds the private key of the GitHub App, in PEM, from a mounted Secret.")
	flag.StringVar(&apiURL, "github-api-url", "",
		"The URL of the GitHub API; empty for https://api.github.com.")
	flag.DurationVar(&resyncInterval, "resync-interval", 10*time.Minute,
		"How often the open pull requests of each allowed repository are read again, to catch lost webhooks.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	log := ctrl.Log.WithName("vesper-github")
	log.Info("Starting vesper-github", "version", version)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vesperv1alpha1.AddToScheme(scheme))

	// HTTP/2 stays off on the metrics endpoint, as on the operator: its
	// Rapid Reset and Stream Cancellation flaws (GHSA-qppj-fm5r-hxr3,
	// GHSA-4374-p667-p6c8).
	metricsOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       []func(*tls.Config){func(c *tls.Config) { c.NextProtos = []string{"http/1.1"} }},
	}
	if secureMetrics {
		metricsOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsOptions,
		HealthProbeBindAddress: probeAddr,
	})
	if err != nil {
		log.Error(err, "Failed to create the manager")
		os.Exit(1)
	}

	app, err := newApp(appIDFile, privateKeyFile, apiURL)
	if err != nil {
		log.Error(err, "Failed to read the GitHub App")
		os.Exit(1)
	}
	if resyncInterval < time.Minute {
		log.Error(nil, "--resync-interval must be at least 1m", "resyncInterval", resyncInterval)
		os.Exit(1)
	}
	queue := github.NewQueue(log.WithName("queue"))
	reconciler := &github.Reconciler{Client: mgr.GetClient(), API: github.NewClient(app), Enqueue: queue.Enqueue}
	if err := github.SetupController(mgr, reconciler, queue); err != nil {
		log.Error(err, "Failed to set up the controller")
		os.Exit(1)
	}
	if err := mgr.Add(&github.Resync{Client: mgr.GetClient(), Queue: queue, Interval: resyncInterval}); err != nil {
		log.Error(err, "Failed to add the resync")
		os.Exit(1)
	}

	receiver, err := github.NewReceiver(github.SecretFile(secretFile), log.WithName("webhook"), metrics.Registry)
	if err != nil {
		log.Error(err, "Failed to create the webhook receiver")
		os.Exit(1)
	}
	receiver.OnEvent(queue.Dispatch)
	if err := mgr.Add(github.NewServer(webhookAddr, receiver)); err != nil {
		log.Error(err, "Failed to add the webhook server")
		os.Exit(1)
	}
	// Ready once the secret is readable: a pod without it would refuse every
	// delivery.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		log.Error(err, "Failed to set up the health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("webhook-secret", github.SecretCheck(github.SecretFile(secretFile))); err != nil {
		log.Error(err, "Failed to set up the ready check")
		os.Exit(1)
	}

	log.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Error(err, "Failed to run the manager")
		os.Exit(1)
	}
}

// newApp reads the ID and the private key of the GitHub App from the files
// of its mounted Secret. Errors name the files, never their content.
func newApp(idFile, keyFile, apiURL string) (*github.App, error) {
	idBytes, err := os.ReadFile(idFile) //nolint:gosec // the path is a flag set by the administrator
	if err != nil {
		return nil, err
	}
	id, err := github.ParseAppID(string(idBytes))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(keyFile) //nolint:gosec // the path is a flag set by the administrator
	if err != nil {
		return nil, err
	}
	return github.NewApp(id, key, apiURL, "vesper-github/"+version)
}
