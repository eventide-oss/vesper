package controller

import (
	"context"
	"maps"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
)

func getService(t *testing.T, r *PreviewEnvironmentReconciler) *corev1.Service {
	t.Helper()
	var s corev1.Service
	key := types.NamespacedName{Namespace: NamespaceName("team-a", "pr-42"), Name: ServiceName}
	if err := r.Get(context.Background(), key, &s); err != nil {
		t.Fatalf("get service: %v", err)
	}
	return &s
}

func TestReconcileCreatesService(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	s := getService(t, r)
	labels := map[string]string{
		LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-42",
	}
	if !maps.Equal(s.Labels, labels) {
		t.Errorf("labels = %v, want %v", s.Labels, labels)
	}
	if len(s.OwnerReferences) != 0 {
		t.Errorf("owner references = %v, want none", s.OwnerReferences)
	}
	if s.Spec.Type != corev1.ServiceTypeClusterIP {
		t.Errorf("type = %q, want ClusterIP", s.Spec.Type)
	}
	// The selector must match the pods of the Deployment.
	podLabels := getDeployment(t, r).Spec.Template.Labels
	for k, v := range s.Spec.Selector {
		if podLabels[k] != v {
			t.Errorf("selector %s=%s does not match the pod labels %v", k, v, podLabels)
		}
	}
	if len(s.Spec.Selector) == 0 {
		t.Error("selector is empty: the Service would select every pod of the namespace")
	}
	want := []corev1.ServicePort{{
		Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, TargetPort: intstr.FromString("http"),
	}}
	if !reflect.DeepEqual(s.Spec.Ports, want) {
		t.Errorf("ports = %+v, want %+v", s.Spec.Ports, want)
	}

	wantURL := "http://app." + NamespaceName("team-a", "pr-42") + ".svc.cluster.local"
	if got := getPreview(t, r).Status.InternalURL; got != wantURL {
		t.Errorf("internalURL = %q, want %q", got, wantURL)
	}
}

// TestReconcileKeepsServiceDefaults simulates the fields the API server fills
// in, among them clusterIP, which can never change: they are kept and the
// next reconciliation writes nothing.
func TestReconcileKeepsServiceDefaults(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	s := getService(t, r)
	s.Spec.ClusterIP = "10.96.12.34"
	s.Spec.ClusterIPs = []string{"10.96.12.34"}
	s.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
	s.Spec.IPFamilyPolicy = ptr.To(corev1.IPFamilyPolicySingleStack)
	s.Spec.SessionAffinity = corev1.ServiceAffinityNone
	s.Spec.InternalTrafficPolicy = ptr.To(corev1.ServiceInternalTrafficPolicyCluster)
	if err := r.Update(context.Background(), s); err != nil {
		t.Fatalf("update service: %v", err)
	}
	before := getService(t, r).ResourceVersion

	reconcileOnce(t, r)
	got := getService(t, r)
	if got.ResourceVersion != before {
		t.Errorf("service written again: resourceVersion %s, then %s", before, got.ResourceVersion)
	}
	if got.Spec.ClusterIP != "10.96.12.34" {
		t.Errorf("clusterIP = %q, want it kept", got.Spec.ClusterIP)
	}
}

// TestReconcileServiceFollowsPort checks that a new spec.port changes the
// container port only: the Service targets it by name.
func TestReconcileServiceFollowsPort(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)
	before := getService(t, r)

	preview := getPreview(t, r)
	preview.Spec.Port = 3000
	if err := r.Update(context.Background(), preview); err != nil {
		t.Fatalf("update preview: %v", err)
	}
	reconcileOnce(t, r)

	if got := getDeployment(t, r).Spec.Template.Spec.Containers[0].Ports[0]; got.Name != "http" || got.ContainerPort != 3000 {
		t.Errorf("container port = %+v, want http/3000", got)
	}
	if got := getService(t, r); got.ResourceVersion != before.ResourceVersion {
		t.Errorf("service changed (%+v), want it unchanged: it targets the port by name", got.Spec.Ports)
	}
}

func TestReconcileLeavesForeignServiceUntouched(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName, Labels: previewLabels(newPreview())}}
	foreign := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: ServiceName, Labels: map[string]string{"owner": "someone"}},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort},
	}
	r := newReconciler(t, newPreview(), ns, foreign)

	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
	if _, err := r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("Reconcile succeeded, want an error for a service Vesper did not create")
	}
	s := getService(t, r)
	if !maps.Equal(s.Labels, foreign.Labels) || s.Spec.Type != corev1.ServiceTypeNodePort {
		t.Errorf("foreign service was modified: %+v", s)
	}
}
