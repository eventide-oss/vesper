// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"
	"strings"
	"time"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// State is what the pull request shows of its preview (ADR-0007).
type State string

const (
	// StateStarting: the preview is created, its pods are not ready yet.
	StateStarting State = "Starting"
	// StateReady: the application runs, at its URL.
	StateReady State = "Ready"
	// StateFailed: a problem blocks the preview; Reason says which.
	StateFailed State = "Failed"
	// StateExpired: the ttl of the preview passed. A new commit brings it
	// back; the resync does not.
	StateExpired State = "Expired"
	// StateStopped: the preview was deleted for another reason, given in
	// Reason: the pull request closed, or got the label no-preview.
	StateStopped State = "Stopped"
)

// DescriptionExpired starts the description of the inactive status of an
// expired preview: the resync reads it to leave the preview expired.
const DescriptionExpired = "Expired"

// Report is what the pull request shows of its preview.
type Report struct {
	// Preview is the name of the PreviewEnvironment.
	Preview string
	State   State
	// URL is the address of the preview; empty without a preview domain.
	URL string
	// HeadSHA is the commit the preview runs.
	HeadSHA string
	// ExpiresAt is when the preview expires; nil when unknown.
	ExpiresAt *time.Time
	// Reason says why the preview failed or stopped.
	Reason string
	// Trigger tells which label stops the preview.
	Trigger vesperv1alpha1.Trigger
}

// DeploymentState returns the state of the GitHub deployment status of r,
// and its description.
func (r Report) DeploymentState() (state, description string) {
	switch r.State {
	case StateStarting:
		return "in_progress", "Starting the preview"
	case StateReady:
		return "success", "The preview is ready"
	case StateFailed:
		return "failure", "The preview failed: " + r.Reason
	case StateExpired:
		return "inactive", DescriptionExpired + ": push a commit to bring the preview back"
	default:
		return "inactive", "Stopped: " + r.Reason
	}
}

// Marker identifies the comment of a preview, hidden in its body: the
// integration finds its own comment again with it.
func Marker(preview string) string {
	return "<!-- vesper:preview " + preview + " -->"
}

// Comment returns the body of the comment of the preview on its pull
// request, in Markdown. It is written again only when the report changes,
// so it gives the expiry as a date, not as a time left.
func Comment(r Report) string {
	var b strings.Builder
	b.WriteString(Marker(r.Preview) + "\n")

	headings := map[State]string{
		StateStarting: "Your preview is starting",
		StateReady:    "Your preview is ready",
		StateFailed:   "Your preview failed",
		StateExpired:  "Your preview expired",
		StateStopped:  "Your preview was stopped",
	}
	fmt.Fprintf(&b, "### ✦ %s\n\n", headings[r.State])

	url := "—"
	if r.URL != "" && r.State != StateExpired && r.State != StateStopped {
		url = fmt.Sprintf("[Visit preview ↗](%s)", r.URL)
	} else if r.State == StateReady {
		url = "no public URL: `kubectl port-forward`"
	}
	expires := "—"
	if r.ExpiresAt != nil && r.State != StateExpired && r.State != StateStopped {
		expires = r.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	commit := "—"
	if len(r.HeadSHA) >= 7 {
		commit = "`" + r.HeadSHA[:7] + "`"
	}
	b.WriteString("| Preview | Status | URL | Commit | Expires |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	fmt.Fprintf(&b, "| %s | ✦ %s | %s | %s | %s |\n", r.Preview, r.State, url, commit, expires)

	if r.Reason != "" && (r.State == StateFailed || r.State == StateStopped) {
		fmt.Fprintf(&b, "\n%s\n", escape(r.Reason))
	}

	var footer string
	switch {
	case r.State == StateExpired:
		footer = "The TTL passed. Push a commit to bring the preview back."
	case r.Trigger == vesperv1alpha1.TriggerLabel:
		footer = "Updated with each push. Deleted when the pull request closes or the TTL ends. " +
			"Remove the label `" + LabelPreview + "` to stop it."
	default:
		footer = "Updated with each push. Deleted when the pull request closes or the TTL ends. " +
			"Add the label `" + LabelNoPreview + "` to stop it."
	}
	fmt.Fprintf(&b, "\n<sub>%s Previews by [Vesper](https://github.com/eventide-oss/vesper).</sub>\n", footer)
	return b.String()
}

// escape keeps a reason, which may quote an error of the cluster, from
// breaking the Markdown of the comment or hiding text in it.
func escape(s string) string {
	s = truncate(s, 300)
	return strings.NewReplacer("<", "&lt;", ">", "&gt;", "|", "\\|", "\n", " ", "`", "'",
		"[", "\\[", "]", "\\]").Replace(s)
}
