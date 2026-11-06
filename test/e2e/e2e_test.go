//go:build e2e

// Package e2e checks the operator as it runs in production: built as an
// image and deployed in a cluster. hack/test-e2e.sh creates the kind cluster,
// deploys the operator and runs these tests; run it with `make test-e2e`.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
	"github.com/eventide-oss/vesper/internal/controller"
)

const (
	// contextEnv names the kubeconfig context of the cluster under test.
	contextEnv = "VESPER_E2E_CONTEXT"

	// readyTimeout bounds the wait for a preview: the node pulls its image.
	readyTimeout = 3 * time.Minute
	pollInterval = time.Second
)

func newClient(t *testing.T) client.Client {
	t.Helper()
	kubeContext := os.Getenv(contextEnv)
	if kubeContext == "" {
		t.Fatalf("%s is not set: run the e2e tests with `make test-e2e`", contextEnv)
	}
	cfg, err := config.GetConfigWithContext(kubeContext)
	if err != nil {
		t.Fatalf("load kubeconfig context %s: %v", kubeContext, err)
	}
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, vesperv1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	return c
}

// eventually calls check until it returns nil, and fails the test with the
// last error after timeout.
func eventually(t *testing.T, timeout time.Duration, check func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not true after %s: %v", timeout, err)
		}
		time.Sleep(pollInterval)
	}
}

// TestPreviewBecomesReady creates a preview of an unprivileged nginx image and
// waits until the operator, running in the cluster, has deployed it and its
// pod is ready.
func TestPreviewBecomesReady(t *testing.T) {
	ctx := context.Background()
	c := newClient(t)

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "vesper-e2e-"}}
	if err := c.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	preview := &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "pr-1"},
		Spec: vesperv1alpha1.PreviewEnvironmentSpec{
			Image: "nginxinc/nginx-unprivileged:1.27",
			Port:  8080,
		},
	}
	previewNamespace := controller.NamespaceName(preview.Namespace, preview.Name)
	t.Cleanup(func() { cleanup(t, c, ns.Name, previewNamespace) })
	if err := c.Create(ctx, preview); err != nil {
		t.Fatalf("create preview: %v", err)
	}

	eventually(t, readyTimeout, func() error {
		var p vesperv1alpha1.PreviewEnvironment
		if err := c.Get(ctx, client.ObjectKeyFromObject(preview), &p); err != nil {
			return err
		}
		ready := meta.FindStatusCondition(p.Status.Conditions, vesperv1alpha1.ConditionReady)
		if p.Status.Phase != vesperv1alpha1.PhaseReady || ready == nil || ready.Status != metav1.ConditionTrue {
			return fmt.Errorf("phase %q, Ready condition %+v", p.Status.Phase, ready)
		}
		return nil
	})

	var d appsv1.Deployment
	key := types.NamespacedName{Namespace: previewNamespace, Name: controller.DeploymentName}
	if err := c.Get(ctx, key, &d); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	var pods corev1.PodList
	if err := c.List(ctx, &pods, client.InNamespace(previewNamespace),
		client.MatchingLabels(d.Spec.Selector.MatchLabels)); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("pods = %d, want 1", len(pods.Items))
	}
	pod := pods.Items[0]
	if !podReady(&pod) {
		t.Errorf("pod %s is not ready: %+v", pod.Name, pod.Status.Conditions)
	}
	// The restricted Pod Security profile of the namespace admitted the pod.
	if sc := pod.Spec.SecurityContext; sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("pod securityContext = %+v, want runAsNonRoot", sc)
	}

	var s corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Namespace: previewNamespace, Name: controller.ServiceName}, &s); err != nil {
		t.Fatalf("get service: %v", err)
	}
	// The Service sends traffic to the pod: an EndpointSlice lists it as ready.
	eventually(t, readyTimeout, func() error {
		var slices discoveryv1.EndpointSliceList
		if err := c.List(ctx, &slices, client.InNamespace(previewNamespace),
			client.MatchingLabels{discoveryv1.LabelServiceName: s.Name}); err != nil {
			return err
		}
		for _, slice := range slices.Items {
			for _, e := range slice.Endpoints {
				if e.Conditions.Ready != nil && *e.Conditions.Ready {
					return nil
				}
			}
		}
		return errors.New("the service has no ready endpoint")
	})
}

func podReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// cleanup deletes the preview with its namespace, then the preview namespace,
// which the operator does not delete yet (#39), and waits until they are gone.
func cleanup(t *testing.T, c client.Client, namespace, previewNamespace string) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{namespace, previewNamespace} {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := c.Delete(ctx, ns); err != nil && !apierrors.IsNotFound(err) {
			t.Errorf("delete namespace %s: %v", name, err)
		}
	}
	eventually(t, readyTimeout, func() error {
		for _, name := range []string{namespace, previewNamespace} {
			err := c.Get(ctx, types.NamespacedName{Name: name}, &corev1.Namespace{})
			if err == nil {
				return fmt.Errorf("namespace %s still exists", name)
			}
			if !apierrors.IsNotFound(err) {
				return err
			}
		}
		return nil
	})
}
