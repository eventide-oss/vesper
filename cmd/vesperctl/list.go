package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/eventide-oss/vesper/internal/output"
)

// previewEnvironmentGVR identifies the PreviewEnvironment custom resource in
// the API. The dynamic client only needs this to read it, so the CLI works
// before the Go types of the API are generated.
var previewEnvironmentGVR = schema.GroupVersionResource{
	Group:    "vesper.eventide.dev",
	Version:  "v1alpha1",
	Resource: "previewenvironments",
}

func newListCmd(factory clientFactory) *cobra.Command {
	var (
		allNamespaces bool
		format        output.Format
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List preview environments",
		Long: "List preview environments in the current namespace, or in every\n" +
			"namespace with --all-namespaces.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd, factory, allNamespaces, format)
		},
	}
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false,
		"list preview environments in every namespace (overrides --namespace)")
	output.AddFlag(cmd, &format)
	return cmd
}

func runList(cmd *cobra.Command, factory clientFactory, allNamespaces bool, format output.Format) error {
	client, err := factory.DynamicClient()
	if err != nil {
		return err
	}

	namespace := metav1.NamespaceAll
	if !allNamespaces {
		if namespace, err = factory.Namespace(); err != nil {
			return err
		}
	}

	items, err := client.Resource(previewEnvironmentGVR).Namespace(namespace).
		List(cmd.Context(), metav1.ListOptions{})
	if err != nil {
		return listError(err, namespace)
	}
	list := newPreviewList(items.Items)

	if format != output.Table {
		// Scripts always get a valid document, even an empty one.
		return output.Write(cmd.OutOrStdout(), format, list)
	}
	if len(list.Items) == 0 {
		// The message goes to stderr so that stdout stays empty for scripts.
		_, err := fmt.Fprintln(cmd.ErrOrStderr(), noPreviewsMessage(namespace))
		return err
	}
	return printPreviewTable(cmd.OutOrStdout(), list.Items, time.Now())
}

// listError explains the errors a user can act on.
func listError(err error, namespace string) error {
	if apierrors.IsNotFound(err) {
		// Listing a missing resource type returns 404: the CRD is not installed.
		return errCRDNotInstalled
	}
	if namespace == metav1.NamespaceAll {
		return apiError(err, "list preview environments in all namespaces")
	}
	return apiError(err, fmt.Sprintf("list preview environments in namespace %q", namespace))
}

func noPreviewsMessage(namespace string) string {
	if namespace == metav1.NamespaceAll {
		return "No preview environments found."
	}
	return fmt.Sprintf("No preview environments found in namespace %q.", namespace)
}

// printPreviewTable writes one row per preview environment, in the order of
// items. now is the reference time for the AGE column.
func printPreviewTable(w io.Writer, items []previewSummary, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAMESPACE\tNAME\tPHASE\tAGE")
	for _, item := range items {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			item.Namespace, item.Name, orNone(item.Phase), age(item.CreatedAt, now))
	}
	// tabwriter buffers everything: write errors surface here.
	return tw.Flush()
}

// none is printed for a missing value, as kubectl does.
const none = "<none>"

func orNone(s string) string {
	if s == "" {
		return none
	}
	return s
}

// age returns the time elapsed since t, formatted like kubectl.
func age(t *time.Time, now time.Time) string {
	if t == nil {
		return "<unknown>"
	}
	return duration.HumanDuration(now.Sub(*t))
}
