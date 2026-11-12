// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// drain returns what q holds.
func drain(q *Queue) []PullRequestKey {
	var keys []PullRequestKey
	for {
		select {
		case e := <-q.Events():
			keys = append(keys, e.Object)
		default:
			return keys
		}
	}
}

func TestDispatch(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload string
		want    []PullRequestKey
	}{
		{"pull_request", "pull_request",
			`{"action":"synchronize","number":42,"repository":{"full_name":"Acme/App"},"pull_request":{"title":"x"}}`,
			[]PullRequestKey{{"acme/app", 42}}},
		{"workflow_run of two pull requests", "workflow_run",
			`{"repository":{"full_name":"acme/app"},"workflow_run":{"pull_requests":[{"number":1},{"number":2}]}}`,
			[]PullRequestKey{{"acme/app", 1}, {"acme/app", 2}}},
		{"workflow_run of a fork: the whole repository", "workflow_run",
			`{"repository":{"full_name":"acme/app"},"workflow_run":{"pull_requests":[]}}`,
			[]PullRequestKey{{"acme/app", 0}}},
		{"no number", "pull_request", `{"repository":{"full_name":"acme/app"}}`, nil},
		{"crafted repository", "pull_request", `{"number":1,"repository":{"full_name":"../../etc"}}`, nil},
		{"repository with a newline", "pull_request", `{"number":1,"repository":{"full_name":"acme/app\nx"}}`, nil},
		{"not JSON", "pull_request", `not json`, nil},
		{"another event", "push", `{"repository":{"full_name":"acme/app"}}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := NewQueue(logr.Discard())
			q.Dispatch(tt.event, []byte(tt.payload))
			if got := drain(q); !slices.Equal(got, tt.want) {
				t.Errorf("queued %v, want %v", got, tt.want)
			}
		})
	}
}

// TestQueueFull: a full queue drops, without blocking the webhook.
func TestQueueFull(t *testing.T) {
	q := NewQueue(logr.Discard())
	done := make(chan struct{})
	go func() {
		for i := range queueSize + 10 {
			q.Enqueue(PullRequestKey{"acme/app", i + 1})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Enqueue blocked on a full queue")
	}
	if n := len(drain(q)); n != queueSize {
		t.Errorf("%d queued, want %d", n, queueSize)
	}
}

// TestResync queues each source at start, then at each interval.
func TestResync(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := vesperv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	sources := []*vesperv1alpha1.PreviewSource{
		{ObjectMeta: metav1.ObjectMeta{Name: "a"}, Spec: vesperv1alpha1.PreviewSourceSpec{Repository: "Acme/App"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "b"}, Spec: vesperv1alpha1.PreviewSourceSpec{Repository: "acme/web"}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(sources[0], sources[1]).Build()
	q := NewQueue(logr.Discard())
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- (&Resync{Client: c, Queue: q, Interval: 50 * time.Millisecond}).Start(ctx) }()

	var keys []PullRequestKey
	deadline := time.Now().Add(5 * time.Second)
	for len(keys) < 4 && time.Now().Before(deadline) {
		keys = append(keys, drain(q)...)
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-stopped; err != nil {
		t.Errorf("Start = %v", err)
	}
	if len(keys) < 4 {
		t.Fatalf("queued %v, want both repositories twice", keys)
	}
	for _, k := range keys {
		if k.Number != 0 || (k.Repository != "acme/app" && k.Repository != "acme/web") {
			t.Errorf("queued %+v, want a repository, in lowercase, with number 0", k)
		}
	}
}
