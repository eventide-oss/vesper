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
	"k8s.io/apimachinery/pkg/util/duration"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/eventide-oss/vesper/internal/output"
)

func newGetCmd(factory clientFactory) *cobra.Command {
	var format output.Format

	cmd := &cobra.Command{
		Use:   "get <name>",
		Short: "Show the details of a preview environment",
		Long: "Show the spec summary, status, conditions and expiry of one preview\n" +
			"environment in the current namespace.",
		Args: nameArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(cmd, factory, args[0], format)
		},
	}
	output.AddFlag(cmd.Flags(), &format)
	return cmd
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

func runGet(cmd *cobra.Command, factory clientFactory, name string, format output.Format) error {
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
	summary := newPreviewSummary(*item)

	if format != output.Table {
		return output.Write(cmd.OutOrStdout(), format, summary)
	}
	return printPreviewDetails(cmd.OutOrStdout(), summary, time.Now())
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
		return errCRDNotInstalled
	}
	return apiError(err, fmt.Sprintf("get preview environment %q in namespace %q", name, namespace))
}

// printPreviewDetails writes a kubectl describe-like view of s. now is the
// reference time for relative durations.
func printPreviewDetails(w io.Writer, s previewSummary, now time.Time) error {
	// The labels are fixed, so a fixed width aligns the values. The first
	// write error is kept and returned.
	var err error
	line := func(format string, args ...any) {
		if err == nil {
			_, err = fmt.Fprintf(w, format, args...)
		}
	}
	field := func(label, value string) { line("%-13s%s\n", label+":", value) }

	field("Name", s.Name)
	field("Namespace", s.Namespace)
	field("Created", createdAt(s.CreatedAt, now))
	field("Phase", orNone(s.Phase))
	field("URL", orNone(s.URL))
	field("Expires", expiresAt(s.ExpiresAt, now))
	line("Spec:\n")
	field("  Image", orNone(s.Spec.Image))
	field("  Port", intOrNone(s.Spec.Port))
	field("  Replicas", intOrNone(s.Spec.Replicas))
	field("  TTL", orNone(s.Spec.TTL))
	field("  Env", envSummary(s.Spec.EnvCount))
	if err != nil {
		return err
	}
	return printConditions(w, s.Conditions, now)
}

func intOrNone(n *int64) string {
	if n == nil {
		return none
	}
	return fmt.Sprint(*n)
}

func envSummary(count int) string {
	switch count {
	case 0:
		return none
	case 1:
		return "1 variable"
	}
	return fmt.Sprintf("%d variables", count)
}

func createdAt(t *time.Time, now time.Time) string {
	if t == nil {
		return "<unknown>"
	}
	return fmt.Sprintf("%s (%s ago)", t.Format(time.RFC3339), duration.HumanDuration(now.Sub(*t)))
}

// expiresAt formats the expiry with the time left, or the time since expiry
// while the controller has not deleted the preview yet.
func expiresAt(t *time.Time, now time.Time) string {
	if t == nil {
		return none
	}
	stamp := t.Format(time.RFC3339)
	if left := t.Sub(now); left > 0 {
		return fmt.Sprintf("%s (in %s)", stamp, duration.HumanDuration(left))
	}
	return fmt.Sprintf("%s (expired %s ago)", stamp, duration.HumanDuration(now.Sub(*t)))
}

// printConditions writes the conditions as a table, in the order the
// controller set them.
func printConditions(w io.Writer, conditions []conditionSummary, now time.Time) error {
	if len(conditions) == 0 {
		_, err := fmt.Fprintf(w, "Conditions:  %s\n", none)
		return err
	}

	_, _ = fmt.Fprintln(w, "Conditions:")
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "  TYPE\tSTATUS\tREASON\tAGE\tMESSAGE")
	for _, c := range conditions {
		_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
			orNone(c.Type), orNone(c.Status), orNone(c.Reason),
			age(c.LastTransitionTime, now), orNone(c.Message))
	}
	return tw.Flush()
}
