// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"strings"
	"testing"
	"time"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

func TestComment(t *testing.T) {
	expires := time.Date(2026, time.November, 14, 12, 0, 0, 0, time.FixedZone("CET", 3600))
	ready := Report{Preview: "acme-app-pr-42", State: StateReady, URL: "https://acme-app-pr-42.preview.example.com",
		HeadSHA: sha1, ExpiresAt: &expires, Trigger: vesperv1alpha1.TriggerAll}

	want := `<!-- vesper:preview acme-app-pr-42 -->
### ✦ Your preview is ready

| Preview | Status | URL | Commit | Expires |
| --- | --- | --- | --- | --- |
| acme-app-pr-42 | ✦ Ready | [Visit preview ↗](https://acme-app-pr-42.preview.example.com) | ` + "`1111111`" + ` | 2026-11-14 11:00 UTC |

<sub>Updated with each push. Deleted when the pull request closes or the TTL ends. Add the label ` + "`no-preview`" + ` to stop it. Previews by [Vesper](https://github.com/eventide-oss/vesper).</sub>
`
	if got := Comment(ready); got != want {
		t.Errorf("Comment(ready) =\n%s\nwant\n%s", got, want)
	}

	tests := []struct {
		name    string
		report  Report
		want    []string
		notWant []string
	}{
		{"starting", Report{Preview: "p", State: StateStarting, HeadSHA: sha1, Trigger: vesperv1alpha1.TriggerAll},
			[]string{"Your preview is starting", "✦ Starting", "| — |", "`no-preview`"}, []string{"Visit preview"}},
		{"ready without a public URL", Report{Preview: "p", State: StateReady, HeadSHA: sha1},
			[]string{"no public URL: `kubectl port-forward`"}, []string{"Visit preview"}},
		{"trigger Label", Report{Preview: "p", State: StateReady, Trigger: vesperv1alpha1.TriggerLabel},
			[]string{"Remove the label `preview` to stop it"}, []string{"no-preview"}},
		{"failed", Report{Preview: "p", State: StateFailed, Reason: "PodsRejected: exceeded quota",
			URL: "https://p.example.com"}, []string{"Your preview failed", "PodsRejected: exceeded quota"}, nil},
		{"expired", Report{Preview: "p", State: StateExpired, URL: "https://p.example.com", ExpiresAt: &expires},
			[]string{"Your preview expired", "Push a commit to bring the preview back"},
			[]string{"Visit preview", "2026-11-14", "label"}},
		{"stopped", Report{Preview: "p", State: StateStopped, Reason: "the pull request is closed",
			URL: "https://p.example.com"}, []string{"Your preview was stopped", "the pull request is closed"},
			[]string{"Visit preview"}},
		{"a reason cannot inject Markdown or HTML", Report{Preview: "p", State: StateFailed,
			Reason: "bad | table\n<img src=x> `code` [link](https://evil.example)"},
			[]string{"bad \\| table &lt;img src=x&gt; 'code' \\[link\\](https://evil.example)"},
			[]string{"<img", "`code`", "[link](https://evil.example)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Comment(tt.report)
			if !strings.HasPrefix(got, Marker(tt.report.Preview)) {
				t.Errorf("comment does not start with its marker:\n%s", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("comment lacks %q:\n%s", w, got)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("comment has %q:\n%s", w, got)
				}
			}
		})
	}
}

func TestDeploymentState(t *testing.T) {
	for _, tt := range []struct {
		state      State
		reason     string
		wantState  string
		wantPrefix string
	}{
		{StateStarting, "", "in_progress", "Starting"},
		{StateReady, "", "success", "The preview is ready"},
		{StateFailed, "RolloutStuck", "failure", "The preview failed: RolloutStuck"},
		{StateExpired, "", "inactive", DescriptionExpired},
		{StateStopped, "the pull request is closed", "inactive", "Stopped: the pull request is closed"},
	} {
		state, description := Report{State: tt.state, Reason: tt.reason}.DeploymentState()
		if state != tt.wantState || !strings.HasPrefix(description, tt.wantPrefix) {
			t.Errorf("%s: %s %q, want %s %q", tt.state, state, description, tt.wantState, tt.wantPrefix)
		}
	}
	if _, d := (Report{State: StateStopped}).DeploymentState(); strings.HasPrefix(d, DescriptionExpired) {
		t.Errorf("a stopped preview reads as expired: %q", d)
	}
}
