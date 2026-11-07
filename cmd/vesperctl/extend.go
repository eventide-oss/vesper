package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/util/retry"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

func newExtendCmd(factory clientFactory) *cobra.Command {
	var by time.Duration

	cmd := &cobra.Command{
		Use:   "extend <name> --by <duration>",
		Short: "Extend the life of a preview environment",
		Long: "Extend a preview environment by raising its ttl. The ttl counts from the\n" +
			"creation of the preview, and cannot exceed 168h: a preview lives 7 days\n" +
			"at most, extensions included. The operator then moves its expiry.",
		Example: "  vesperctl extend pr-42 --by 2h\n" +
			"  vesperctl extend pr-42 --by 90m -n team-a",
		Args: nameArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("by") {
				return &usageError{errors.New("--by is required")}
			}
			if by <= 0 {
				return &usageError{fmt.Errorf("--by must be a positive duration, got %s", by)}
			}
			return runExtend(cmd, factory, args[0], by)
		},
	}
	cmd.Flags().DurationVar(&by, "by", 0, "how much longer the preview lives, such as 2h or 90m (required)")
	return cmd
}

func runExtend(cmd *cobra.Command, factory clientFactory, name string, by time.Duration) error {
	client, err := factory.DynamicClient()
	if err != nil {
		return err
	}
	namespace, err := factory.Namespace()
	if err != nil {
		return err
	}
	previews := client.Resource(previewEnvironmentGVR).Namespace(namespace)

	// Someone else may change the preview between the read and the patch.
	// The patch carries the resourceVersion that was read, so the API server
	// refuses it (409 Conflict) if the preview changed: it is then read and
	// extended again, and no other extension is lost.
	//
	// The errors of the read and of the checks are explained where they
	// happen; patchErr tells apart the error of the patch, explained after.
	var ttl time.Duration
	var created time.Time
	var patchErr error
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		item, err := previews.Get(cmd.Context(), name, metav1.GetOptions{})
		if err != nil {
			return getError(err, namespace, name)
		}
		if ttl, err = extendedTTL(item, by); err != nil {
			return err
		}
		created = item.GetCreationTimestamp().Time
		patchErr = patchTTL(cmd, previews, item, ttl)
		return patchErr
	})
	if err != nil && err == patchErr {
		return extendError(err, namespace, name)
	}
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(cmd.OutOrStdout(), "preview environment %q extended by %s: ttl %s, expires at %s\n",
		name, by, ttl, created.Add(ttl).UTC().Format(time.RFC3339))
	return err
}

// extendedTTL returns the ttl of preview raised by by, or an error if the
// preview is being deleted or the ttl would exceed the maximum.
func extendedTTL(preview *unstructured.Unstructured, by time.Duration) (time.Duration, error) {
	if preview.GetDeletionTimestamp() != nil {
		return 0, fmt.Errorf("preview environment %q is being deleted: it cannot be extended; create it again",
			preview.GetName())
	}
	ttl := vesperv1alpha1.DefaultTTL
	if s, found, err := unstructured.NestedString(preview.Object, "spec", "ttl"); err != nil {
		return 0, fmt.Errorf("read the ttl of preview environment %q: %w", preview.GetName(), err)
	} else if found {
		if ttl, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("read the ttl of preview environment %q: %w", preview.GetName(), err)
		}
	}
	// Compared this way, a huge --by cannot overflow the sum.
	if by > vesperv1alpha1.MaxTTL-ttl {
		return 0, &invalidError{fmt.Errorf("cannot extend %q by %s: its ttl would be %s, over the maximum "+
			"of %s; it can be extended by %s at most", preview.GetName(), by, ttl+by,
			vesperv1alpha1.MaxTTL, vesperv1alpha1.MaxTTL-ttl)}
	}
	return ttl + by, nil
}

// patchTTL sets spec.ttl of preview with a merge patch, on the condition
// that the preview is still at the resourceVersion that was read.
func patchTTL(cmd *cobra.Command, previews dynamic.ResourceInterface, preview *unstructured.Unstructured,
	ttl time.Duration) error {
	patch, err := json.Marshal(map[string]any{
		"metadata": map[string]any{"resourceVersion": preview.GetResourceVersion()},
		"spec":     map[string]any{"ttl": metav1.Duration{Duration: ttl}},
	})
	if err != nil {
		return err
	}
	_, err = previews.Patch(cmd.Context(), preview.GetName(), types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// extendError explains the errors of the patch a user can act on.
func extendError(err error, namespace, name string) error {
	var status apierrors.APIStatus
	switch {
	case apierrors.IsInvalid(err) && errors.As(err, &status):
		return &invalidError{fmt.Errorf("the API server rejected the new ttl: %s", invalidCauses(status.Status()))}
	case apierrors.IsNotFound(err):
		return getError(err, namespace, name)
	case apierrors.IsConflict(err):
		return fmt.Errorf("preview environment %q in namespace %q keeps changing: try again", name, namespace)
	}
	return apiError(err, fmt.Sprintf("extend preview environment %q in namespace %q", name, namespace))
}
