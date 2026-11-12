// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// The labels that choose which pull requests get a preview (ADR-0007).
const (
	// LabelPreview starts the preview of a pull request with trigger Label.
	LabelPreview = "preview"
	// LabelNoPreview stops the preview of a pull request with trigger All.
	LabelNoPreview = "no-preview"
)

// The labels and annotations of the previews the integration creates.
const (
	// LabelSource is the name of the PreviewSource of a preview.
	LabelSource = "vesper.eventide.dev/source"
	// LabelPullRequest is the number of the pull request of a preview.
	LabelPullRequest = "vesper.eventide.dev/pull-request"
	// AnnotationRepository is the repository of the pull request.
	AnnotationRepository = "vesper.eventide.dev/repository"
	// AnnotationHeadSHA is the commit the preview runs.
	AnnotationHeadSHA = "vesper.eventide.dev/head-sha"
)

// Action is what to do with the preview of a pull request.
type Action string

const (
	// ActionNone: the pull request has no preview, and must not have one;
	// delete it if it exists.
	ActionNone Action = "None"
	// ActionKeep: leave the preview as it is, if any; for example while
	// the image of a new commit is being built.
	ActionKeep Action = "Keep"
	// ActionApply: create the preview, or update it to Preview.
	ActionApply Action = "Apply"
	// ActionReplace: delete the preview, and create Preview once it is
	// gone. Its ttl would pass the bound of ADR-0004 otherwise.
	ActionReplace Action = "Replace"
)

// Decision is the preview a pull request must have.
type Decision struct {
	Action Action
	// Preview is the preview to create or update to, for ActionApply and
	// ActionReplace.
	Preview *vesperv1alpha1.PreviewEnvironment
	// Reason says why, for the logs.
	Reason string
}

// PreviewName returns the name of the preview of pull request number of the
// source: <source>-pr-<number>. The name of a source has at most 40
// characters, so the name fits in a label value (63).
func PreviewName(source string, number int) string {
	return source + "-pr-" + strconv.Itoa(number)
}

// RenderImage returns the image of the template of a source for a pull
// request and its head commit.
func RenderImage(template string, number int, sha string) string {
	return strings.NewReplacer("{{number}}", strconv.Itoa(number), "{{sha}}", sha).Replace(template)
}

// Decide returns what the preview of pr must be, from its source, whether
// the image of its head commit is built, and its current preview if any
// (nil otherwise; not one being deleted). It reads nothing and changes
// nothing: the reconciler acts on the decision.
func Decide(source *vesperv1alpha1.PreviewSource, pr PullRequest, imageReady bool,
	current *vesperv1alpha1.PreviewEnvironment, now time.Time) Decision {
	if reason := noPreviewReason(source.Spec, pr); reason != "" {
		return Decision{Action: ActionNone, Reason: reason}
	}

	image := RenderImage(source.Spec.Image, pr.Number, pr.HeadSHA)
	if current != nil && current.Spec.Image == image {
		// Up to date: the ttl runs from the commit it runs.
		return Decision{Action: ActionApply, Preview: desired(source, pr, image, current.Spec.TTL), Reason: "up to date"}
	}
	if !imageReady {
		return Decision{Action: ActionKeep, Reason: fmt.Sprintf("waiting for %s to build commit %.7s",
			source.Spec.BuildWorkflow, pr.HeadSHA)}
	}

	ttl := sourceTTL(source)
	if current == nil {
		return Decision{Action: ActionApply, Preview: desired(source, pr, image, ttl), Reason: "new preview"}
	}
	// A new commit: the preview expires one ttl after it. Its ttl counts
	// from its creation (ADR-0004), so it grows by the age of the preview.
	age := now.Sub(current.CreationTimestamp.Time).Round(time.Second)
	if age < 0 {
		age = 0
	}
	// Compared this way, a huge age cannot overflow the sum.
	if age > vesperv1alpha1.MaxTTL-ttl.Duration {
		return Decision{Action: ActionReplace, Preview: desired(source, pr, image, ttl),
			Reason: fmt.Sprintf("new commit %.7s; the preview is %s old, past the bound of %s: replaced",
				pr.HeadSHA, age, vesperv1alpha1.MaxTTL)}
	}
	extended := &metav1.Duration{Duration: age + ttl.Duration}
	return Decision{Action: ActionApply, Preview: desired(source, pr, image, extended),
		Reason: fmt.Sprintf("new commit %.7s", pr.HeadSHA)}
}

// noPreviewReason says why pr gets no preview, or "" if it gets one.
func noPreviewReason(spec vesperv1alpha1.PreviewSourceSpec, pr PullRequest) string {
	switch {
	case !pr.Open:
		return "the pull request is closed"
	case pr.Draft:
		return "the pull request is a draft"
	case pr.Fork:
		// RequireLabel, with a label from someone with write access, comes
		// with #131; until then, no fork gets a preview.
		return "the pull request comes from a fork"
	case spec.Trigger == vesperv1alpha1.TriggerLabel && !pr.HasLabel(LabelPreview):
		return "the pull request has no label " + LabelPreview
	case spec.Trigger != vesperv1alpha1.TriggerLabel && pr.HasLabel(LabelNoPreview):
		return "the pull request has the label " + LabelNoPreview
	}
	return ""
}

// sourceTTL returns the ttl of the previews of source.
func sourceTTL(source *vesperv1alpha1.PreviewSource) *metav1.Duration {
	if source.Spec.Defaults.TTL != nil {
		return source.Spec.Defaults.TTL.DeepCopy()
	}
	return &metav1.Duration{Duration: vesperv1alpha1.DefaultTTL}
}

// desired returns the preview of pr, as its source defines it.
func desired(source *vesperv1alpha1.PreviewSource, pr PullRequest, image string,
	ttl *metav1.Duration) *vesperv1alpha1.PreviewEnvironment {
	d := source.Spec.Defaults
	return &vesperv1alpha1.PreviewEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      PreviewName(source.Name, pr.Number),
			Namespace: source.Spec.Namespace,
			Labels: map[string]string{
				LabelSource:      source.Name,
				LabelPullRequest: strconv.Itoa(pr.Number),
			},
			Annotations: map[string]string{
				AnnotationRepository: source.Spec.Repository,
				AnnotationHeadSHA:    pr.HeadSHA,
			},
		},
		Spec: vesperv1alpha1.PreviewEnvironmentSpec{
			Image:    image,
			Port:     d.Port,
			Replicas: d.Replicas,
			TTL:      ttl.DeepCopy(),
			Env:      append([]vesperv1alpha1.EnvVar(nil), d.Env...),
		},
	}
}

// SourceFor returns the PreviewSource of repository among sources: an error
// if there is none, or more than one, since the integration cannot tell
// which applies (ADR-0007).
func SourceFor(sources []vesperv1alpha1.PreviewSource, repository string) (*vesperv1alpha1.PreviewSource, error) {
	var found []*vesperv1alpha1.PreviewSource
	for i := range sources {
		if strings.EqualFold(sources[i].Spec.Repository, repository) {
			found = append(found, &sources[i])
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no PreviewSource allows the repository %s", repository)
	case 1:
		return found[0], nil
	default:
		names := make([]string, 0, len(found))
		for _, s := range found {
			names = append(names, s.Name)
		}
		return nil, fmt.Errorf("%d PreviewSources allow the repository %s (%s): none applies until one is deleted",
			len(found), repository, strings.Join(names, ", "))
	}
}
