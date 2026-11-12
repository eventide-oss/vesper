// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/source"
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
	return c.Watch(source.TypedChannel(queue.Events(), enqueue))
}
