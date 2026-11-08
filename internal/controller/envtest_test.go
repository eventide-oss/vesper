package controller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// envPreview is a preview in a test of its own: its namespace, its name and
// the name of its preview namespace.
type envPreview struct {
	namespace, name, previewNamespace string
}

// createEnvPreview creates the preview pr-42 in a new user namespace.
func createEnvPreview(t *testing.T, spec vesperv1alpha1.PreviewEnvironmentSpec) envPreview {
	t.Helper()
	p := envPreview{namespace: newUserNamespace(t), name: "pr-42"}
	p.previewNamespace = NamespaceName(p.namespace, p.name)
	preview := &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.namespace, Name: p.name},
		Spec:       spec,
	}
	if err := envClient.Create(context.Background(), preview); err != nil {
		t.Fatalf("create preview: %v", err)
	}
	return p
}

func (p envPreview) get(obj client.Object) error {
	return envClient.Get(context.Background(), types.NamespacedName{Namespace: p.namespace, Name: p.name}, obj)
}

func (p envPreview) getInPreviewNamespace(name string, obj client.Object) error {
	return envClient.Get(context.Background(), types.NamespacedName{Namespace: p.previewNamespace, Name: name}, obj)
}

// waitForStatus waits until the preview has seen its latest spec and is in
// phase with the Ready condition reason, and returns it.
func (p envPreview) waitForStatus(
	t *testing.T, phase vesperv1alpha1.Phase, reason string,
) *vesperv1alpha1.PreviewEnvironment {
	t.Helper()
	var preview vesperv1alpha1.PreviewEnvironment
	eventually(t, func() error {
		if err := p.get(&preview); err != nil {
			return err
		}
		ready := meta.FindStatusCondition(preview.Status.Conditions, vesperv1alpha1.ConditionReady)
		if preview.Status.ObservedGeneration != preview.Generation || preview.Status.Phase != phase ||
			ready == nil || ready.Reason != reason {
			return fmt.Errorf("status = %+v, want phase %s, reason %s, generation %d",
				preview.Status, phase, reason, preview.Generation)
		}
		return nil
	})
	return &preview
}

var testSpec = vesperv1alpha1.PreviewEnvironmentSpec{Image: "ghcr.io/acme/app:pr-42"}

func TestEnvtestCreatesPreviewObjects(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)

	preview := p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	if want := "http://app." + p.previewNamespace + ".svc.cluster.local"; preview.Status.InternalURL != want {
		t.Errorf("internalURL = %q, want %q", preview.Status.InternalURL, want)
	}
	if preview.Status.Namespace != p.previewNamespace {
		t.Errorf("status.namespace = %q, want %q", preview.Status.Namespace, p.previewNamespace)
	}

	labels := map[string]string{
		LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: p.namespace, LabelPreviewName: p.name,
	}
	var ns corev1.Namespace
	if err := envClient.Get(context.Background(), types.NamespacedName{Name: p.previewNamespace}, &ns); err != nil {
		t.Fatalf("get preview namespace: %v", err)
	}
	wantNamespaceLabels := maps.Clone(labels)
	wantNamespaceLabels[labelPodSecurity] = "restricted"
	for k, v := range wantNamespaceLabels {
		if ns.Labels[k] != v {
			t.Errorf("namespace label %s = %q, want %q", k, ns.Labels[k], v)
		}
	}

	// The API server added its defaults: the next reconciliations must keep
	// them, which the fake client could only simulate.
	var d appsv1.Deployment
	if err := p.getInPreviewNamespace(DeploymentName, &d); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got := d.Spec.Template.Spec.Containers[0].Image; got != testSpec.Image {
		t.Errorf("image = %q, want %q", got, testSpec.Image)
	}
	if d.Spec.Strategy.Type == "" || d.Spec.Template.Spec.Containers[0].ImagePullPolicy == "" {
		t.Errorf("deployment has no server defaults: %+v", d.Spec)
	}
	var s corev1.Service
	if err := p.getInPreviewNamespace(ServiceName, &s); err != nil {
		t.Fatalf("get service: %v", err)
	}
	if s.Spec.ClusterIP == "" {
		t.Error("service has no clusterIP")
	}
	for kind, obj := range map[string]metav1.Object{"deployment": &d, "service": &s} {
		if !ownedBy(obj, preview) {
			t.Errorf("%s labels = %v, want the preview labels", kind, obj.GetLabels())
		}
		if len(obj.GetOwnerReferences()) != 0 {
			t.Errorf("%s owner references = %v, want none", kind, obj.GetOwnerReferences())
		}
	}
}

// TestEnvtestReadyWhenDeploymentAvailable writes the status the Deployment
// controller would write once the pods run: the watch on Deployments
// reconciles the preview, which becomes Ready.
func TestEnvtestReadyWhenDeploymentAvailable(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)

	eventually(t, func() error {
		var d appsv1.Deployment
		if err := p.getInPreviewNamespace(DeploymentName, &d); err != nil {
			return err
		}
		d.Status = appsv1.DeploymentStatus{
			ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1,
		}
		return envClient.Status().Update(context.Background(), &d) // retried on a conflict
	})

	preview := p.waitForStatus(t, vesperv1alpha1.PhaseReady, vesperv1alpha1.ReasonDeploymentAvailable)
	ready := meta.FindStatusCondition(preview.Status.Conditions, vesperv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Message != "1/1 replicas available" {
		t.Errorf("Ready condition = %+v, want True, 1/1 replicas available", ready)
	}
}

// TestEnvtestUpdatesImage changes the spec: the API server increases the
// generation, and the controller rolls the new image out.
func TestEnvtestUpdatesImage(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)

	const image = "ghcr.io/acme/app:pr-42-b"
	eventually(t, func() error {
		var preview vesperv1alpha1.PreviewEnvironment
		if err := p.get(&preview); err != nil {
			return err
		}
		preview.Spec.Image = image
		return envClient.Update(context.Background(), &preview) // retried on a conflict
	})

	preview := p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	if preview.Generation != 2 {
		t.Errorf("generation = %d, want 2", preview.Generation)
	}
	eventually(t, func() error {
		var d appsv1.Deployment
		if err := p.getInPreviewNamespace(DeploymentName, &d); err != nil {
			return err
		}
		if got := d.Spec.Template.Spec.Containers[0].Image; got != image {
			return fmt.Errorf("image = %q, want %q", got, image)
		}
		return nil
	})
}

// TestEnvtestRestoresDeletedObjects deletes the Deployment and the Service by
// hand: the watches reconcile the preview, which creates them again.
func TestEnvtestRestoresDeletedObjects(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)

	for name, obj := range map[string]client.Object{"deployment": &appsv1.Deployment{}, "service": &corev1.Service{}} {
		t.Run(name, func(t *testing.T) {
			if err := p.getInPreviewNamespace("app", obj); err != nil {
				t.Fatalf("get %s: %v", name, err)
			}
			oldUID := obj.GetUID()
			if err := envClient.Delete(context.Background(), obj); err != nil {
				t.Fatalf("delete %s: %v", name, err)
			}
			eventually(t, func() error {
				if err := p.getInPreviewNamespace("app", obj); err != nil {
					return err
				}
				if obj.GetUID() == oldUID {
					return errors.New("not deleted yet")
				}
				return nil
			})
		})
	}
}

// TestEnvtestNamespaceConflict creates the preview namespace beforehand,
// without the labels of the preview: the controller leaves it alone.
func TestEnvtestNamespaceConflict(t *testing.T) {
	requireEnvtest(t)
	userNamespace := newUserNamespace(t)
	foreign := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: NamespaceName(userNamespace, "pr-42"), Labels: map[string]string{"owner": "someone"},
	}}
	if err := envClient.Create(context.Background(), foreign); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	preview := &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: userNamespace, Name: "pr-42"},
		Spec:       testSpec,
	}
	if err := envClient.Create(context.Background(), preview); err != nil {
		t.Fatalf("create preview: %v", err)
	}
	p := envPreview{namespace: userNamespace, name: "pr-42", previewNamespace: foreign.Name}

	p.waitForStatus(t, vesperv1alpha1.PhaseFailed, vesperv1alpha1.ReasonNamespaceConflict)
	var ns corev1.Namespace
	if err := envClient.Get(context.Background(), types.NamespacedName{Name: foreign.Name}, &ns); err != nil {
		t.Fatalf("get namespace: %v", err)
	}
	if !maps.Equal(ns.Labels, map[string]string{"owner": "someone", "kubernetes.io/metadata.name": foreign.Name}) {
		t.Errorf("foreign namespace labels = %v, want them unchanged", ns.Labels)
	}
	err := p.getInPreviewNamespace(DeploymentName, &appsv1.Deployment{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("get deployment in the foreign namespace: %v, want NotFound", err)
	}
}

// TestEnvtestAPIDefaults checks the defaults the API server fills in from the
// CRD schema.
func TestEnvtestAPIDefaults(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)

	var preview vesperv1alpha1.PreviewEnvironment
	if err := p.get(&preview); err != nil {
		t.Fatalf("get preview: %v", err)
	}
	spec := preview.Spec
	if spec.Port != 8080 || spec.Replicas != 1 || spec.TTL == nil || spec.TTL.Duration.String() != "24h0m0s" {
		t.Errorf("defaults: port %d, replicas %d, ttl %v; want 8080, 1, 24h", spec.Port, spec.Replicas, spec.TTL)
	}
}

// TestEnvtestAPIValidation sends invalid previews: the API server rejects
// them with the rules of the CRD schema, before the controller sees them.
func TestEnvtestAPIValidation(t *testing.T) {
	requireEnvtest(t)
	namespace := newUserNamespace(t)
	ttl := func(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

	tests := []struct {
		name    string
		preview string
		mutate  func(*vesperv1alpha1.PreviewEnvironmentSpec)
		message string // part of the error message, usually the field
	}{
		{"latest tag", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) { s.Image = "ghcr.io/acme/app:latest" }, "spec.image"},
		{"no tag", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) { s.Image = "ghcr.io/acme/app" }, "spec.image"},
		{"uppercase image", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) { s.Image = "ghcr.io/Acme/app:1" }, "spec.image"},
		{"port too high", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) { s.Port = 70000 }, "spec.port"},
		{"too many replicas", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) { s.Replicas = 6 }, "spec.replicas"},
		{"ttl too short", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) { s.TTL = ttl(30 * time.Second) }, "spec.ttl"},
		{"ttl too long", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) { s.TTL = ttl(200 * time.Hour) }, "spec.ttl"},
		{"bad env name", "", func(s *vesperv1alpha1.PreviewEnvironmentSpec) {
			s.Env = []vesperv1alpha1.EnvVar{{Name: "1BAD"}}
		}, "spec.env"},
		{"name over 63 characters", strings.Repeat("a", 64), nil, "at most 63 characters"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := *testSpec.DeepCopy()
			spec.Port, spec.Replicas = 8080, 1
			if tt.mutate != nil {
				tt.mutate(&spec)
			}
			name := "pr-1"
			if tt.preview != "" {
				name = tt.preview
			}
			preview := &vesperv1alpha1.PreviewEnvironment{
				ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
				Spec:       spec,
			}
			err := envClient.Create(context.Background(), preview)
			if !apierrors.IsInvalid(err) {
				t.Fatalf("create: %v, want an Invalid error", err)
			}
			if !strings.Contains(err.Error(), tt.message) {
				t.Errorf("error %q does not name %q", err, tt.message)
			}
		})
	}
}

// TestEnvtestDeletionWaitsForNamespace deletes a preview: the controller
// deletes its namespace and keeps the preview until the namespace is gone.
// envtest has no namespace controller, so the namespace stays Terminating and
// the preview must stay with its finalizer; the e2e test covers the end.
func TestEnvtestDeletionWaitsForNamespace(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	preview := p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	if !controllerutil.ContainsFinalizer(preview, FinalizerName) {
		t.Fatalf("finalizers = %v, want %s", preview.Finalizers, FinalizerName)
	}

	if err := envClient.Delete(context.Background(), preview); err != nil {
		t.Fatalf("delete preview: %v", err)
	}
	eventually(t, func() error {
		var ns corev1.Namespace
		if err := envClient.Get(context.Background(), types.NamespacedName{Name: p.previewNamespace}, &ns); err != nil {
			return err
		}
		if ns.DeletionTimestamp.IsZero() {
			return errors.New("preview namespace not deleted yet")
		}
		return nil
	})

	var got vesperv1alpha1.PreviewEnvironment
	if err := p.get(&got); err != nil {
		t.Fatalf("get preview: %v, want it kept while its namespace terminates", err)
	}
	if got.DeletionTimestamp.IsZero() || !controllerutil.ContainsFinalizer(&got, FinalizerName) {
		t.Errorf("preview = %+v, want it being deleted, with its finalizer", got.ObjectMeta)
	}
}

// TestEnvtestExpiresAtFollowsTTL checks status.expiresAt against the
// creation time the API server sets: 24h by default, then the new ttl.
func TestEnvtestExpiresAtFollowsTTL(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)

	waitForExpiry := func(ttl time.Duration) {
		t.Helper()
		eventually(t, func() error {
			var preview vesperv1alpha1.PreviewEnvironment
			if err := p.get(&preview); err != nil {
				return err
			}
			want := preview.CreationTimestamp.Add(ttl)
			if got := preview.Status.ExpiresAt; got == nil || !got.Time.Equal(want) {
				return fmt.Errorf("expiresAt = %v, want %s", got, want)
			}
			return nil
		})
	}
	waitForExpiry(24 * time.Hour)

	eventually(t, func() error {
		var preview vesperv1alpha1.PreviewEnvironment
		if err := p.get(&preview); err != nil {
			return err
		}
		preview.Spec.TTL = &metav1.Duration{Duration: 72 * time.Hour}
		return envClient.Update(context.Background(), &preview) // retried on a conflict
	})
	waitForExpiry(72 * time.Hour)
}

// TestEnvtestExtendAndShortenTTL moves the clock of the controller past the
// expiry of a preview, without waiting. Raising the ttl then saves the
// preview; lowering it below its age expires it at once (ADR-0004): it is
// marked Expired and deleted, and its finalizer keeps it while its namespace
// terminates.
func TestEnvtestExtendAndShortenTTL(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)

	// 25h later, the default ttl of 24h has passed. Nothing triggers a
	// reconciliation yet: the controller comes back at the expiry in real
	// time, or when the spec changes.
	shiftTime(t, 25*time.Hour)

	setTTL := func(ttl time.Duration) {
		t.Helper()
		eventually(t, func() error {
			var preview vesperv1alpha1.PreviewEnvironment
			if err := p.get(&preview); err != nil {
				return err
			}
			preview.Spec.TTL = &metav1.Duration{Duration: ttl}
			return envClient.Update(context.Background(), &preview) // retried on a conflict
		})
	}

	setTTL(48 * time.Hour)
	preview := p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	if want := preview.CreationTimestamp.Add(48 * time.Hour); preview.Status.ExpiresAt == nil ||
		!preview.Status.ExpiresAt.Time.Equal(want) {
		t.Errorf("expiresAt = %v, want %s", preview.Status.ExpiresAt, want)
	}
	if !preview.DeletionTimestamp.IsZero() {
		t.Fatal("extended preview is being deleted")
	}

	setTTL(24 * time.Hour)
	eventually(t, func() error {
		var got vesperv1alpha1.PreviewEnvironment
		if err := p.get(&got); err != nil {
			return err
		}
		if got.Status.Phase != vesperv1alpha1.PhaseExpired || got.DeletionTimestamp.IsZero() {
			return fmt.Errorf("phase %s, deletionTimestamp %v; want Expired and being deleted",
				got.Status.Phase, got.DeletionTimestamp)
		}
		if !controllerutil.ContainsFinalizer(&got, FinalizerName) {
			return fmt.Errorf("finalizers = %v, want %s while the namespace terminates", got.Finalizers, FinalizerName)
		}
		var ns corev1.Namespace
		if err := envClient.Get(context.Background(), types.NamespacedName{Name: p.previewNamespace}, &ns); err != nil {
			return err
		}
		if ns.DeletionTimestamp.IsZero() {
			return errors.New("preview namespace not deleted yet")
		}
		return nil
	})
	eventually(t, func() error {
		var list eventsv1.EventList
		if err := envClient.List(context.Background(), &list, client.InNamespace(p.namespace)); err != nil {
			return err
		}
		for _, e := range list.Items {
			if e.Regarding.Name == p.name && e.Reason == vesperv1alpha1.ReasonExpired && e.Type == corev1.EventTypeNormal {
				return nil
			}
		}
		return fmt.Errorf("no Normal %s event for the preview", vesperv1alpha1.ReasonExpired)
	})
}

// TestEnvtestCreatesIngress checks that the API server accepts the Ingress of
// a preview and that status.url gives its host.
func TestEnvtestCreatesIngress(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	host := HostName(p.namespace, p.name, envtestDomain)

	eventually(t, func() error {
		var preview vesperv1alpha1.PreviewEnvironment
		if err := p.get(&preview); err != nil {
			return err
		}
		if want := "http://" + host; preview.Status.URL != want {
			return fmt.Errorf("url = %q, want %q", preview.Status.URL, want)
		}
		return nil
	})
	var ing networkingv1.Ingress
	if err := p.getInPreviewNamespace(IngressName, &ing); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if len(ing.Spec.Rules) != 1 || ing.Spec.Rules[0].Host != host {
		t.Errorf("rules = %+v, want one rule for %s", ing.Spec.Rules, host)
	}
}

// TestEnvtestAcceptsTLSIngress checks that the API server accepts the
// Ingress of --preview-tls, whose TLS section names no Secret, and keeps it as
// written. The controller under test runs without TLS, so the Ingress is
// built by mutateIngress and created directly.
func TestEnvtestAcceptsTLSIngress(t *testing.T) {
	requireEnvtest(t)
	namespace := newUserNamespace(t)
	preview := &vesperv1alpha1.PreviewEnvironment{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "pr-42"}}
	host := HostName(namespace, "pr-42", envtestDomain)
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: IngressName}}
	mutateIngress(ing, preview, host, "", true)
	if err := envClient.Create(context.Background(), ing); err != nil {
		t.Fatalf("create ingress with tls: %v", err)
	}

	var got networkingv1.Ingress
	if err := envClient.Get(context.Background(), client.ObjectKeyFromObject(ing), &got); err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	if want := []networkingv1.IngressTLS{{Hosts: []string{host}}}; !equality.Semantic.DeepEqual(got.Spec.TLS, want) {
		t.Errorf("tls = %+v, want %+v", got.Spec.TLS, want)
	}
}

// TestEnvtestCreatesLimits checks that the API server accepts the LimitRange
// and the ResourceQuota of a preview. envtest runs no quota controller, so it
// cannot admit pods under a quota: the e2e test checks the pods.
func TestEnvtestCreatesLimits(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)

	var lr corev1.LimitRange
	eventually(t, func() error { return p.getInPreviewNamespace(LimitsName, &lr) })
	if len(lr.Spec.Limits) != 1 || lr.Spec.Limits[0].Default.Cpu().String() != "500m" {
		t.Errorf("limitrange limits = %+v, want the default limit of 500m CPU", lr.Spec.Limits)
	}
	var q corev1.ResourceQuota
	eventually(t, func() error { return p.getInPreviewNamespace(LimitsName, &q) })
	if got := q.Spec.Hard.Pods().String(); got != "10" {
		t.Errorf("resourcequota pods = %s, want 10", got)
	}
}

// TestEnvtestCreatesNetworkPolicy checks that the API server accepts the
// NetworkPolicy of a preview as Vesper writes it: a later reconciliation,
// after a change of the spec, does not rewrite it. envtest has no network,
// so the e2e test checks that the policy is enforced.
func TestEnvtestCreatesNetworkPolicy(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)

	var np networkingv1.NetworkPolicy
	if err := p.getInPreviewNamespace(NetworkPolicyName, &np); err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if !equality.Semantic.DeepEqual(np.Spec, wantNetworkPolicySpec()) {
		t.Errorf("networkpolicy spec = %+v, want %+v", np.Spec, wantNetworkPolicySpec())
	}

	eventually(t, func() error {
		var preview vesperv1alpha1.PreviewEnvironment
		if err := p.get(&preview); err != nil {
			return err
		}
		preview.Spec.Replicas = 2
		return envClient.Update(context.Background(), &preview)
	})
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	var after networkingv1.NetworkPolicy
	if err := p.getInPreviewNamespace(NetworkPolicyName, &after); err != nil {
		t.Fatalf("get networkpolicy: %v", err)
	}
	if after.ResourceVersion != np.ResourceVersion {
		t.Errorf("networkpolicy rewritten by a reconciliation: resourceVersion %s then %s",
			np.ResourceVersion, after.ResourceVersion)
	}
}

// TestEnvtestRejectsPrivilegedPod creates a pod that breaks the restricted
// Pod Security Standard in the namespace of a preview: the API server rejects
// it. envtest runs no quota controller, so the API server would reject any
// pod under the quota of the preview: the test checks that the reason is Pod
// Security.
func TestEnvtestRejectsPrivilegedPod(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)
	eventually(t, func() error { return p.getInPreviewNamespace(DeploymentName, &appsv1.Deployment{}) })

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: p.previewNamespace, Name: "privileged"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:            "app",
			Image:           "ghcr.io/acme/app:pr-42",
			SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)},
		}}},
	}
	err := envClient.Create(context.Background(), pod)
	if err == nil || !strings.Contains(err.Error(), `violates PodSecurity "restricted`) {
		t.Errorf("create privileged pod: %v, want a rejection by Pod Security", err)
	}
}

// TestEnvtestRecordsEvents checks that the events of a preview reach the API
// server, attached to the preview, where kubectl describe finds them.
func TestEnvtestRecordsEvents(t *testing.T) {
	requireEnvtest(t)
	p := createEnvPreview(t, testSpec)

	eventually(t, func() error {
		var list eventsv1.EventList
		if err := envClient.List(context.Background(), &list, client.InNamespace(p.namespace)); err != nil {
			return err
		}
		for _, e := range list.Items {
			if e.Regarding.Name == p.name && e.Reason == EventNamespaceCreated &&
				e.Note == "created namespace "+p.previewNamespace {
				return nil
			}
		}
		return fmt.Errorf("no NamespaceCreated event for the preview in %d events", len(list.Items))
	})
}

// scrapeMetrics reads the metrics endpoint of the manager.
func scrapeMetrics() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, envMetricsURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", envMetricsURL, resp.Status)
	}
	return string(body), nil
}

// metricValue returns the value of the series line, such as
// `vesper_previews_active{phase="Pending"}`, in a scrape.
func metricValue(scrape, series string) (float64, error) {
	for line := range strings.Lines(scrape) {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), series+" "); ok {
			return strconv.ParseFloat(value, 64)
		}
	}
	return 0, fmt.Errorf("no series %s", series)
}

// TestEnvtestServesMetrics reads the metrics endpoint of the manager: it
// serves the metrics of Vesper with those of controller-runtime, and the
// gauge counts a new preview. Previews of the other tests remain, so the
// test compares the count before and after.
func TestEnvtestServesMetrics(t *testing.T) {
	requireEnvtest(t)
	const pending = `vesper_previews_active{phase="Pending"}`

	before, err := scrapeMetrics()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		pending, `vesper_previews_active{phase="Unknown"}`, "vesper_previews_expired_total",
		`controller_runtime_reconcile_total{controller="previewenvironment"`,
	} {
		if !strings.Contains(before, want) {
			t.Errorf("metrics do not contain %s", want)
		}
	}
	pendingBefore, err := metricValue(before, pending)
	if err != nil {
		t.Fatal(err)
	}

	p := createEnvPreview(t, testSpec)
	p.waitForStatus(t, vesperv1alpha1.PhasePending, vesperv1alpha1.ReasonProvisioning)
	eventually(t, func() error {
		after, err := scrapeMetrics()
		if err != nil {
			return err
		}
		got, err := metricValue(after, pending)
		if err != nil {
			return err
		}
		if got != pendingBefore+1 {
			return fmt.Errorf("%s = %v, want %v", pending, got, pendingBefore+1)
		}
		return nil
	})
}
