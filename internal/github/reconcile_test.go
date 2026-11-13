// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	gh "github.com/google/go-github/v93/github"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// fakeAPI answers for GitHub in the tests of the reconciler.
type fakeAPI struct {
	prs     map[int]PullRequest
	built   map[string]bool // head commits whose image is built
	err     error
	checked int // calls to WorkflowSucceeded
}

func (f *fakeAPI) PullRequest(_ context.Context, _ string, number int) (PullRequest, error) {
	if f.err != nil {
		return PullRequest{}, f.err
	}
	pr, ok := f.prs[number]
	if !ok {
		return PullRequest{}, &gh.ErrorResponse{Response: &http.Response{StatusCode: http.StatusNotFound}}
	}
	return pr, nil
}

func (f *fakeAPI) OpenPullRequests(context.Context, string) ([]PullRequest, error) {
	var open []PullRequest
	for _, pr := range f.prs {
		if pr.Open {
			open = append(open, pr)
		}
	}
	return open, f.err
}

func (f *fakeAPI) WorkflowSucceeded(_ context.Context, _, _, sha string) (bool, error) {
	f.checked++
	return f.built[sha], f.err
}

func (f *fakeAPI) LatestDeployment(context.Context, string, string, string) (Deployment, error) {
	return Deployment{}, nil
}

func (f *fakeAPI) CreateDeployment(context.Context, string, string, string) (int64, error) {
	return 1, nil
}

func (f *fakeAPI) SetDeploymentStatus(context.Context, string, int64, string, string, string) error {
	return nil
}

func (f *fakeAPI) UpsertComment(context.Context, string, int, int64, string, string) (int64, error) {
	return 1, nil
}

type reconcileFixture struct {
	client   client.Client
	api      *fakeAPI
	r        *Reconciler
	enqueued []PullRequestKey
}

func newReconcileFixture(t *testing.T, objs ...client.Object) *reconcileFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := vesperv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	source := testSource(vesperv1alpha1.TriggerAll)
	f := &reconcileFixture{
		client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(append([]client.Object{source}, objs...)...).Build(),
		api:    &fakeAPI{prs: map[int]PullRequest{}, built: map[string]bool{}},
	}
	f.r = &Reconciler{Client: f.client, API: f.api, Now: func() time.Time { return decideNow },
		Enqueue: func(k PullRequestKey) { f.enqueued = append(f.enqueued, k) }}
	return f
}

func (f *reconcileFixture) reconcile(t *testing.T, number int) time.Duration {
	t.Helper()
	result, err := f.r.Reconcile(context.Background(), NewPullRequestKey("Acme/App", number))
	if err != nil {
		t.Fatalf("Reconcile(%d): %v", number, err)
	}
	return result.RequeueAfter
}

func (f *reconcileFixture) preview(t *testing.T, number int) *vesperv1alpha1.PreviewEnvironment {
	t.Helper()
	p := &vesperv1alpha1.PreviewEnvironment{}
	err := f.client.Get(context.Background(),
		types.NamespacedName{Namespace: "previews-acme", Name: PreviewName("acme-app", number)}, p)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReconcileLifecycle follows a pull request from its opening to its
// closing.
func TestReconcileLifecycle(t *testing.T) {
	f := newReconcileFixture(t)

	// Opened, image not built yet: nothing.
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha1}
	f.reconcile(t, 42)
	if f.preview(t, 42) != nil {
		t.Fatal("a preview was created before its image was built")
	}

	// Built: the preview is created.
	f.api.built[sha1] = true
	f.reconcile(t, 42)
	p := f.preview(t, 42)
	if p == nil || p.Spec.Image != "ghcr.io/acme/app:pr-42-"+sha1 || p.Spec.TTL.Duration != 24*time.Hour {
		t.Fatalf("preview = %+v, want the image of %s and a ttl of 24h", p, sha1[:7])
	}

	// Reconciled again: unchanged, no build check.
	checked := f.api.checked
	f.reconcile(t, 42)
	if f.api.checked != checked {
		t.Error("the build was checked again for a preview up to date")
	}

	// New commit, not built: the preview keeps the old image.
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha2}
	f.reconcile(t, 42)
	if got := f.preview(t, 42).Spec.Image; got != "ghcr.io/acme/app:pr-42-"+sha1 {
		t.Errorf("image = %s while the new commit builds, want the old one", got)
	}

	// Built, 3 hours after the creation of the preview: the new image, and a
	// ttl that restarts, so 3h + 24h counted from the creation.
	f.api.built[sha2] = true
	// The fake client sets no creation time: the preview is as old as Now
	// says from the zero time.
	created := f.preview(t, 42).CreationTimestamp.Time
	f.r.Now = func() time.Time { return created.Add(3 * time.Hour) }
	f.reconcile(t, 42)
	p = f.preview(t, 42)
	if p.Spec.Image != "ghcr.io/acme/app:pr-42-"+sha2 || p.Annotations[AnnotationHeadSHA] != sha2 {
		t.Errorf("preview = %s (%s), want the image of the new commit", p.Spec.Image, p.Annotations[AnnotationHeadSHA])
	}
	if p.Spec.TTL.Duration != 27*time.Hour {
		t.Errorf("ttl = %s, want 27h: it restarts from the commit", p.Spec.TTL.Duration)
	}

	// no-preview: deleted.
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha2, Labels: []string{LabelNoPreview}}
	f.reconcile(t, 42)
	if f.preview(t, 42) != nil {
		t.Fatal("the label no-preview left the preview")
	}

	// Label removed: back.
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha2}
	f.reconcile(t, 42)
	if f.preview(t, 42) == nil {
		t.Fatal("removing no-preview did not bring the preview back")
	}

	// Closed: deleted.
	f.api.prs[42] = PullRequest{Number: 42, HeadSHA: sha2}
	f.reconcile(t, 42)
	if f.preview(t, 42) != nil {
		t.Fatal("closing the pull request left the preview")
	}
}

// TestReconcileForeignPreview: a PreviewEnvironment of the same name, not
// created by the integration, is never changed or deleted.
func TestReconcileForeignPreview(t *testing.T) {
	foreign := &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "previews-acme", Name: "acme-app-pr-42"},
		Spec:       vesperv1alpha1.PreviewEnvironmentSpec{Image: "nginx:1.27", Port: 8080, Replicas: 1},
	}
	f := newReconcileFixture(t, foreign)
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha1}
	f.api.built[sha1] = true
	f.reconcile(t, 42)
	if got := f.preview(t, 42); got == nil || got.Spec.Image != "nginx:1.27" {
		t.Errorf("preview = %+v, want the foreign one untouched", got)
	}

	f.api.prs[42] = PullRequest{Number: 42, HeadSHA: sha1} // closed
	f.reconcile(t, 42)
	if f.preview(t, 42) == nil {
		t.Error("the foreign preview was deleted")
	}
}

// TestReconcileWithoutWorkflow: a source without buildWorkflow uses the
// image as soon as the pull request is seen.
func TestReconcileWithoutWorkflow(t *testing.T) {
	f := newReconcileFixture(t)
	source := &vesperv1alpha1.PreviewSource{}
	if err := f.client.Get(context.Background(), types.NamespacedName{Name: "acme-app"}, source); err != nil {
		t.Fatal(err)
	}
	source.Spec.BuildWorkflow = ""
	if err := f.client.Update(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	f.api.prs[7] = PullRequest{Number: 7, Open: true, HeadSHA: sha1}
	f.reconcile(t, 7)
	if f.preview(t, 7) == nil || f.api.checked != 0 {
		t.Errorf("preview %v, %d build checks; want a preview and none", f.preview(t, 7), f.api.checked)
	}
}

// TestReconcileNoSource: a repository without a source gets nothing, and no
// error, so that it is not retried.
func TestReconcileNoSource(t *testing.T) {
	f := newReconcileFixture(t)
	f.api.prs[1] = PullRequest{Number: 1, Open: true, HeadSHA: sha1}
	if _, err := f.r.Reconcile(context.Background(), NewPullRequestKey("acme/other", 1)); err != nil {
		t.Errorf("Reconcile = %v, want nil", err)
	}
	var previews vesperv1alpha1.PreviewEnvironmentList
	if err := f.client.List(context.Background(), &previews); err != nil || len(previews.Items) != 0 {
		t.Errorf("%d previews, %v; want none", len(previews.Items), err)
	}
}

// TestReconcileErrors: errors of the API are returned, for a retry with
// backoff; a pull request that GitHub no longer has is treated as closed.
func TestReconcileErrors(t *testing.T) {
	f := newReconcileFixture(t)
	f.api.prs[5] = PullRequest{Number: 5, Open: true, HeadSHA: sha1}
	f.api.built[sha1] = true
	f.reconcile(t, 5)

	f.api.err = errors.New("rate limited")
	if _, err := f.r.Reconcile(context.Background(), NewPullRequestKey("acme/app", 5)); err == nil {
		t.Error("an API error was not returned")
	}
	if f.preview(t, 5) == nil {
		t.Error("an API error deleted the preview")
	}

	f.api.err = nil
	delete(f.api.prs, 5)
	f.reconcile(t, 5)
	if f.preview(t, 5) != nil {
		t.Error("a pull request GitHub no longer has kept its preview")
	}
}

// TestReconcileReplace: past the bound of 7 days, the preview is deleted,
// and created again once gone.
func TestReconcileReplace(t *testing.T) {
	f := newReconcileFixture(t)
	f.api.prs[9] = PullRequest{Number: 9, Open: true, HeadSHA: sha1}
	f.api.built[sha1], f.api.built[sha2] = true, true
	f.reconcile(t, 9)

	f.api.prs[9] = PullRequest{Number: 9, Open: true, HeadSHA: sha2}
	created := f.preview(t, 9).CreationTimestamp.Time
	f.r.Now = func() time.Time { return created.Add(6*24*time.Hour + 2*time.Hour) }
	if after := f.reconcile(t, 9); after == 0 {
		t.Error("no requeue after the deletion of the preview to replace")
	}
	if f.preview(t, 9) != nil {
		t.Fatal("the preview past the bound was not deleted")
	}
	f.reconcile(t, 9)
	if p := f.preview(t, 9); p == nil || p.Spec.Image != "ghcr.io/acme/app:pr-9-"+sha2 || p.Spec.TTL.Duration != 24*time.Hour {
		t.Errorf("preview = %+v, want it created again with the new image and a fresh ttl", p)
	}
}

// TestReconcileResync: number 0 queues the open pull requests and those of
// the existing previews.
func TestReconcileResync(t *testing.T) {
	f := newReconcileFixture(t)
	f.api.prs[1] = PullRequest{Number: 1, Open: true, HeadSHA: sha1}
	f.api.prs[2] = PullRequest{Number: 2, Open: true, HeadSHA: sha1}
	f.api.prs[3] = PullRequest{Number: 3, HeadSHA: sha1} // closed
	f.api.built[sha1] = true
	f.reconcile(t, 2)
	// Closed since, without a webhook: only the preview remembers it.
	f.api.prs[2] = PullRequest{Number: 2, HeadSHA: sha1}
	f.api.prs[4] = PullRequest{Number: 4, Open: true, HeadSHA: sha1}

	f.reconcile(t, 0)
	var numbers []int
	for _, k := range f.enqueued {
		if k.Repository != "acme/app" {
			t.Errorf("queued %+v, want the repository in lowercase", k)
		}
		numbers = append(numbers, k.Number)
	}
	slices.Sort(numbers)
	if !slices.Equal(numbers, []int{1, 2, 4}) {
		t.Errorf("queued %v, want 1 and 4 (open) and 2 (its preview)", numbers)
	}
}
