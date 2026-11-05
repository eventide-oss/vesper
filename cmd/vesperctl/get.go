package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/util/validation"
)

// none is printed for a missing value, as kubectl does.
const none = "<none>"

func newGetCmd(factory clientFactory) *cobra.Command {
	return &cobra.Command{
		Use:   "get <name>",
		Short: "Show the details of a preview environment",
		Long: "Show the spec summary, status, conditions and expiry of one preview\n" +
			"environment in the current namespace.",
		Args: nameArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(cmd, factory, args[0])
		},
	}
}

// nameArg requires exactly one argument that is a valid object name, and
// marks any violation as a usage error. Checking the name locally gives a
// clear message instead of an API server error.
func nameArg(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return &usageError{err}
	}
	if msgs := validation.IsDNS1123Subdomain(args[0]); len(msgs) > 0 {
		return &usageError{fmt.Errorf("invalid name %q: %s", args[0], strings.Join(msgs, "; "))}
	}
	return nil
}

func runGet(cmd *cobra.Command, factory clientFactory, name string) error {
	client, err := factory.DynamicClient()
	if err != nil {
		return err
	}
	namespace, err := factory.Namespace()
	if err != nil {
		return err
	}

	item, err := client.Resource(previewEnvironmentGVR).Namespace(namespace).
		Get(cmd.Context(), name, metav1.GetOptions{})
	if err != nil {
		return getError(err, namespace, name)
	}
	return printPreviewDetails(cmd.OutOrStdout(), *item, time.Now())
}

// getError explains the errors a user can act on. Both a missing object and
// a missing CRD answer 404; only the first one names the object in the
// status details.
func getError(err error, namespace, name string) error {
	var status apierrors.APIStatus
	if apierrors.IsNotFound(err) && errors.As(err, &status) {
		if details := status.Status().Details; details != nil && details.Name == name {
			return fmt.Errorf("preview environment %q not found in namespace %q", name, namespace)
		}
		return listError(err)
	}
	return fmt.Errorf("get preview environment %q: %w", name, err)
}

// printPreviewDetails writes a kubectl describe-like view of item. now is the
// reference time for relative durations.
//
// spec.env may hold secrets, so only the number of variables is printed.
func printPreviewDetails(w io.Writer, item unstructured.Unstructured, now time.Time) error {
	// The labels are fixed, so a fixed width aligns the values. The first
	// write error is kept and returned.
	var err error
	line := func(format string, args ...any) {
		if err == nil {
			_, err = fmt.Fprintf(w, format, args...)
		}
	}
	field := func(label, value string) { line("%-13s%s\n", label+":", value) }

	field("Name", item.GetName())
	field("Namespace", item.GetNamespace())
	field("Created", createdAt(item, now))
	field("Phase", phase(item))
	field("URL", stringField(item, "status", "url"))
	field("Expires", expiresAt(item, now))
	line("Spec:\n")
	field("  Image", stringField(item, "spec", "image"))
	field("  Port", scalarField(item, "spec", "port"))
	field("  Replicas", scalarField(item, "spec", "replicas"))
	field("  TTL", stringField(item, "spec", "ttl"))
	field("  Env", envSummary(item))
	if err != nil {
		return err
	}
	return printConditions(w, item, now)
}

func stringField(item unstructured.Unstructured, fields ...string) string {
	v, found, err := unstructured.NestedString(item.Object, fields...)
	if err != nil || !found || v == "" {
		return none
	}
	return v
}

// scalarField formats a number or a boolean. Numbers decoded from JSON may be
// int64 or float64, so the value is not converted to a fixed type.
func scalarField(item unstructured.Unstructured, fields ...string) string {
	v, found, err := unstructured.NestedFieldNoCopy(item.Object, fields...)
	if err != nil || !found {
		return none
	}
	switch v.(type) {
	case int64, float64, bool:
		return fmt.Sprint(v)
	}
	return none
}

func envSummary(item unstructured.Unstructured) string {
	env, found, err := unstructured.NestedSlice(item.Object, "spec", "env")
	if err != nil || !found || len(env) == 0 {
		return none
	}
	if len(env) == 1 {
		return "1 variable"
	}
	return fmt.Sprintf("%d variables", len(env))
}

func createdAt(item unstructured.Unstructured, now time.Time) string {
	created := item.GetCreationTimestamp()
	if created.IsZero() {
		return "<unknown>"
	}
	return fmt.Sprintf("%s (%s ago)", created.UTC().Format(time.RFC3339),
		duration.HumanDuration(now.Sub(created.Time)))
}

// expiresAt formats status.expiresAt with the time left, or the time since
// expiry while the controller has not deleted the preview yet.
func expiresAt(item unstructured.Unstructured, now time.Time) string {
	raw := stringField(item, "status", "expiresAt")
	if raw == none {
		return none
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	stamp := t.UTC().Format(time.RFC3339)
	if left := t.Sub(now); left > 0 {
		return fmt.Sprintf("%s (in %s)", stamp, duration.HumanDuration(left))
	}
	return fmt.Sprintf("%s (expired %s ago)", stamp, duration.HumanDuration(now.Sub(t)))
}

// printConditions writes status.conditions as a table, in the order the
// controller set them.
func printConditions(w io.Writer, item unstructured.Unstructured, now time.Time) error {
	conditions, found, err := unstructured.NestedSlice(item.Object, "status", "conditions")
	if err != nil || !found || len(conditions) == 0 {
		_, err := fmt.Fprintf(w, "Conditions:  %s\n", none)
		return err
	}

	_, _ = fmt.Fprintln(w, "Conditions:")
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TYPE\tSTATUS\tREASON\tAGE\tMESSAGE")
	for _, c := range conditions {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		cond := unstructured.Unstructured{Object: m}
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			stringField(cond, "type"), stringField(cond, "status"),
			stringField(cond, "reason"), conditionAge(cond, now),
			stringField(cond, "message"))
	}
	return tw.Flush()
}

func conditionAge(cond unstructured.Unstructured, now time.Time) string {
	t, err := time.Parse(time.RFC3339, stringField(cond, "lastTransitionTime"))
	if err != nil {
		return "<unknown>"
	}
	return duration.HumanDuration(now.Sub(t))
}
