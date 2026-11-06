package controller

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func getLimitRange(t *testing.T, r *PreviewEnvironmentReconciler) *corev1.LimitRange {
	t.Helper()
	var lr corev1.LimitRange
	key := types.NamespacedName{Namespace: NamespaceName("team-a", "pr-42"), Name: LimitsName}
	if err := r.Get(context.Background(), key, &lr); err != nil {
		t.Fatalf("get limitrange: %v", err)
	}
	return &lr
}

func getResourceQuota(t *testing.T, r *PreviewEnvironmentReconciler) *corev1.ResourceQuota {
	t.Helper()
	var q corev1.ResourceQuota
	key := types.NamespacedName{Namespace: NamespaceName("team-a", "pr-42"), Name: LimitsName}
	if err := r.Get(context.Background(), key, &q); err != nil {
		t.Fatalf("get resourcequota: %v", err)
	}
	return &q
}

// checkLimits checks the LimitRange and the ResourceQuota of the preview
// against res.
func checkLimits(t *testing.T, r *PreviewEnvironmentReconciler, res Resources) {
	t.Helper()
	wantLimits := []corev1.LimitRangeItem{{
		Type:           corev1.LimitTypeContainer,
		DefaultRequest: corev1.ResourceList{"cpu": res.CPURequest, "memory": res.MemoryRequest},
		Default:        corev1.ResourceList{"cpu": res.CPULimit, "memory": res.MemoryLimit},
	}}
	if got := getLimitRange(t, r).Spec.Limits; !equality.Semantic.DeepEqual(got, wantLimits) {
		t.Errorf("limitrange limits = %+v, want %+v", got, wantLimits)
	}
	wantHard := corev1.ResourceList{
		"limits.cpu":             res.QuotaCPU,
		"limits.memory":          res.QuotaMemory,
		"pods":                   *resource.NewQuantity(res.QuotaPods, resource.DecimalSI),
		"services.nodeports":     resource.MustParse("0"),
		"services.loadbalancers": resource.MustParse("0"),
	}
	if got := getResourceQuota(t, r).Spec.Hard; !equality.Semantic.DeepEqual(got, wantHard) {
		t.Errorf("resourcequota hard = %v, want %v", got, wantHard)
	}
}

func TestReconcileCreatesDefaultLimits(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	want := Resources{
		CPURequest: resource.MustParse("100m"), MemoryRequest: resource.MustParse("128Mi"),
		CPULimit: resource.MustParse("500m"), MemoryLimit: resource.MustParse("512Mi"),
		QuotaCPU: resource.MustParse("4"), QuotaMemory: resource.MustParse("4Gi"), QuotaPods: 10,
	}
	checkLimits(t, r, want)
}

// TestReconcileUsesConfiguredResources gives the operator other bounds, as
// its flags do, then changes them: the objects follow.
func TestReconcileUsesConfiguredResources(t *testing.T) {
	r := newReconciler(t, newPreview())
	res := Resources{
		CPURequest: resource.MustParse("250m"), MemoryRequest: resource.MustParse("256Mi"),
		CPULimit: resource.MustParse("1"), MemoryLimit: resource.MustParse("1Gi"),
		QuotaCPU: resource.MustParse("8"), QuotaMemory: resource.MustParse("8Gi"), QuotaPods: 20,
	}
	r.Resources = &res
	reconcileOnce(t, r)
	checkLimits(t, r, res)

	res.QuotaPods = 5
	res.MemoryLimit = resource.MustParse("768Mi")
	reconcileOnce(t, r)
	checkLimits(t, r, res)
}

// TestReconcileRestoresChangedQuota raises the quota by hand: the operator
// puts it back, and a reconciliation that changes nothing writes nothing.
func TestReconcileRestoresChangedQuota(t *testing.T) {
	r := newReconciler(t, newPreview())
	reconcileOnce(t, r)

	q := getResourceQuota(t, r)
	q.Spec.Hard["pods"] = resource.MustParse("1000")
	if err := r.Update(context.Background(), q); err != nil {
		t.Fatalf("update resourcequota: %v", err)
	}
	reconcileOnce(t, r)
	checkLimits(t, r, DefaultResources())

	before := getResourceQuota(t, r).ResourceVersion
	reconcileOnce(t, r)
	if after := getResourceQuota(t, r).ResourceVersion; after != before {
		t.Errorf("resourcequota rewritten without change: resourceVersion %s then %s", before, after)
	}
}

// TestReconcileCreatesLimitsBeforeDeployment records the order of creation:
// the API server applies a LimitRange and a quota only to pods created after
// them, and the quota rejects pods without limits. The NetworkPolicy also
// comes first, so that no pod runs without it.
func TestReconcileCreatesLimitsBeforeDeployment(t *testing.T) {
	r := newReconciler(t, newPreview())
	var created []string
	r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			created = append(created, fmt.Sprintf("%T", obj))
			return c.Create(ctx, obj, opts...)
		},
	})
	reconcileOnce(t, r)

	want := []string{
		"*v1.Namespace", "*v1.LimitRange", "*v1.ResourceQuota", "*v1.NetworkPolicy", "*v1.Deployment", "*v1.Service",
	}
	if !reflect.DeepEqual(created, want) {
		t.Errorf("created %v, want %v", created, want)
	}
}

func TestReconcileLeavesForeignLimitsUntouched(t *testing.T) {
	nsName := NamespaceName("team-a", "pr-42")
	foreignLabels := map[string]string{"owner": "someone"}
	for _, foreign := range []client.Object{
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: LimitsName, Labels: foreignLabels}},
		&corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Namespace: nsName, Name: LimitsName, Labels: foreignLabels}},
	} {
		t.Run(fmt.Sprintf("%T", foreign), func(t *testing.T) {
			r := newReconciler(t, newPreview(), newPreviewNamespace(nsName), foreign)
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
			if _, err := r.Reconcile(context.Background(), req); err == nil {
				t.Error("Reconcile succeeded, want an error for an object Vesper did not create")
			}
			got := foreign.DeepCopyObject().(client.Object)
			if err := r.Get(context.Background(), client.ObjectKeyFromObject(foreign), got); err != nil {
				t.Fatalf("get: %v, want the foreign object kept", err)
			}
			if !maps.Equal(got.GetLabels(), foreignLabels) || got.GetResourceVersion() != "999" {
				t.Errorf("foreign object modified: %+v", got)
			}
		})
	}
}

func TestResourcesValidate(t *testing.T) {
	tests := []struct {
		name    string
		change  func(*Resources)
		wantErr string
	}{
		{"defaults", func(*Resources) {}, ""},
		{"request equals limit", func(r *Resources) { r.CPURequest = r.CPULimit }, ""},
		{"limit equals quota", func(r *Resources) { r.MemoryLimit = r.QuotaMemory }, ""},
		{"zero cpu request", func(r *Resources) { r.CPURequest = resource.MustParse("0") },
			"cpu request must be positive"},
		{"negative memory quota", func(r *Resources) { r.QuotaMemory = resource.MustParse("-1Gi") },
			"memory quota must be positive"},
		{"no pods", func(r *Resources) { r.QuotaPods = 0 }, "pod quota must be at least 1"},
		{"request above limit", func(r *Resources) { r.MemoryRequest = resource.MustParse("1Gi") },
			"memory request 1Gi is more than the memory limit 512Mi"},
		{"limit above quota", func(r *Resources) { r.CPULimit = resource.MustParse("5") },
			"cpu limit 5 is more than the cpu quota 4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := DefaultResources()
			tt.change(&res)
			err := res.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("Validate: %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate: %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}
