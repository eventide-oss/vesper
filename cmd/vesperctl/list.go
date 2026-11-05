package main

import (
	"cmp"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
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
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List preview environments",
		Long: "List preview environments in the current namespace, or in every\n" +
			"namespace with --all-namespaces.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(cmd, factory, allNamespaces)
		},
	}
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false,
		"list preview environments in every namespace (overrides --namespace)")
	return cmd
}

func runList(cmd *cobra.Command, factory clientFactory, allNamespaces bool) error {
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

	list, err := client.Resource(previewEnvironmentGVR).Namespace(namespace).
		List(cmd.Context(), metav1.ListOptions{})
	if err != nil {
		return listError(err)
	}

	if len(list.Items) == 0 {
		// The message goes to stderr so that stdout stays empty for scripts.
		_, err := fmt.Fprintln(cmd.ErrOrStderr(), noPreviewsMessage(namespace))
		return err
	}
	return printPreviewTable(cmd.OutOrStdout(), list.Items, time.Now())
}

// listError explains the errors a user can act on.
func listError(err error) error {
	if apierrors.IsNotFound(err) {
		// Listing a missing resource type returns 404: the CRD is not installed.
		return fmt.Errorf("the PreviewEnvironment API (%s) is not installed in this cluster: "+
			"install Vesper, or check --context", previewEnvironmentGVR.GroupResource())
	}
	return fmt.Errorf("list preview environments: %w", err)
}

func noPreviewsMessage(namespace string) string {
	if namespace == metav1.NamespaceAll {
		return "No preview environments found."
	}
	return fmt.Sprintf("No preview environments found in namespace %q.", namespace)
}

// printPreviewTable writes one row per preview environment, sorted by
// namespace then name. now is the reference time for the AGE column.
func printPreviewTable(w io.Writer, items []unstructured.Unstructured, now time.Time) error {
	items = slices.Clone(items)
	slices.SortFunc(items, func(a, b unstructured.Unstructured) int {
		return cmp.Or(
			cmp.Compare(a.GetNamespace(), b.GetNamespace()),
			cmp.Compare(a.GetName(), b.GetName()),
		)
	})

	tw := tabwriter.NewWriter(w, 0, 0, 3, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAMESPACE\tNAME\tPHASE\tAGE")
	for _, item := range items {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			item.GetNamespace(), item.GetName(), phase(item), age(item, now))
	}
	// tabwriter buffers everything: write errors surface here.
	return tw.Flush()
}

// phase returns status.phase, or "<none>" while the controller has not set
// it yet, as kubectl prints missing values.
func phase(item unstructured.Unstructured) string {
	p, found, err := unstructured.NestedString(item.Object, "status", "phase")
	if err != nil || !found || p == "" {
		return "<none>"
	}
	return p
}

// age returns the time since the object was created, formatted like kubectl.
func age(item unstructured.Unstructured, now time.Time) string {
	created := item.GetCreationTimestamp()
	if created.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(now.Sub(created.Time))
}
