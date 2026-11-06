package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
// Expiry itself is covered with a fake clock in the unit tests.
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
