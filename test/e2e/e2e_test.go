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
	eventsv1 "k8s.io/api/events/v1"
	networkingv1 "k8s.io/api/networking/v1"
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

	// previewDomain is the --preview-domain of the operator, set in
	// test/e2e/kustomization.yaml.
	previewDomain = "preview.localtest.me"

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

// TestPreviewLifecycle creates a preview of an unprivileged nginx image, waits
// until the operator, running in the cluster, has deployed it and its pod is
// ready, then deletes it and waits until the preview and its namespace are
// gone.
func TestPreviewLifecycle(t *testing.T) {
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

	waitForReady(t, c, preview)
	checkConditionsAndEvents(t, c, preview)

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
	// Vesper sets no resources on the container: the LimitRange of the
	// namespace gave it the defaults, and the quota admitted it.
	want := controller.DefaultResources()
	got := pod.Spec.Containers[0].Resources
	if !got.Requests.Cpu().Equal(want.CPURequest) || !got.Requests.Memory().Equal(want.MemoryRequest) ||
		!got.Limits.Cpu().Equal(want.CPULimit) || !got.Limits.Memory().Equal(want.MemoryLimit) {
		t.Errorf("container resources = %+v, want the defaults of the LimitRange", got)
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

	// The preview has a public address: an Ingress for its host. kind has no
	// ingress controller, so the routing itself is not tested here.
	host := controller.HostName(preview.Namespace, preview.Name, previewDomain)
	var p vesperv1alpha1.PreviewEnvironment
	if err := c.Get(ctx, client.ObjectKeyFromObject(preview), &p); err != nil {
		t.Fatalf("get preview: %v", err)
	}
	if want := "http://" + host; p.Status.URL != want {
		t.Errorf("url = %q, want %q", p.Status.URL, want)
	}
	var ing networkingv1.Ingress
	if err := c.Get(ctx, types.NamespacedName{Namespace: previewNamespace, Name: controller.IngressName}, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != host {
		t.Errorf("ingress rules = %+v, want one rule for %s", ing.Spec.Rules, host)
	}

	// Deleting the preview deletes its namespace first: the finalizer keeps
	// the preview until the namespace is gone (#39).
	if err := c.Delete(ctx, preview); err != nil {
		t.Fatalf("delete preview: %v", err)
	}
	eventually(t, readyTimeout, func() error {
		err := c.Get(ctx, types.NamespacedName{Name: previewNamespace}, &corev1.Namespace{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get preview namespace %s: %v, want NotFound", previewNamespace, err)
		}
		err = c.Get(ctx, client.ObjectKeyFromObject(preview), &vesperv1alpha1.PreviewEnvironment{})
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get preview: %v, want NotFound", err)
		}
		return nil
	})
}

// checkConditionsAndEvents checks what kubectl describe shows of a Ready
// preview: the Available and Expired conditions, and the event of its
// readiness, which the operator records with its own permissions.
func checkConditionsAndEvents(t *testing.T, c client.Client, preview *vesperv1alpha1.PreviewEnvironment) {
	t.Helper()
	ctx := context.Background()
	var p vesperv1alpha1.PreviewEnvironment
	if err := c.Get(ctx, client.ObjectKeyFromObject(preview), &p); err != nil {
		t.Fatalf("get preview: %v", err)
	}
	for typ, want := range map[string]metav1.ConditionStatus{
		vesperv1alpha1.ConditionAvailable: metav1.ConditionTrue,
		vesperv1alpha1.ConditionExpired:   metav1.ConditionFalse,
	} {
		if got := meta.FindStatusCondition(p.Status.Conditions, typ); got == nil || got.Status != want {
			t.Errorf("%s condition = %+v, want %s", typ, got, want)
		}
	}
	eventually(t, readyTimeout, func() error {
		var list eventsv1.EventList
		if err := c.List(ctx, &list, client.InNamespace(preview.Namespace)); err != nil {
			return err
		}
		for _, e := range list.Items {
			if e.Regarding.Name == preview.Name && e.Reason == vesperv1alpha1.ReasonDeploymentAvailable {
				return nil
			}
		}
		return fmt.Errorf("no %s event for the preview", vesperv1alpha1.ReasonDeploymentAvailable)
	})
}

// waitForReady waits until the operator reports the preview Ready, with the
// namespace where it runs.
func waitForReady(t *testing.T, c client.Client, preview *vesperv1alpha1.PreviewEnvironment) {
	t.Helper()
	eventually(t, readyTimeout, func() error {
		var p vesperv1alpha1.PreviewEnvironment
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(preview), &p); err != nil {
			return err
		}
		ready := meta.FindStatusCondition(p.Status.Conditions, vesperv1alpha1.ConditionReady)
		if p.Status.Phase != vesperv1alpha1.PhaseReady || ready == nil || ready.Status != metav1.ConditionTrue {
			return fmt.Errorf("phase %q, Ready condition %+v", p.Status.Phase, ready)
		}
		// A ready preview tells where it runs.
		if want := controller.NamespaceName(p.Namespace, p.Name); p.Status.Namespace != want {
			return fmt.Errorf("status.namespace %q, want %q", p.Status.Namespace, want)
		}
		return nil
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

// cleanup deletes the namespace of the test, with the preview in it, and the
// preview namespace, in case the test stopped before deleting the preview, and
// waits until they are gone.
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
