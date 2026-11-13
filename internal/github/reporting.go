// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// The annotations a preview keeps of what it showed on its pull request, so
// that GitHub is written to only when that changes.
const (
	// AnnotationReported is the last report shown.
	AnnotationReported = "vesper.eventide.dev/reported"
	// AnnotationDeployment is the deployment of the commit the preview runs,
	// as <sha>:<id>.
	AnnotationDeployment = "vesper.eventide.dev/deployment"
	// AnnotationComment is the ID of the comment of the preview.
	AnnotationComment = "vesper.eventide.dev/comment"
)

// report shows the state of the preview of a pull request on it: a status
// of the deployment of its commit, and its comment. It writes to GitHub only
// when the state changes, and never for a pull request that never had a
// preview.
func (r *Reconciler) report(ctx context.Context, log logr.Logger, source *vesperv1alpha1.PreviewSource,
	key PullRequestKey, pr PullRequest, decision Decision) error {
	name := types.NamespacedName{Namespace: source.Spec.Namespace, Name: PreviewName(source.Name, key.Number)}
	preview := &vesperv1alpha1.PreviewEnvironment{}
	if err := r.Client.Get(ctx, name, preview); apierrors.IsNotFound(err) {
		preview = nil
	} else if err != nil {
		return fmt.Errorf("get preview %s: %w", name, err)
	} else if preview.Labels[LabelSource] != source.Name {
		return nil
	}

	rep, ok := newReport(source, pr, preview, decision)
	if !ok {
		return nil
	}
	if preview != nil && preview.Annotations[AnnotationReported] == reportKey(rep) {
		return nil
	}
	state, description := rep.DeploymentState()

	deploymentID, err := r.deploymentOf(ctx, key.Repository, preview, rep, state, description)
	if err != nil || deploymentID == 0 {
		return err
	}
	if err := r.API.SetDeploymentStatus(ctx, key.Repository, deploymentID, state, rep.URL, description); err != nil {
		return err
	}
	var commentID int64
	if preview != nil {
		commentID, _ = strconv.ParseInt(preview.Annotations[AnnotationComment], 10, 64)
	}
	commentID, err = r.API.UpsertComment(ctx, key.Repository, key.Number, commentID, Marker(rep.Preview), Comment(rep))
	if err != nil {
		return err
	}
	log.Info("reported on the pull request", "state", rep.State, "commit", short(rep.HeadSHA))

	if preview == nil {
		return nil
	}
	patch := client.MergeFrom(preview.DeepCopy())
	if preview.Annotations == nil {
		preview.Annotations = map[string]string{}
	}
	preview.Annotations[AnnotationReported] = reportKey(rep)
	preview.Annotations[AnnotationDeployment] = rep.HeadSHA + ":" + strconv.FormatInt(deploymentID, 10)
	preview.Annotations[AnnotationComment] = strconv.FormatInt(commentID, 10)
	return client.IgnoreNotFound(r.Client.Patch(ctx, preview, patch))
}

// newReport says what the pull request must show of preview, the preview
// of pr if it exists; false when there is nothing to show yet, or for a
// pull request without a preview.
func newReport(source *vesperv1alpha1.PreviewSource, pr PullRequest, preview *vesperv1alpha1.PreviewEnvironment,
	decision Decision) (Report, bool) {
	rep := Report{Preview: PreviewName(source.Name, pr.Number), Trigger: source.Spec.Trigger, HeadSHA: pr.HeadSHA}
	if preview == nil {
		// Gone: stopped, if it was ever shown; kept or expired otherwise.
		if decision.Action != ActionNone {
			return rep, false
		}
		rep.State, rep.Reason = StateStopped, decision.Reason
		return rep, true
	}

	// The commit it runs, which may not be the head of the pull request
	// while the image of a new commit is built.
	rep.HeadSHA = preview.Annotations[AnnotationHeadSHA]
	rep.URL = preview.Status.URL
	if preview.Status.ExpiresAt != nil {
		expires := preview.Status.ExpiresAt.Time
		rep.ExpiresAt = &expires
	}
	switch {
	case preview.Status.Phase == vesperv1alpha1.PhaseExpired:
		rep.State = StateExpired
	case !preview.DeletionTimestamp.IsZero():
		rep.State, rep.Reason = StateStopped, decision.Reason
	case preview.Status.Phase == vesperv1alpha1.PhaseReady:
		rep.State = StateReady
	case preview.Status.Phase == vesperv1alpha1.PhaseFailed:
		rep.State = StateFailed
		if c := meta.FindStatusCondition(preview.Status.Conditions, vesperv1alpha1.ConditionReady); c != nil {
			rep.Reason = c.Reason + ": " + c.Message
		}
	default:
		rep.State = StateStarting
	}
	return rep, true
}

// deploymentOf returns the deployment of the commit of the report: the one
// the preview remembers, the latest one GitHub has, or a new one. It returns
// 0 when there is nothing to update: no deployment for a preview that
// stopped or expired, or one that already shows the state.
func (r *Reconciler) deploymentOf(ctx context.Context, repository string, preview *vesperv1alpha1.PreviewEnvironment,
	rep Report, state, description string) (int64, error) {
	if preview != nil {
		if sha, id, ok := strings.Cut(preview.Annotations[AnnotationDeployment], ":"); ok && sha == rep.HeadSHA {
			if n, err := strconv.ParseInt(id, 10, 64); err == nil && n > 0 {
				return n, nil
			}
		}
	}
	latest, err := r.API.LatestDeployment(ctx, repository, rep.Preview, rep.HeadSHA)
	if err != nil {
		return 0, err
	}
	switch {
	case latest.ID != 0 && latest.State == state && latest.Description == truncate(description, maxDescription):
		return 0, nil
	case latest.ID != 0:
		return latest.ID, nil
	case rep.State == StateStopped || rep.State == StateExpired:
		return 0, nil
	}
	return r.API.CreateDeployment(ctx, repository, rep.Preview, rep.HeadSHA)
}

// reportKey sums up a report, to tell whether it changed.
func reportKey(r Report) string {
	expires := ""
	if r.ExpiresAt != nil {
		expires = r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return strings.Join([]string{string(r.State), short(r.HeadSHA), r.URL, expires, truncate(r.Reason, 100)}, "|")
}

// short returns the first 7 characters of a commit.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
