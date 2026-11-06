package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// The envtest tests run against a real API server and etcd, started once for
// the package by TestMain, with the controller running in a real manager.
// Nothing else runs: no Deployment controller (no pods), no namespace
// controller (a deleted namespace stays Terminating), no garbage collector.
// Each test works in a namespace of its own.

const (
	eventuallyTimeout  = 10 * time.Second
	eventuallyInterval = 100 * time.Millisecond
)

var (
	// envClient talks to the envtest API server directly, without a cache, so
	// that a test reads what the server holds. It is nil when envtest is not
	// running.
	envClient client.Client
	// envtestSkipped says why envtest is not running.
	envtestSkipped string
)

func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		envtestSkipped = "KUBEBUILDER_ASSETS is not set: run `make test` to start envtest"
		return m.Run()
	}

	stop, err := startEnvtest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		return 1
	}
	code := m.Run()
	if err := stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
		return 1
	}
	return code
}

// startEnvtest starts the API server with the generated CRD and a manager
// running the controller, and returns a function that stops both.
func startEnvtest() (stop func() error, err error) {
	// The manager logs every reconciliation; the test failures say enough.
	ctrl.SetLogger(zap.New(zap.WriteTo(io.Discard)))

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		return nil, err
	}
	stopEnv := func(err error) (func() error, error) {
		return nil, errors.Join(err, env.Stop())
	}

	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, vesperv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			return stopEnv(err)
		}
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"}, // no metrics server
	})
	if err != nil {
		return stopEnv(fmt.Errorf("create manager: %w", err))
	}
	r := &PreviewEnvironmentReconciler{Client: mgr.GetClient(), Scheme: scheme}
	if err := r.SetupWithManager(mgr); err != nil {
		return stopEnv(fmt.Errorf("set up controller: %w", err))
	}
	if envClient, err = client.New(cfg, client.Options{Scheme: scheme}); err != nil {
		return stopEnv(fmt.Errorf("create client: %w", err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()

	return func() error {
		cancel()
		return errors.Join(<-done, env.Stop())
	}, nil
}

// requireEnvtest skips the test when envtest is not running. In CI it fails
// instead, so that the tests never pass by being skipped.
func requireEnvtest(t *testing.T) {
	t.Helper()
	if envClient != nil {
		return
	}
	if os.Getenv("CI") != "" {
		t.Fatal(envtestSkipped)
	}
	t.Skip(envtestSkipped)
}

// eventually calls check until it returns nil, and fails the test with the
// last error if that does not happen within eventuallyTimeout. The controller
// works asynchronously: a test waits for the state it expects, never for a
// fixed time.
func eventually(t *testing.T, check func() error) {
	t.Helper()
	deadline := time.Now().Add(eventuallyTimeout)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not true after %s: %v", eventuallyTimeout, err)
		}
		time.Sleep(eventuallyInterval)
	}
}

// newUserNamespace creates a namespace with a unique name, the namespace of
// the previews of one test. Namespaces cannot be deleted in envtest, so tests
// never share one.
func newUserNamespace(t *testing.T) string {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "team-"}}
	if err := envClient.Create(context.Background(), ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	return ns.Name
}
