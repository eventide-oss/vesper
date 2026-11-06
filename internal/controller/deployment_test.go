package controller

import (
	"context"
	"maps"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

func getDeployment(t *testing.T, r *PreviewEnvironmentReconciler) *appsv1.Deployment {
	t.Helper()
	var d appsv1.Deployment
	key := types.NamespacedName{Namespace: NamespaceName("team-a", "pr-42"), Name: DeploymentName}
	if err := r.Get(context.Background(), key, &d); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	return &d
}

func TestReconcileCreatesDeployment(t *testing.T) {
	preview := newPreview()
	preview.Spec.Port = 3000
	preview.Spec.Replicas = 2
	preview.Spec.Env = []vesperv1alpha1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "EMPTY"}}
	r := newReconciler(t, preview)
	reconcileOnce(t, r)

	d := getDeployment(t, r)
	labels := map[string]string{
		LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-42",
	}
	if !maps.Equal(d.Labels, labels) {
		t.Errorf("labels = %v, want %v", d.Labels, labels)
	}
	if got := ptr.Deref(d.Spec.Replicas, 0); got != 2 {
		t.Errorf("replicas = %d, want 2", got)
	}
	selector := map[string]string{"app.kubernetes.io/name": "preview-app"}
	if d.Spec.Selector == nil || !maps.Equal(d.Spec.Selector.MatchLabels, selector) {
		t.Errorf("selector = %v, want %v", d.Spec.Selector, selector)
	}
	podLabels := maps.Clone(labels)
	maps.Copy(podLabels, selector)
	if !maps.Equal(d.Spec.Template.Labels, podLabels) {
		t.Errorf("pod labels = %v, want %v", d.Spec.Template.Labels, podLabels)
	}

	pod := d.Spec.Template.Spec
	if !ptr.Equal(pod.AutomountServiceAccountToken, ptr.To(false)) {
		t.Errorf("automountServiceAccountToken = %v, want false", pod.AutomountServiceAccountToken)
	}
	wantPodSecurity := &corev1.PodSecurityContext{
		RunAsNonRoot:   ptr.To(true),
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	if !reflect.DeepEqual(pod.SecurityContext, wantPodSecurity) {
		t.Errorf("pod securityContext = %+v, want %+v", pod.SecurityContext, wantPodSecurity)
	}
	if len(pod.Containers) != 1 {
		t.Fatalf("containers = %d, want 1", len(pod.Containers))
	}
	want := corev1.Container{
		Name:  "app",
		Image: "ghcr.io/acme/app:pr-42",
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 3000, Protocol: corev1.ProtocolTCP}},
		Env:   []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}, {Name: "EMPTY"}},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
	if !reflect.DeepEqual(pod.Containers[0], want) {
		t.Errorf("container =\n%+v\nwant\n%+v", pod.Containers[0], want)
	}
}

func TestReconcileUpdatesImage(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	preview := getPreview(t, r)
	preview.Spec.Image = "ghcr.io/acme/app:pr-42-b"
	if err := r.Update(context.Background(), preview); err != nil {
		t.Fatalf("update preview: %v", err)
	}
	reconcileOnce(t, r)

	if got := getDeployment(t, r).Spec.Template.Spec.Containers[0].Image; got != "ghcr.io/acme/app:pr-42-b" {
		t.Errorf("image = %q, want the new one", got)
	}
}

// TestReconcileKeepsServerDefaults simulates the defaults the API server adds
// and a change made by hand: the change is reverted, the defaults are kept,
// and the next reconciliation writes nothing.
func TestReconcileKeepsServerDefaults(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	d := getDeployment(t, r)
	d.Spec.Strategy.Type = appsv1.RollingUpdateDeploymentStrategyType
	d.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyAlways
	c := &d.Spec.Template.Spec.Containers[0]
	c.ImagePullPolicy = corev1.PullIfNotPresent
	c.TerminationMessagePath = corev1.TerminationMessagePathDefault
	c.Image = "attacker/image:1" // changed by hand
	if err := r.Update(context.Background(), d); err != nil {
		t.Fatalf("update deployment: %v", err)
	}

	reconcileOnce(t, r)
	d = getDeployment(t, r)
	c = &d.Spec.Template.Spec.Containers[0]
	if c.Image != "ghcr.io/acme/app:pr-42" {
		t.Errorf("image = %q, want it restored", c.Image)
	}
	if c.ImagePullPolicy != corev1.PullIfNotPresent || c.TerminationMessagePath == "" ||
		d.Spec.Strategy.Type == "" || d.Spec.Template.Spec.RestartPolicy == "" {
		t.Errorf("server defaults were erased: %+v", d.Spec)
	}

	before := d.ResourceVersion
	reconcileOnce(t, r)
	if got := getDeployment(t, r).ResourceVersion; got != before {
		t.Errorf("deployment written again: resourceVersion %s, then %s", before, got)
	}
}

func TestReconcileReadyWhenDeploymentAvailable(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	ready := meta.FindStatusCondition(getPreview(t, r).Status.Conditions, vesperv1alpha1.ConditionReady)
	if ready == nil || ready.Message != "0/1 replicas available" {
		t.Fatalf("Ready condition before the pods run = %+v, want 0/1 replicas available", ready)
	}

	d := getDeployment(t, r)
	d.Status = appsv1.DeploymentStatus{
		ObservedGeneration: d.Generation, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1,
	}
	if err := r.Status().Update(context.Background(), d); err != nil {
		t.Fatalf("update deployment status: %v", err)
	}
	reconcileOnce(t, r)

	p := getPreview(t, r)
	if p.Status.Phase != vesperv1alpha1.PhaseReady {
		t.Errorf("phase = %q, want Ready", p.Status.Phase)
	}
	ready = meta.FindStatusCondition(p.Status.Conditions, vesperv1alpha1.ConditionReady)
	if ready == nil || ready.Status != metav1.ConditionTrue ||
		ready.Reason != vesperv1alpha1.ReasonDeploymentAvailable || ready.Message != "1/1 replicas available" {
		t.Errorf("Ready condition = %+v, want True/DeploymentAvailable, 1/1 replicas available", ready)
	}
}

func TestReconcileLeavesForeignDeploymentUntouched(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: previewLabels(newPreview())}}
	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: DeploymentName, Labels: map[string]string{"owner": "someone"}},
	}
	r := newReconciler(t, newPreview(), ns, foreign)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("Reconcile succeeded, want an error for a deployment Vesper did not create")
	}
	d := getDeployment(t, r)
	if !maps.Equal(d.Labels, foreign.Labels) || len(d.Spec.Template.Spec.Containers) != 0 {
		t.Errorf("foreign deployment was modified: %+v", d)
	}
}

func TestDeploymentAvailable(t *testing.T) {
	tests := []struct {
		name        string
		generation  int64
		status      appsv1.DeploymentStatus
		want        bool
		wantMessage string
	}{
		{"all available", 2, appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}, true, "2/2 replicas available"},
		{"some starting", 2, appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 1}, false, "1/2 replicas available"},
		{"latest spec not seen", 3, appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}, false, "2/2 replicas available"},
		{"old pods still running", 2, appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 3, UpdatedReplicas: 2, AvailableReplicas: 2}, false, "2/2 replicas available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: tt.generation},
				Spec:       appsv1.DeploymentSpec{Replicas: ptr.To(int32(2))},
				Status:     tt.status,
			}
			got, message := deploymentAvailable(d)
			if got != tt.want || message != tt.wantMessage {
				t.Errorf("deploymentAvailable = %v, %q, want %v, %q", got, message, tt.want, tt.wantMessage)
			}
		})
	}
}
