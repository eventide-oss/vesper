// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8stesting "k8s.io/client-go/testing"
)

const previewUID = types.UID("0b1c2d3e-0000-4000-8000-000000000042")

// newEvent returns an event about the object with uid in namespace team-a,
// as the core Event API serves the events the operator writes through
// events.k8s.io: eventTime set, the deprecated timestamps empty.
func newEvent(name string, uid types.UID, eventType, reason, message string, at time.Time) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Event",
		"metadata":   map[string]any{"namespace": "team-a", "name": name},
		"involvedObject": map[string]any{
			"apiVersion": "vesper.eventide.dev/v1alpha1", "kind": "PreviewEnvironment",
			"namespace": "team-a", "name": "pr-42", "uid": string(uid),
		},
		"type":      eventType,
		"reason":    reason,
		"message":   message,
		"eventTime": at.UTC().Format(time.RFC3339Nano),
	}}
	return obj
}

func previewWithUID(now time.Time) *unstructured.Unstructured {
	p := readyPreview(now)
	p.SetUID(previewUID)
	return p
}

func TestGetShowsEvents(t *testing.T) {
	now := time.Now()
	client := newFakeClient(previewWithUID(now),
		// Written out of order: printed oldest first.
		newEvent("e2", previewUID, "Normal", "Ready", "1/1 replicas available", now.Add(-time.Minute)),
		newEvent("e1", previewUID, "Normal", "Provisioning", "creating the objects", now.Add(-3*time.Minute)),
		// An older preview with the same name, and another object.
		newEvent("old", "11111111-0000-4000-8000-000000000000", "Warning", "Expired", "from a deleted preview", now),
	)

	code, stdout, stderr := executeWith(t, []string{"get", "pr-42", "-n", "team-a"},
		func(opts *globalOptions) clientFactory { return &fakeFactory{opts: opts, client: client} })
	if code != exitOK || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q, want 0 and nothing", code, stderr)
	}

	events := stdout[strings.Index(stdout, "Events:"):]
	if strings.Contains(events, "from a deleted preview") {
		t.Errorf("events of another preview shown:\n%s", events)
	}
	first, second := strings.Index(events, "Provisioning"), strings.Index(events, "Ready")
	if first < 0 || second < 0 || first > second {
		t.Errorf("events =\n%s\nwant Provisioning then Ready", events)
	}
	if !strings.Contains(events, "TYPE") || !strings.Contains(events, "3m") {
		t.Errorf("events =\n%s\nwant a table with the age of each event", events)
	}
}

func TestGetWithoutEvents(t *testing.T) {
	client := newFakeClient(previewWithUID(time.Now()))
	code, stdout, _ := executeWith(t, []string{"get", "pr-42", "-n", "team-a"},
		func(opts *globalOptions) clientFactory { return &fakeFactory{opts: opts, client: client} })
	if code != exitOK || !strings.Contains(stdout, "Events:      <none>\n") {
		t.Errorf("exit code = %d, stdout =\n%s\nwant 0 and Events: <none>", code, stdout)
	}
}

// TestGetEventsForbidden: a user who may read previews but not events still
// gets the preview, and exit code 0.
func TestGetEventsForbidden(t *testing.T) {
	client := newFakeClient(previewWithUID(time.Now()))
	client.PrependReactor("list", "events", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(eventsGVR.GroupResource(), "", errors.New("no"))
	})
	code, stdout, stderr := executeWith(t, []string{"get", "pr-42", "-n", "team-a"},
		func(opts *globalOptions) clientFactory { return &fakeFactory{opts: opts, client: client} })
	if code != exitOK || stderr != "" {
		t.Fatalf("exit code = %d, stderr = %q, want 0 and nothing", code, stderr)
	}
	if !strings.Contains(stdout, "Phase:       Ready") || !strings.Contains(stdout, "<cannot list events: forbidden>") {
		t.Errorf("stdout =\n%s\nwant the preview and a note on the events", stdout)
	}
}

// TestGetJSONHasNoEvents: the JSON output is a stable contract, without
// events.
func TestGetJSONHasNoEvents(t *testing.T) {
	now := time.Now()
	client := newFakeClient(previewWithUID(now), newEvent("e1", previewUID, "Normal", "Ready", "up", now))
	_, stdout, _ := executeWith(t, []string{"get", "pr-42", "-n", "team-a", "-o", "json"},
		func(opts *globalOptions) clientFactory { return &fakeFactory{opts: opts, client: client} })
	if strings.Contains(stdout, "\"up\"") || strings.Contains(strings.ToLower(stdout), "events") {
		t.Errorf("JSON output holds the events:\n%s", stdout)
	}
}

func TestPreviewEventsKeepsTheLatest(t *testing.T) {
	now := time.Now()
	objs := []runtime.Object{previewWithUID(now)}
	for i := range maxEvents + 3 {
		objs = append(objs, newEvent(fmt.Sprintf("e%02d", i), previewUID, "Normal", fmt.Sprintf("R%02d", i), "m",
			now.Add(time.Duration(i)*time.Second)))
	}
	events, err := previewEvents(t.Context(), newFakeClient(objs...), "team-a", previewUID)
	if err != nil {
		t.Fatalf("previewEvents: %v", err)
	}
	if len(events) != maxEvents || events[0].Reason != "R03" || events[maxEvents-1].Reason != "R12" {
		t.Errorf("events = %+v, want the %d latest, R03 to R12", events, maxEvents)
	}
}

func TestEventTimeAndCount(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	e := newEvent("e", previewUID, "Warning", "PodsRejected", "quota", now.Add(-time.Hour))
	e.Object["series"] = map[string]any{"count": int64(4), "lastObservedTime": now.Format(time.RFC3339Nano)}
	got := newEventSummary(*e)
	if !got.Last.Equal(now) || got.Count != 4 {
		t.Errorf("event = %+v, want last %s and count 4", got, now)
	}

	var out bytes.Buffer
	if err := printEvents(&out, []eventSummary{got}, nil, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2m (x4)") {
		t.Errorf("output =\n%s\nwant the age and the count, 2m (x4)", out.String())
	}

	// No time at all: the age is unknown, not 56 years.
	bare := newEventSummary(unstructured.Unstructured{Object: map[string]any{"reason": "X"}})
	out.Reset()
	_ = printEvents(&out, []eventSummary{bare}, nil, now)
	if !strings.Contains(out.String(), "<unknown>") {
		t.Errorf("output =\n%s\nwant <unknown> for an event without time", out.String())
	}
}
