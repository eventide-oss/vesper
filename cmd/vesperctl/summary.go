package main

import (
	"cmp"
	"math"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// previewSummary is the view of a PreviewEnvironment that vesperctl prints.
// The table of `list` and the details of `get` are built from it, and
// `-o json` and `-o yaml` print it as is.
//
// Its JSON fields are a stable, documented contract for scripts, independent
// of the CRD version:
//
//   - name, namespace: always present.
//   - runNamespace: the namespace the operator created for the preview, where
//     its application runs; omitted until the operator sets it.
//   - createdAt: creation time, RFC 3339 in UTC.
//   - phase, url: from the status; omitted until the controller sets them.
//   - expiresAt: when the preview is deleted, RFC 3339 in UTC; omitted until
//     the controller sets it, or when it is not a valid RFC 3339 time.
//   - spec: image, port, replicas and ttl, each omitted when unset, and
//     envCount, the number of environment variables. Their names and values
//     are never printed, as they may hold secrets.
//   - conditions: the status conditions in the order the controller set them;
//     omitted when there are none.
//
// Fields may be added in later versions; existing fields keep their meaning.
type previewSummary struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	// RunNamespace is status.namespace.
	RunNamespace string             `json:"runNamespace,omitempty"`
	CreatedAt    *time.Time         `json:"createdAt,omitempty"`
	Phase        string             `json:"phase,omitempty"`
	URL          string             `json:"url,omitempty"`
	ExpiresAt    *time.Time         `json:"expiresAt,omitempty"`
	Spec         previewSpecSummary `json:"spec"`
	Conditions   []conditionSummary `json:"conditions,omitempty"`
}

type previewSpecSummary struct {
	Image    string `json:"image,omitempty"`
	Port     *int64 `json:"port,omitempty"`
	Replicas *int64 `json:"replicas,omitempty"`
	TTL      string `json:"ttl,omitempty"`
	EnvCount int    `json:"envCount"`
}

// conditionSummary mirrors a metav1.Condition, without observedGeneration.
type conditionSummary struct {
	Type               string     `json:"type"`
	Status             string     `json:"status"`
	Reason             string     `json:"reason,omitempty"`
	Message            string     `json:"message,omitempty"`
	LastTransitionTime *time.Time `json:"lastTransitionTime,omitempty"`
}

// previewList is what `list -o json|yaml` prints. Items is never nil, so an
// empty list prints [] rather than null.
type previewList struct {
	Items []previewSummary `json:"items"`
}

// newPreviewList summarizes items, sorted by namespace then name.
func newPreviewList(items []unstructured.Unstructured) previewList {
	list := previewList{Items: make([]previewSummary, 0, len(items))}
	for _, item := range items {
		list.Items = append(list.Items, newPreviewSummary(item))
	}
	slices.SortFunc(list.Items, func(a, b previewSummary) int {
		return cmp.Or(cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})
	return list
}

// newPreviewSummary reads item. It is the only place that knows where each
// field lives in the object, and it never fails: a missing or malformed field
// is left empty.
func newPreviewSummary(item unstructured.Unstructured) previewSummary {
	s := previewSummary{
		Name:         item.GetName(),
		Namespace:    item.GetNamespace(),
		RunNamespace: stringField(item.Object, "status", "namespace"),
		Phase:        stringField(item.Object, "status", "phase"),
		URL:          stringField(item.Object, "status", "url"),
		ExpiresAt:    timeField(item.Object, "status", "expiresAt"),
		Spec: previewSpecSummary{
			Image:    stringField(item.Object, "spec", "image"),
			Port:     intField(item.Object, "spec", "port"),
			Replicas: intField(item.Object, "spec", "replicas"),
			TTL:      stringField(item.Object, "spec", "ttl"),
		},
	}
	if created := item.GetCreationTimestamp(); !created.IsZero() {
		t := created.UTC()
		s.CreatedAt = &t
	}
	if env, _, _ := unstructured.NestedSlice(item.Object, "spec", "env"); env != nil {
		s.Spec.EnvCount = len(env)
	}

	conditions, _, _ := unstructured.NestedSlice(item.Object, "status", "conditions")
	for _, c := range conditions {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		s.Conditions = append(s.Conditions, conditionSummary{
			Type:               stringField(m, "type"),
			Status:             stringField(m, "status"),
			Reason:             stringField(m, "reason"),
			Message:            stringField(m, "message"),
			LastTransitionTime: timeField(m, "lastTransitionTime"),
		})
	}
	return s
}

func stringField(obj map[string]any, fields ...string) string {
	v, _, _ := unstructured.NestedString(obj, fields...)
	return v
}

// timeField parses an RFC 3339 time, and returns nil when the field is
// missing or malformed.
func timeField(obj map[string]any, fields ...string) *time.Time {
	t, err := time.Parse(time.RFC3339, stringField(obj, fields...))
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}

// intField returns a whole number. Numbers decoded from JSON may be int64 or
// float64, so both are accepted; anything else returns nil.
func intField(obj map[string]any, fields ...string) *int64 {
	v, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found {
		return nil
	}
	switch n := v.(type) {
	case int64:
		return &n
	case float64:
		if n == math.Trunc(n) && math.Abs(n) <= math.MaxInt64 {
			i := int64(n)
			return &i
		}
	}
	return nil
}
