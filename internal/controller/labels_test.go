package controller

import (
	"maps"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestReconcileTracksObjectsWithLabels checks every object Vesper creates for
// a preview: each one carries the preview labels, and none has an owner
// reference, which cannot point from the preview namespace to the preview.
func TestReconcileTracksObjectsWithLabels(t *testing.T) {
	r := newIngressReconciler(t)
	reconcileOnce(t, r)

	want := map[string]string{
		LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-42",
	}
	d := getDeployment(t, r)
	ing, err := getIngress(t, r)
	if err != nil {
		t.Fatalf("get ingress: %v", err)
	}
	objects := map[string]metav1.Object{
		"namespace":    getNamespace(t, r, NamespaceName("team-a", "pr-42")),
		"deployment":   d,
		"pod template": &d.Spec.Template.ObjectMeta,
		"service":      getService(t, r),
		"ingress":      ing,
		"limitrange":   getLimitRange(t, r),
		"quota":        getResourceQuota(t, r),
	}
	for kind, obj := range objects {
		for k, v := range want {
			if got := obj.GetLabels()[k]; got != v {
				t.Errorf("%s label %s = %q, want %q", kind, k, got, v)
			}
		}
		if refs := obj.GetOwnerReferences(); len(refs) != 0 {
			t.Errorf("%s owner references = %v, want none", kind, refs)
		}
	}
}

func TestOwnedBy(t *testing.T) {
	preview := newPreview() // team-a/pr-42
	tests := []struct {
		name   string
		labels map[string]string
		want   bool
	}{
		{"all labels", previewLabels(preview), true},
		{"extra labels", map[string]string{
			LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-42", "team": "a",
		}, true},
		{"no labels", nil, false},
		{"not managed by vesper", map[string]string{
			LabelManagedBy: "helm", LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-42",
		}, false},
		{"preview name missing", map[string]string{
			LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a",
		}, false},
		{"same name in another namespace", map[string]string{
			LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-b", LabelPreviewName: "pr-42",
		}, false},
		{"another preview", map[string]string{
			LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-43",
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Labels: tt.labels}}
			if got := ownedBy(obj, preview); got != tt.want {
				t.Errorf("ownedBy = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSetPreviewLabelsKeepsOtherLabels(t *testing.T) {
	obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
		"team": "a", LabelPreviewName: "stale",
	}}}
	setPreviewLabels(obj, newPreview())

	want := map[string]string{
		"team": "a", LabelManagedBy: ManagedByVesper, LabelPreviewNamespace: "team-a", LabelPreviewName: "pr-42",
	}
	if !maps.Equal(obj.Labels, want) {
		t.Errorf("labels = %v, want %v", obj.Labels, want)
	}
}

func TestSetPreviewLabelsOnNilLabels(t *testing.T) {
	obj := &corev1.Service{}
	setPreviewLabels(obj, newPreview())
	if !maps.Equal(obj.Labels, previewLabels(newPreview())) {
		t.Errorf("labels = %v, want the preview labels", obj.Labels)
	}
}
