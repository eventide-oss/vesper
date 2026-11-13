// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// setPhase sets the status of the preview of pull request number, as the
// operator would.
func (f *reconcileFixture) setPhase(t *testing.T, number int, phase vesperv1alpha1.Phase, url string) {
	t.Helper()
	p := f.preview(t, number)
	p.Status.Phase, p.Status.URL = phase, url
	expires := metav1.NewTime(time.Date(2026, time.November, 14, 12, 0, 0, 0, time.UTC))
	p.Status.ExpiresAt = &expires
	if phase == vesperv1alpha1.PhaseFailed {
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{Type: vesperv1alpha1.ConditionReady,
			Status: metav1.ConditionFalse, Reason: vesperv1alpha1.ReasonPodsRejected, Message: "exceeded quota"})
	}
	if err := f.client.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
}

func lastStatus(f *reconcileFixture, environment string) string {
	s := f.api.statuses(environment)
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

// TestReportLifecycle follows what a pull request shows of its preview.
func TestReportLifecycle(t *testing.T) {
	f := newReconcileFixture(t)
	// The fake client sets no creation time: an hour after it, the preview
	// is an hour old, and a new commit updates it rather than replace it.
	f.r.Now = func() time.Time { return time.Time{}.Add(time.Hour) }
	const env = "acme-app-pr-42"
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha1}
	f.api.built[sha1] = true

	// Created: starting.
	f.reconcile(t, 42)
	if got := lastStatus(f, env); !strings.HasPrefix(got, "in_progress") {
		t.Fatalf("status = %q, want in_progress", got)
	}
	if !strings.Contains(f.api.comments[42], "Your preview is starting") {
		t.Fatalf("comment = %q, want starting", f.api.comments[42])
	}

	// Nothing changed: GitHub is not written to again.
	writes, statuses := f.api.commentWrites, len(f.api.statuses(env))
	f.reconcile(t, 42)
	if f.api.commentWrites != writes || len(f.api.statuses(env)) != statuses {
		t.Error("an unchanged preview wrote to GitHub again")
	}

	// Ready: success with the URL, and the link in the comment.
	f.setPhase(t, 42, vesperv1alpha1.PhaseReady, "https://acme-app-pr-42.preview.example.com")
	f.reconcile(t, 42)
	if got := lastStatus(f, env); got != "success: The preview is ready" || f.api.deployments[0].url == "" {
		t.Errorf("status = %q, url %q; want success with the URL", got, f.api.deployments[0].url)
	}
	if c := f.api.comments[42]; !strings.Contains(c, "Your preview is ready") || !strings.Contains(c, "Visit preview") {
		t.Errorf("comment = %q, want ready with a link", c)
	}

	// Failed: failure with the reason of the Ready condition.
	f.setPhase(t, 42, vesperv1alpha1.PhaseFailed, "")
	f.reconcile(t, 42)
	if got := lastStatus(f, env); got != "failure: The preview failed: PodsRejected: exceeded quota" {
		t.Errorf("status = %q", got)
	}

	// A new commit, built: a deployment of its own.
	f.setPhase(t, 42, vesperv1alpha1.PhaseReady, "https://acme-app-pr-42.preview.example.com")
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha2}
	f.api.built[sha2] = true
	f.reconcile(t, 42)
	if n := len(f.api.deployments); n != 2 || f.api.deployments[1].sha != sha2 {
		t.Fatalf("%d deployments, want a second one for the new commit", n)
	}
	if !strings.Contains(f.api.comments[42], "`2222222`") {
		t.Errorf("comment = %q, want the new commit", f.api.comments[42])
	}

	// no-preview: deleted, and stopped.
	f.api.prs[42] = PullRequest{Number: 42, Open: true, HeadSHA: sha2, Labels: []string{LabelNoPreview}}
	f.reconcile(t, 42)
	if got := lastStatus(f, env); got != "inactive: Stopped: the pull request has the label no-preview" {
		t.Errorf("status = %q, want stopped", got)
	}
	if c := f.api.comments[42]; !strings.Contains(c, "Your preview was stopped") {
		t.Errorf("comment = %q, want stopped", c)
	}
	writes = f.api.commentWrites
	f.reconcile(t, 42)
	if f.api.commentWrites != writes {
		t.Error("a stopped preview was reported again")
	}
}

// TestReportNothingWithoutPreview: a pull request that never had a preview
// gets no deployment and no comment.
func TestReportNothingWithoutPreview(t *testing.T) {
	f := newReconcileFixture(t)
	f.api.prs[7] = PullRequest{Number: 7, Open: true, Draft: true, HeadSHA: sha1}
	f.api.prs[8] = PullRequest{Number: 8, Open: true, HeadSHA: sha1} // not built
	f.api.prs[9] = PullRequest{Number: 9, HeadSHA: sha1}             // closed
	for _, n := range []int{7, 8, 9} {
		f.reconcile(t, n)
	}
	if len(f.api.deployments) != 0 || f.api.commentWrites != 0 {
		t.Errorf("%d deployments, %d comments; want none", len(f.api.deployments), f.api.commentWrites)
	}
}

// TestReportExpired: an expired preview is shown so, and the resync does
// not bring it back; a new commit does (ADR-0007).
func TestReportExpired(t *testing.T) {
	f := newReconcileFixture(t)
	const env = "acme-app-pr-5"
	f.api.prs[5] = PullRequest{Number: 5, Open: true, HeadSHA: sha1}
	f.api.built[sha1], f.api.built[sha2] = true, true
	f.reconcile(t, 5)

	// The operator expires it: phase Expired, deletion held by its finalizer.
	p := f.preview(t, 5)
	p.Finalizers = []string{"vesper.eventide.dev/cleanup"}
	if err := f.client.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.setPhase(t, 5, vesperv1alpha1.PhaseExpired, "")
	if err := f.client.Delete(context.Background(), f.preview(t, 5)); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t, 5)
	if got := lastStatus(f, env); !strings.HasPrefix(got, "inactive: "+DescriptionExpired) {
		t.Fatalf("status = %q, want inactive and expired", got)
	}
	if c := f.api.comments[5]; !strings.Contains(c, "Your preview expired") {
		t.Errorf("comment = %q, want expired", c)
	}

	// Gone: the resync and the next webhooks leave it expired.
	p = f.preview(t, 5)
	p.Finalizers = nil
	if err := f.client.Update(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	f.reconcile(t, 5)
	f.reconcile(t, 5)
	if f.preview(t, 5) != nil {
		t.Fatal("the expired preview was created again on the same commit")
	}

	// A new commit brings it back.
	f.api.prs[5] = PullRequest{Number: 5, Open: true, HeadSHA: sha2}
	f.reconcile(t, 5)
	if p := f.preview(t, 5); p == nil || !strings.HasSuffix(p.Spec.Image, sha2) {
		t.Fatalf("preview = %+v, want it back on the new commit", p)
	}
	states := f.api.statuses(env)
	if !slices.Contains(states, "in_progress: Starting the preview") {
		t.Errorf("statuses = %v, want the new commit starting", states)
	}
}

// TestPullRequestOf maps a preview to its pull request.
func TestPullRequestOf(t *testing.T) {
	p := desired(testSource(vesperv1alpha1.TriggerAll), PullRequest{Number: 42, HeadSHA: sha1}, "img:1",
		&metav1.Duration{Duration: time.Hour})
	if got := pullRequestOf(context.Background(), p); !slices.Equal(got, []PullRequestKey{{"acme/app", 42}}) {
		t.Errorf("pullRequestOf = %v", got)
	}
	foreign := &vesperv1alpha1.PreviewEnvironment{}
	if got := pullRequestOf(context.Background(), foreign); got != nil {
		t.Errorf("a preview not created by the integration maps to %v", got)
	}
	p.Annotations[AnnotationRepository] = "../evil"
	if got := pullRequestOf(context.Background(), p); got != nil {
		t.Errorf("a crafted repository maps to %v", got)
	}
}
