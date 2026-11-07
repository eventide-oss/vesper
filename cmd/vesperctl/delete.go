package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newDeleteCmd(factory clientFactory) *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a preview environment",
		Long: "Delete a preview environment, after a confirmation. The operator then\n" +
			"deletes its preview namespace and everything in it, then the preview.\n\n" +
			"Without --yes, vesperctl asks on stderr and reads the answer on stdin:\n" +
			"y or yes deletes, anything else does not.",
		Example: "  vesperctl delete pr-42\n" +
			"  vesperctl delete pr-42 -n team-a --yes",
		Args: nameArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDelete(cmd, factory, args[0], yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "delete without asking for confirmation")
	return cmd
}

func runDelete(cmd *cobra.Command, factory clientFactory, name string, yes bool) error {
	client, err := factory.DynamicClient()
	if err != nil {
		return err
	}
	namespace, err := factory.Namespace()
	if err != nil {
		return err
	}
	previews := client.Resource(previewEnvironmentGVR).Namespace(namespace)

	item, err := previews.Get(cmd.Context(), name, metav1.GetOptions{})
	if err != nil {
		return getError(err, namespace, name)
	}
	if item.GetDeletionTimestamp() != nil {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "preview environment %q is already being deleted\n", name)
		return err
	}

	if !yes {
		question := fmt.Sprintf("Delete preview environment %q in namespace %q, "+
			"with everything in its preview namespace? [y/N] ", name, namespace)
		if err := confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), question); err != nil {
			return err
		}
	}

	// The UID precondition makes the API server refuse the deletion (409) if
	// the preview was deleted and created again since it was read: only the
	// preview the user confirmed is deleted.
	uid := item.GetUID()
	err = previews.Delete(cmd.Context(), name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if err != nil {
		return deleteError(err, namespace, name)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"preview environment %q is being deleted: the operator deletes its namespace, then the preview\n", name)
	return err
}

// confirm writes question to w and reads one line from r. It returns nil if
// the answer is y or yes, in any case, and an error otherwise.
func confirm(r io.Reader, w io.Writer, question string) error {
	if _, err := io.WriteString(w, question); err != nil {
		return err
	}
	answer, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read the answer: %w", err)
	}
	if errors.Is(err, io.EOF) && answer == "" {
		// Nothing to read, as in a script: a newline ends the question.
		_, _ = io.WriteString(w, "\n")
		return errors.New("no answer: not deleted; pass --yes to delete without confirmation")
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return nil
	}
	return errors.New("not deleted")
}

// deleteError explains the errors of the deletion a user can act on.
func deleteError(err error, namespace, name string) error {
	switch {
	case apierrors.IsNotFound(err):
		return getError(err, namespace, name)
	case apierrors.IsConflict(err):
		return fmt.Errorf("preview environment %q in namespace %q changed since it was read: not deleted; "+
			"run the command again", name, namespace)
	}
	return apiError(err, fmt.Sprintf("delete preview environment %q in namespace %q", name, namespace))
}
