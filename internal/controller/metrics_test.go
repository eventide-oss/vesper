package controller

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// previewIn returns the preview name in team-a, in phase; an empty phase
// leaves the status unset.
func previewIn(name string, phase vesperv1alpha1.Phase) *vesperv1alpha1.PreviewEnvironment {
	p := newPreview()
	p.Name = name
	p.Status.Phase = phase
	return p
}

// wantActive compares vesper_previews_active with the counts of the phases
// Pending, Ready, Failed, Expired and Unknown.
func wantActive(t *testing.T, c client.Reader, pending, ready, failed, expired, unknown int) {
	t.Helper()
	want := `# HELP vesper_previews_active Number of preview environments that exist and are not being deleted, by phase.
# TYPE vesper_previews_active gauge
vesper_previews_active{phase="Expired"} ` + strconv.Itoa(expired) + `
vesper_previews_active{phase="Failed"} ` + strconv.Itoa(failed) + `
vesper_previews_active{phase="Pending"} ` + strconv.Itoa(pending) + `
vesper_previews_active{phase="Ready"} ` + strconv.Itoa(ready) + `
vesper_previews_active{phase="Unknown"} ` + strconv.Itoa(unknown) + `
`
	if err := testutil.CollectAndCompare(NewPreviewCollector(c), strings.NewReader(want)); err != nil {
		t.Error(err)
	}
}

func TestPreviewCollector(t *testing.T) {
	wantActive(t, newReconciler(t).Client, 0, 0, 0, 0, 0)

	// A preview being deleted is not counted. The fake client only keeps a
	// deletionTimestamp on objects it starts with.
	deleting := previewIn("pr-5", vesperv1alpha1.PhaseReady)
	deleting.DeletionTimestamp = &metav1.Time{Time: testNow}
	deleting.Finalizers = []string{FinalizerName}
	r := newReconciler(t,
		previewIn("pr-1", vesperv1alpha1.PhasePending),
		previewIn("pr-2", vesperv1alpha1.PhaseReady),
		previewIn("pr-3", vesperv1alpha1.PhaseReady),
		previewIn("pr-4", vesperv1alpha1.PhaseFailed),
		previewIn("pr-6", ""), // not reconciled yet
		deleting,
	)
	wantActive(t, r.Client, 1, 2, 1, 0, 1)

	// The gauge follows the previews at each scrape.
	var p vesperv1alpha1.PreviewEnvironment
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "pr-1"}, &p); err != nil {
		t.Fatal(err)
	}
	p.Status.Phase = vesperv1alpha1.PhaseReady
	if err := r.Status().Update(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(context.Background(), previewIn("pr-4", "")); err != nil {
		t.Fatal(err)
	}
	wantActive(t, r.Client, 0, 3, 0, 0, 1)
}

// TestPreviewCollectorListError checks that a scrape reports an error
// instead of wrong values when the previews cannot be read.
func TestPreviewCollectorListError(t *testing.T) {
	r := newReconciler(t)
	c := interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
		List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error {
			return errors.New("cache not synced")
		},
	})
	err := testutil.CollectAndCompare(NewPreviewCollector(c), strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "cache not synced") {
		t.Errorf("scrape error = %v, want the list error", err)
	}
}

// TestExpiredTotal checks that an expiry counts once, and only when the
// operator deleted the preview.
func TestExpiredTotal(t *testing.T) {
	tests := []struct {
		name      string
		deleteErr error
		wantErr   bool
		wantInc   float64
	}{
		{"deleted", nil, false, 1},
		{"already gone", apierrors.NewNotFound(schema.GroupResource{Resource: "previewenvironments"}, "pr-42"), false, 0},
		{"delete fails", errors.New("etcd unavailable"), true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newReconciler(t, newPreview())
			reconcileOnce(t, r)
			if tt.deleteErr != nil {
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
					Delete: func(context.Context, client.WithWatch, client.Object, ...client.DeleteOption) error {
						return tt.deleteErr
					},
				})
			}
			before := testutil.ToFloat64(expiredTotal)
			setTime(r, testCreation.Add(25*time.Hour))
			req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "team-a", Name: "pr-42"}}
			_, err := r.Reconcile(context.Background(), req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Reconcile error = %v, want error %v", err, tt.wantErr)
			}
			if got := testutil.ToFloat64(expiredTotal) - before; got != tt.wantInc {
				t.Errorf("vesper_previews_expired_total increased by %v, want %v", got, tt.wantInc)
			}
		})
	}
}
