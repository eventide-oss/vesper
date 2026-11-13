// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"strconv"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// maxConcurrentReconciles is how many pull requests are reconciled at once:
// each waits on the GitHub API more than on the cluster.
const maxConcurrentReconciles = 4

// SetupController adds to mgr the controller of the integration: it
// reconciles the pull requests that queue receives, from webhooks and the
// resync, one at a time per pull request, retrying with backoff on errors.
func SetupController(mgr manager.Manager, r *Reconciler, queue *Queue) error {
	c, err := controller.NewTyped("pullrequest", mgr, controller.TypedOptions[PullRequestKey]{
		Reconciler:              r,
		MaxConcurrentReconciles: maxConcurrentReconciles,
	})
	if err != nil {
		return err
	}
	enqueue := handler.TypedFuncs[PullRequestKey, PullRequestKey]{
		GenericFunc: func(_ context.Context, e event.TypedGenericEvent[PullRequestKey],
			q workqueue.TypedRateLimitingInterface[PullRequestKey]) {
			q.Add(e.Object)
		},
	}
	if err := c.Watch(source.TypedChannel(queue.Events(), enqueue)); err != nil {
		return err
	}
	// A change of a preview of the integration, its phase above all, is
	// shown on its pull request (#129).
	return c.Watch(source.TypedKind(mgr.GetCache(), &vesperv1alpha1.PreviewEnvironment{},
		handler.TypedEnqueueRequestsFromMapFunc(pullRequestOf)))
}

// pullRequestOf returns the pull request of a preview the integration
// created, from its labels and annotations; none for other previews.
func pullRequestOf(_ context.Context, p *vesperv1alpha1.PreviewEnvironment) []PullRequestKey {
	number, err := strconv.Atoi(p.Labels[LabelPullRequest])
	repository := p.Annotations[AnnotationRepository]
	if p.Labels[LabelSource] == "" || err != nil || number <= 0 || !repositoryName.MatchString(repository) {
		return nil
	}
	return []PullRequestKey{NewPullRequestKey(repository, number)}
}
