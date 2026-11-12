// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// queueSize bounds the pull requests waiting to enter the queue. Past it,
// a webhook is dropped and logged: the resync catches up with it.
const queueSize = 1024

// repositoryName matches <owner>/<name> as GitHub allows it: payloads are
// signed, but checked before they name anything.
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?/[A-Za-z0-9._-]{1,100}$`)

// Queue feeds the controller of the integration: webhooks and the resync
// send pull requests to it, through a channel the controller reads.
type Queue struct {
	events chan event.TypedGenericEvent[PullRequestKey]
	log    logr.Logger
}

// NewQueue returns an empty Queue.
func NewQueue(log logr.Logger) *Queue {
	return &Queue{events: make(chan event.TypedGenericEvent[PullRequestKey], queueSize), log: log}
}

// Events is the channel the controller reads.
func (q *Queue) Events() <-chan event.TypedGenericEvent[PullRequestKey] { return q.events }

// Enqueue adds a pull request, without waiting: a full queue drops it.
func (q *Queue) Enqueue(key PullRequestKey) {
	select {
	case q.events <- event.TypedGenericEvent[PullRequestKey]{Object: key}:
	default:
		q.log.Info("queue full: dropped, the resync will catch up", "repository", key.Repository,
			"pullRequest", key.Number)
	}
}

// The parts of the payloads the integration reads: which pull requests an
// event is about, never their content (ADR-0007).
type repositoryPayload struct {
	FullName string `json:"full_name"`
}

type pullRequestPayload struct {
	Number     int               `json:"number"`
	Repository repositoryPayload `json:"repository"`
}

type workflowRunPayload struct {
	Repository  repositoryPayload `json:"repository"`
	WorkflowRun struct {
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"workflow_run"`
}

// Dispatch queues the pull requests an authenticated delivery is about. A
// workflow run lists the pull requests of its commit, except for forks:
// then the whole repository is looked at again.
func (q *Queue) Dispatch(eventName string, payload []byte) {
	switch eventName {
	case "pull_request":
		var p pullRequestPayload
		if json.Unmarshal(payload, &p) != nil || !repositoryName.MatchString(p.Repository.FullName) || p.Number <= 0 {
			q.log.Info("ignored a pull_request delivery without a valid repository and number")
			return
		}
		q.Enqueue(NewPullRequestKey(p.Repository.FullName, p.Number))
	case "workflow_run":
		var p workflowRunPayload
		if json.Unmarshal(payload, &p) != nil || !repositoryName.MatchString(p.Repository.FullName) {
			q.log.Info("ignored a workflow_run delivery without a valid repository")
			return
		}
		queued := 0
		for _, pr := range p.WorkflowRun.PullRequests {
			if pr.Number > 0 {
				q.Enqueue(NewPullRequestKey(p.Repository.FullName, pr.Number))
				queued++
			}
		}
		if queued == 0 {
			q.Enqueue(NewPullRequestKey(p.Repository.FullName, 0))
		}
	}
}

// Resync queues every repository of the PreviewSources at start, then every
// interval: it catches what lost webhooks missed (ADR-0007). It is a
// Runnable of the manager.
type Resync struct {
	Client   client.Reader
	Queue    *Queue
	Interval time.Duration
}

// Start resyncs until ctx is done.
func (r *Resync) Start(ctx context.Context) error {
	log := logr.FromContextOrDiscard(ctx).WithName("resync")
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		var sources vesperv1alpha1.PreviewSourceList
		if err := r.Client.List(ctx, &sources); err != nil {
			log.Error(err, "list the PreviewSources")
		}
		for _, s := range sources.Items {
			r.Queue.Enqueue(NewPullRequestKey(s.Spec.Repository, 0))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
