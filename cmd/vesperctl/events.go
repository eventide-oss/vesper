// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// eventsGVR is the core Event API. Events written through events.k8s.io/v1,
// as the operator does, are served there too, with the object they are about
// in involvedObject.
var eventsGVR = schema.GroupVersionResource{Version: "v1", Resource: "events"}

// maxEvents is how many of the latest events vesperctl get shows, as
// kubectl describe does: enough to follow the last changes.
const maxEvents = 10

// eventSummary is one event of a preview, as vesperctl get prints it.
type eventSummary struct {
	Type, Reason, Message string
	// Last is the last time the event happened: an event repeated by the
	// API server keeps its first time and counts the others.
	Last  time.Time
	Count int64
}

// previewEvents lists the latest events about the preview with uid, oldest
// first. It selects them by UID, so that the events of a deleted preview
// with the same name are left out; the selector is checked again here, as a
// client may not apply it.
func previewEvents(ctx context.Context, client dynamic.Interface, namespace string, uid types.UID) ([]eventSummary, error) {
	list, err := client.Resource(eventsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("involvedObject.uid", string(uid)).String(),
	})
	if err != nil {
		return nil, err
	}

	var events []eventSummary
	for _, item := range list.Items {
		if got, _, _ := unstructured.NestedString(item.Object, "involvedObject", "uid"); got != string(uid) {
			continue
		}
		events = append(events, newEventSummary(item))
	}
	slices.SortStableFunc(events, func(a, b eventSummary) int { return a.Last.Compare(b.Last) })
	if len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	return events, nil
}

func newEventSummary(item unstructured.Unstructured) eventSummary {
	str := func(path ...string) string {
		s, _, _ := unstructured.NestedString(item.Object, path...)
		return s
	}
	e := eventSummary{Type: str("type"), Reason: str("reason"), Message: str("message")}

	// The newest of the times the API sets, depending on how the event was
	// written: the last time of its series, the deprecated last timestamp,
	// the time it happened, and its creation as a last resort.
	for _, path := range [][]string{
		{"series", "lastObservedTime"}, {"lastTimestamp"}, {"eventTime"}, {"metadata", "creationTimestamp"},
	} {
		if t, err := time.Parse(time.RFC3339Nano, str(path...)); err == nil && t.After(e.Last) {
			e.Last = t
		}
	}

	e.Count = 1
	for _, path := range [][]string{{"series", "count"}, {"count"}} {
		if n, ok, _ := unstructured.NestedInt64(item.Object, path...); ok && n > e.Count {
			e.Count = n
		}
	}
	return e
}

// printEvents writes the events as a table, or why they could not be read.
// Reading the events is a bonus: a failure is reported, not returned.
func printEvents(w io.Writer, events []eventSummary, listErr error, now time.Time) error {
	switch {
	case listErr != nil && apierrors.IsForbidden(listErr):
		_, err := fmt.Fprintf(w, "Events:      %s\n", "<cannot list events: forbidden>")
		return err
	case listErr != nil:
		_, err := fmt.Fprintf(w, "Events:      <cannot list events: %v>\n", listErr)
		return err
	case len(events) == 0:
		_, err := fmt.Fprintf(w, "Events:      %s\n", none)
		return err
	}

	_, _ = fmt.Fprintln(w, "Events:")
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TYPE\tREASON\tAGE\tMESSAGE")
	for _, e := range events {
		when := "<unknown>"
		if !e.Last.IsZero() {
			when = age(&e.Last, now)
		}
		if e.Count > 1 {
			when = fmt.Sprintf("%s (x%d)", when, e.Count)
		}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", orNone(e.Type), orNone(e.Reason), when, orNone(e.Message))
	}
	return tw.Flush()
}
