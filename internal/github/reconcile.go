// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// waitForDeletion is how long the reconciler waits before it looks again at
// a preview being deleted, to create it again.
const waitForDeletion = 5 * time.Second

// PullRequestKey is the request of the queue of the integration: a pull
// request of a repository. Number 0 asks for the whole repository: its open
// pull requests and its previews, as the resync does (ADR-0007).
type PullRequestKey struct {
	// Repository is <owner>/<name>, in lowercase: GitHub compares it so.
	Repository string
	Number     int
}

// NewPullRequestKey returns the key of a pull request of repository.
func NewPullRequestKey(repository string, number int) PullRequestKey {
	return PullRequestKey{Repository: strings.ToLower(repository), Number: number}
}

// Reconciler turns pull requests into previews: it reads the pull request
// from the API, decides its preview, and creates, updates or deletes the
// PreviewEnvironment. The operator does the rest.
type Reconciler struct {
	// Client reads the PreviewSources and writes the PreviewEnvironments.
	Client client.Client
	// API reads the pull requests and the runs of their workflows.
	API API
	// Enqueue adds a pull request to the queue, for a repository resync.
	Enqueue func(PullRequestKey)
	// Now tells the time; time.Now if nil.
	Now func() time.Time
}

// Reconcile brings the preview of a pull request, or of every pull request
// of a repository for number 0, in line with GitHub.
func (r *Reconciler) Reconcile(ctx context.Context, key PullRequestKey) (reconcile.Result, error) {
	log := logr.FromContextOrDiscard(ctx).WithValues("repository", key.Repository, "pullRequest", key.Number)

	var sources vesperv1alpha1.PreviewSourceList
	if err := r.Client.List(ctx, &sources); err != nil {
		return reconcile.Result{}, fmt.Errorf("list the PreviewSources: %w", err)
	}
	source, err := SourceFor(sources.Items, key.Repository)
	if err != nil {
		// Nothing to do until an administrator changes the sources: a change
		// of a source does not requeue, the next webhook or resync will.
		log.Info("no preview", "reason", err.Error())
		return reconcile.Result{}, nil
	}
	log = log.WithValues("source", source.Name)

	if key.Number == 0 {
		return reconcile.Result{}, r.resync(ctx, log, source, key.Repository)
	}
	return r.reconcilePullRequest(ctx, log, source, key)
}

// resync queues the open pull requests of a repository, and the pull
// requests of its previews, which may have closed without a webhook.
func (r *Reconciler) resync(ctx context.Context, log logr.Logger, source *vesperv1alpha1.PreviewSource,
	repository string) error {
	open, err := r.API.OpenPullRequests(ctx, repository)
	if err != nil {
		return err
	}
	numbers := map[int]bool{}
	for _, pr := range open {
		numbers[pr.Number] = true
	}
	var previews vesperv1alpha1.PreviewEnvironmentList
	if err := r.Client.List(ctx, &previews, client.InNamespace(source.Spec.Namespace),
		client.MatchingLabels{LabelSource: source.Name}); err != nil {
		return fmt.Errorf("list the previews of %s: %w", source.Name, err)
	}
	for _, p := range previews.Items {
		if n, err := strconv.Atoi(p.Labels[LabelPullRequest]); err == nil && n > 0 {
			numbers[n] = true
		}
	}
	for n := range numbers {
		r.Enqueue(NewPullRequestKey(repository, n))
	}
	log.V(1).Info("resynced", "openPullRequests", len(open), "previews", len(previews.Items))
	return nil
}

// reconcilePullRequest brings the preview of a pull request in line with
// GitHub, then shows its state on the pull request.
func (r *Reconciler) reconcilePullRequest(ctx context.Context, log logr.Logger,
	source *vesperv1alpha1.PreviewSource, key PullRequestKey) (reconcile.Result, error) {
	var decision Decision
	var pr PullRequest
	result, err := r.apply(ctx, log, source, key, &pr, &decision)
	if err != nil || decision.Action == "" {
		return result, err
	}
	if err := r.report(ctx, log, source, key, pr, decision); err != nil {
		return result, err
	}
	return result, nil
}

// apply creates, updates or deletes the preview of a pull request; it sets
// pr to the pull request and decision to what it decided, empty if it did
// not get to decide.
func (r *Reconciler) apply(ctx context.Context, log logr.Logger,
	source *vesperv1alpha1.PreviewSource, key PullRequestKey, prOut *PullRequest, decisionOut *Decision) (reconcile.Result, error) {
	pr, err := r.API.PullRequest(ctx, key.Repository, key.Number)
	switch {
	case IsNotFound(err):
		// Deleted, or no longer reachable by the App: no preview.
		pr = PullRequest{Number: key.Number}
	case err != nil:
		return reconcile.Result{}, err
	}

	name := types.NamespacedName{Namespace: source.Spec.Namespace, Name: PreviewName(source.Name, key.Number)}
	current := &vesperv1alpha1.PreviewEnvironment{}
	if err := r.Client.Get(ctx, name, current); apierrors.IsNotFound(err) {
		current = nil
	} else if err != nil {
		return reconcile.Result{}, fmt.Errorf("get preview %s: %w", name, err)
	} else if current.Labels[LabelSource] != source.Name {
		// Created by someone else: never touch it.
		log.Info("no preview: a PreviewEnvironment of the same name exists, not created by the integration",
			"preview", name.String())
		return reconcile.Result{}, nil
	}
	deleting := current != nil && !current.DeletionTimestamp.IsZero()
	live := current
	if deleting {
		live = nil
	}

	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	decision := Decide(source, pr, source.Spec.BuildWorkflow == "", live, now())
	if decision.Action == ActionKeep && source.Spec.BuildWorkflow != "" {
		built, err := r.API.WorkflowSucceeded(ctx, key.Repository, source.Spec.BuildWorkflow, pr.HeadSHA)
		if err != nil {
			return reconcile.Result{}, err
		}
		decision = Decide(source, pr, built, live, now())
	}
	*prOut, *decisionOut = pr, decision
	log = log.WithValues("action", decision.Action, "reason", decision.Reason)

	switch decision.Action {
	case ActionKeep:
		log.V(1).Info("preview kept")
		return reconcile.Result{}, nil
	case ActionNone:
		if current == nil || deleting {
			log.V(1).Info("no preview")
			return reconcile.Result{}, nil
		}
		log.Info("deleting the preview")
		return reconcile.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, current))
	case ActionReplace:
		if !deleting {
			log.Info("replacing the preview")
			if err := r.Client.Delete(ctx, current); client.IgnoreNotFound(err) != nil {
				return reconcile.Result{}, err
			}
		}
		return reconcile.Result{RequeueAfter: waitForDeletion}, nil
	}

	// ActionApply.
	if deleting {
		// Created again once the old one is gone.
		return reconcile.Result{RequeueAfter: waitForDeletion}, nil
	}
	if current == nil {
		// An expired preview stays expired until a new commit: GitHub keeps
		// the record, as the inactive status of its deployment (ADR-0007).
		latest, err := r.API.LatestDeployment(ctx, key.Repository, decision.Preview.Name, pr.HeadSHA)
		if err != nil {
			return reconcile.Result{}, err
		}
		if latest.State == "inactive" && strings.HasPrefix(latest.Description, DescriptionExpired) {
			log.V(1).Info("preview expired: a new commit brings it back")
			*decisionOut = Decision{Action: ActionKeep, Reason: "expired"}
			return reconcile.Result{}, nil
		}
		log.Info("creating the preview", "image", decision.Preview.Spec.Image)
		if err := r.Client.Create(ctx, decision.Preview); err != nil {
			if apierrors.IsNotFound(err) {
				return reconcile.Result{}, fmt.Errorf("create preview %s: does the namespace %s of the PreviewSource %s exist? %w",
					name, source.Spec.Namespace, source.Name, err)
			}
			return reconcile.Result{}, fmt.Errorf("create preview %s: %w", name, err)
		}
		return reconcile.Result{}, nil
	}
	if !changed(current, decision.Preview) {
		log.V(1).Info("preview up to date")
		return reconcile.Result{}, nil
	}
	log.Info("updating the preview", "image", decision.Preview.Spec.Image, "ttl", decision.Preview.Spec.TTL.Duration.String())
	current.Spec = decision.Preview.Spec
	if current.Labels == nil {
		current.Labels = map[string]string{}
	}
	maps.Copy(current.Labels, decision.Preview.Labels)
	if current.Annotations == nil {
		current.Annotations = map[string]string{}
	}
	maps.Copy(current.Annotations, decision.Preview.Annotations)
	if err := r.Client.Update(ctx, current); err != nil {
		return reconcile.Result{}, fmt.Errorf("update preview %s: %w", name, err)
	}
	return reconcile.Result{}, nil
}

// changed reports whether current differs from desired in what the
// integration sets: the spec, its labels and its annotations.
func changed(current, desired *vesperv1alpha1.PreviewEnvironment) bool {
	if !equality.Semantic.DeepEqual(current.Spec, desired.Spec) {
		return true
	}
	for k, v := range desired.Labels {
		if current.Labels[k] != v {
			return true
		}
	}
	for k, v := range desired.Annotations {
		if current.Annotations[k] != v {
			return true
		}
	}
	return false
}
