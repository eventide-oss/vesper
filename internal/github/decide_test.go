// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

var decideNow = time.Date(2026, time.November, 13, 12, 0, 0, 0, time.UTC)

func testSource(trigger vesperv1alpha1.Trigger) *vesperv1alpha1.PreviewSource {
	return &vesperv1alpha1.PreviewSource{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-app"},
		Spec: vesperv1alpha1.PreviewSourceSpec{
			Repository:    "acme/app",
			Image:         "ghcr.io/acme/app:pr-{{number}}-{{sha}}",
			BuildWorkflow: "preview.yml",
			Namespace:     "previews-acme",
			Trigger:       trigger,
			Defaults: vesperv1alpha1.PreviewDefaults{
				Port: 3000, Replicas: 2, TTL: &metav1.Duration{Duration: 24 * time.Hour},
				Env: []vesperv1alpha1.EnvVar{{Name: "APP_ENV", Value: "preview"}},
			},
		},
	}
}

// existing returns the preview of pull request 42 at commit sha, created age
// ago, with a ttl.
func existing(sha string, age, ttl time.Duration) *vesperv1alpha1.PreviewEnvironment {
	p := desired(testSource(vesperv1alpha1.TriggerAll), PullRequest{Number: 42, HeadSHA: sha},
		RenderImage("ghcr.io/acme/app:pr-{{number}}-{{sha}}", 42, sha), &metav1.Duration{Duration: ttl})
	p.CreationTimestamp = metav1.NewTime(decideNow.Add(-age))
	return p
}

func TestDecide(t *testing.T) {
	open := PullRequest{Number: 42, Open: true, HeadSHA: sha1}
	with := func(pr PullRequest, f func(*PullRequest)) PullRequest { f(&pr); return pr }

	tests := []struct {
		name       string
		trigger    vesperv1alpha1.Trigger
		pr         PullRequest
		imageReady bool
		current    *vesperv1alpha1.PreviewEnvironment
		wantAction Action
		wantTTL    time.Duration // for Apply and Replace
		wantReason string        // part of the reason
	}{
		{"new pull request, image built", vesperv1alpha1.TriggerAll, open, true, nil, ActionApply, 24 * time.Hour, "new preview"},
		{"new pull request, image not built yet", vesperv1alpha1.TriggerAll, open, false, nil, ActionKeep, 0, "waiting for preview.yml"},
		{"closed", vesperv1alpha1.TriggerAll, with(open, func(p *PullRequest) { p.Open = false }), true,
			existing(sha1, time.Hour, 24*time.Hour), ActionNone, 0, "closed"},
		{"draft", vesperv1alpha1.TriggerAll, with(open, func(p *PullRequest) { p.Draft = true }), true, nil, ActionNone, 0, "draft"},
		{"fork", vesperv1alpha1.TriggerAll, with(open, func(p *PullRequest) { p.Fork = true }), true, nil, ActionNone, 0, "fork"},
		{"fork with the label preview", vesperv1alpha1.TriggerLabel,
			with(open, func(p *PullRequest) { p.Fork, p.Labels = true, []string{LabelPreview} }), true, nil, ActionNone, 0, "fork"},
		{"trigger All, label no-preview", vesperv1alpha1.TriggerAll,
			with(open, func(p *PullRequest) { p.Labels = []string{LabelNoPreview} }), true,
			existing(sha1, time.Hour, 24*time.Hour), ActionNone, 0, "no-preview"},
		{"trigger All, label preview ignored", vesperv1alpha1.TriggerAll,
			with(open, func(p *PullRequest) { p.Labels = []string{LabelPreview} }), true, nil, ActionApply, 24 * time.Hour, "new preview"},
		{"trigger Label, no label", vesperv1alpha1.TriggerLabel, open, true, nil, ActionNone, 0, "no label preview"},
		{"trigger Label, label preview", vesperv1alpha1.TriggerLabel,
			with(open, func(p *PullRequest) { p.Labels = []string{"bug", LabelPreview} }), true, nil, ActionApply, 24 * time.Hour, "new preview"},
		{"trigger Label, label no-preview means nothing", vesperv1alpha1.TriggerLabel,
			with(open, func(p *PullRequest) { p.Labels = []string{LabelNoPreview} }), true, nil, ActionNone, 0, "no label preview"},
		{"up to date: the ttl stays", vesperv1alpha1.TriggerAll, open, true,
			existing(sha1, 3*time.Hour, 30*time.Hour), ActionApply, 30 * time.Hour, "up to date"},
		{"up to date even if the image check fails", vesperv1alpha1.TriggerAll, open, false,
			existing(sha1, 3*time.Hour, 30*time.Hour), ActionApply, 30 * time.Hour, "up to date"},
		{"new commit, not built yet: the preview keeps the old one", vesperv1alpha1.TriggerAll,
			with(open, func(p *PullRequest) { p.HeadSHA = sha2 }), false, existing(sha1, 3*time.Hour, 24*time.Hour), ActionKeep, 0, "waiting"},
		{"new commit, built: the ttl restarts from now", vesperv1alpha1.TriggerAll,
			with(open, func(p *PullRequest) { p.HeadSHA = sha2 }), true, existing(sha1, 3*time.Hour, 24*time.Hour),
			ActionApply, 27 * time.Hour, "new commit 2222222"},
		{"new commit on a preview of 6 days and 1 hour: past 7 days, replaced", vesperv1alpha1.TriggerAll,
			with(open, func(p *PullRequest) { p.HeadSHA = sha2 }), true, existing(sha1, 6*24*time.Hour+time.Hour, 6*24*time.Hour+2*time.Hour),
			ActionReplace, 24 * time.Hour, "replaced"},
		{"new commit on a preview of 6 days, exactly at the bound", vesperv1alpha1.TriggerAll,
			with(open, func(p *PullRequest) { p.HeadSHA = sha2 }), true, existing(sha1, 6*24*time.Hour, 24*time.Hour),
			ActionApply, 7 * 24 * time.Hour, "new commit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(testSource(tt.trigger), tt.pr, tt.imageReady, tt.current, decideNow)
			if got.Action != tt.wantAction {
				t.Fatalf("action = %s (%s), want %s", got.Action, got.Reason, tt.wantAction)
			}
			if !strings.Contains(got.Reason, tt.wantReason) {
				t.Errorf("reason = %q, want it to contain %q", got.Reason, tt.wantReason)
			}
			if tt.wantAction != ActionApply && tt.wantAction != ActionReplace {
				if got.Preview != nil {
					t.Errorf("preview = %+v, want none", got.Preview)
				}
				return
			}
			if got.Preview.Spec.TTL == nil || got.Preview.Spec.TTL.Duration != tt.wantTTL {
				t.Errorf("ttl = %v, want %s", got.Preview.Spec.TTL, tt.wantTTL)
			}
		})
	}
}

// TestDecidePreview checks the preview a source defines.
func TestDecidePreview(t *testing.T) {
	source := testSource(vesperv1alpha1.TriggerAll)
	got := Decide(source, PullRequest{Number: 42, Open: true, HeadSHA: sha1}, true, nil, decideNow).Preview

	if got.Name != "acme-app-pr-42" || got.Namespace != "previews-acme" {
		t.Errorf("preview %s/%s, want previews-acme/acme-app-pr-42", got.Namespace, got.Name)
	}
	if want := "ghcr.io/acme/app:pr-42-" + sha1; got.Spec.Image != want {
		t.Errorf("image = %s, want %s", got.Spec.Image, want)
	}
	if got.Spec.Port != 3000 || got.Spec.Replicas != 2 || len(got.Spec.Env) != 1 || got.Spec.Env[0].Name != "APP_ENV" {
		t.Errorf("spec = %+v, want the defaults of the source", got.Spec)
	}
	if got.Labels[LabelSource] != "acme-app" || got.Labels[LabelPullRequest] != "42" {
		t.Errorf("labels = %v", got.Labels)
	}
	if got.Annotations[AnnotationRepository] != "acme/app" || got.Annotations[AnnotationHeadSHA] != sha1 {
		t.Errorf("annotations = %v", got.Annotations)
	}

	// The preview owns copies: changing it leaves the source as it was.
	got.Spec.Env[0].Value = "changed"
	got.Spec.TTL.Duration = time.Minute
	if source.Spec.Defaults.Env[0].Value != "preview" || source.Spec.Defaults.TTL.Duration != 24*time.Hour {
		t.Error("the preview shares the env or the ttl of its source")
	}

	// Without a ttl in the source, the default of a preview.
	source.Spec.Defaults.TTL = nil
	if got := Decide(source, PullRequest{Number: 1, Open: true, HeadSHA: sha1}, true, nil, decideNow).Preview; got.Spec.TTL.Duration != vesperv1alpha1.DefaultTTL {
		t.Errorf("ttl = %s, want %s", got.Spec.TTL.Duration, vesperv1alpha1.DefaultTTL)
	}
}

func TestRenderImage(t *testing.T) {
	for template, want := range map[string]string{
		"ghcr.io/acme/app:pr-{{number}}-{{sha}}":  "ghcr.io/acme/app:pr-7-" + sha1,
		"acme/app:{{sha}}":                        "acme/app:" + sha1,
		"r.example.com:5000/a/{{number}}:{{sha}}": "r.example.com:5000/a/7:" + sha1,
	} {
		if got := RenderImage(template, 7, sha1); got != want {
			t.Errorf("RenderImage(%s) = %s, want %s", template, got, want)
		}
	}
	if got := PreviewName("acme-app", 1234); got != "acme-app-pr-1234" {
		t.Errorf("PreviewName = %s", got)
	}
}

func TestSourceFor(t *testing.T) {
	source := func(name, repository string) vesperv1alpha1.PreviewSource {
		return vesperv1alpha1.PreviewSource{ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: vesperv1alpha1.PreviewSourceSpec{Repository: repository}}
	}
	sources := []vesperv1alpha1.PreviewSource{
		source("acme-app", "acme/app"), source("acme-web", "acme/web"), source("web-again", "ACME/Web"),
	}

	got, err := SourceFor(sources, "Acme/App")
	if err != nil || got.Name != "acme-app" {
		t.Errorf("SourceFor(Acme/App) = %v, %v; want acme-app, compared without case", got, err)
	}
	if _, err := SourceFor(sources, "acme/api"); err == nil || !strings.Contains(err.Error(), "no PreviewSource") {
		t.Errorf("SourceFor(acme/api) error = %v, want none allows it", err)
	}
	if _, err := SourceFor(sources, "acme/web"); err == nil || !strings.Contains(err.Error(), "acme-web, web-again") {
		t.Errorf("SourceFor(acme/web) error = %v, want both named", err)
	}
}
