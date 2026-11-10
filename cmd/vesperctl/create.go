// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/yaml"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// createOptions are the flags of `vesperctl create`.
type createOptions struct {
	image          string
	port, replicas int32
	ttl            time.Duration
	env            []string
	dryRun         bool
}

func newCreateCmd(factory clientFactory) *cobra.Command {
	var o createOptions

	cmd := &cobra.Command{
		Use:   "create <name> --image <image>",
		Short: "Create a preview environment",
		Long: "Create a preview environment in the current namespace. The operator then\n" +
			"runs its image in a namespace of its own; follow it with `vesperctl get`.\n\n" +
			"vesperctl checks the form of the flags; the API server checks their\n" +
			"values (port, replicas, ttl, image) and its errors are shown as they are.",
		Example: "  vesperctl create pr-42 --image ghcr.io/acme/app:pr-42\n" +
			"  vesperctl create pr-42 --image ghcr.io/acme/app:pr-42 --port 3000 --ttl 48h --env LOG_LEVEL=debug\n" +
			"  vesperctl create pr-42 --image ghcr.io/acme/app:pr-42 --dry-run > preview.yaml",
		Args: nameArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCreate(cmd, factory, args[0], o)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.image, "image", "",
		"container image to run, pinned with a tag other than latest or a digest (required)")
	f.Int32Var(&o.port, "port", 0, "port the application listens on (default 8080, set by the API server)")
	f.Int32Var(&o.replicas, "replicas", 0, "number of pods to run (default 1, set by the API server)")
	f.DurationVar(&o.ttl, "ttl", 0, "time to live, such as 2h or 90m, from the creation (default 24h)")
	f.StringArrayVar(&o.env, "env", nil, "environment variable of the application, as KEY=VALUE (repeatable)")
	f.BoolVar(&o.dryRun, "dry-run", false, "print the preview environment as YAML instead of creating it")
	return cmd
}

func runCreate(cmd *cobra.Command, factory clientFactory, name string, o createOptions) error {
	namespace, err := factory.Namespace()
	if err != nil {
		return err
	}
	obj, err := newPreviewObject(cmd, namespace, name, o)
	if err != nil {
		return err
	}
	if o.dryRun {
		out, err := yaml.Marshal(obj.Object)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(out)
		return err
	}

	client, err := factory.DynamicClient()
	if err != nil {
		return err
	}
	_, err = client.Resource(previewEnvironmentGVR).Namespace(namespace).
		Create(cmd.Context(), obj, metav1.CreateOptions{})
	if err != nil {
		return createError(err, namespace, name)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(),
		"preview environment %q created in namespace %q\nFollow it with: vesperctl get %s -n %s\n",
		name, namespace, name, namespace)
	return err
}

// newPreviewObject builds the PreviewEnvironment the flags describe. It
// checks their form only; the API server checks their values. Flags that
// were not given stay unset, so that the API server applies its defaults.
func newPreviewObject(cmd *cobra.Command, namespace, name string, o createOptions) (*unstructured.Unstructured, error) {
	flags := cmd.Flags()
	if o.image == "" {
		return nil, &usageError{errors.New("--image is required")}
	}
	spec := vesperv1alpha1.PreviewEnvironmentSpec{Image: o.image}
	// 0 would be dropped from the object and replaced by the default: refuse
	// it rather than ignore it.
	if flags.Changed("port") {
		if o.port <= 0 {
			return nil, &usageError{fmt.Errorf("--port must be a positive number, got %d", o.port)}
		}
		spec.Port = o.port
	}
	if flags.Changed("replicas") {
		if o.replicas <= 0 {
			return nil, &usageError{fmt.Errorf("--replicas must be a positive number, got %d", o.replicas)}
		}
		spec.Replicas = o.replicas
	}
	if flags.Changed("ttl") {
		spec.TTL = &metav1.Duration{Duration: o.ttl}
	}
	for _, kv := range o.env {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, &usageError{fmt.Errorf("--env must be KEY=VALUE, got %q", kv)}
		}
		spec.Env = append(spec.Env, vesperv1alpha1.EnvVar{Name: key, Value: value})
	}

	preview := &vesperv1alpha1.PreviewEnvironment{
		TypeMeta:   metav1.TypeMeta{APIVersion: vesperv1alpha1.GroupVersion.String(), Kind: "PreviewEnvironment"},
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       spec,
	}
	content, err := runtime.DefaultUnstructuredConverter.ToUnstructured(preview)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: content}, nil
}

// createError explains the errors a user can act on.
func createError(err error, namespace, name string) error {
	var status apierrors.APIStatus
	errors.As(err, &status)
	switch {
	case apierrors.IsAlreadyExists(err):
		return fmt.Errorf("preview environment %q already exists in namespace %q: delete it first, "+
			"or choose another name", name, namespace)
	case apierrors.IsInvalid(err) && status != nil:
		return &invalidError{fmt.Errorf("the API server rejected the preview environment: %s",
			invalidCauses(status.Status()))}
	case apierrors.IsNotFound(err) && status != nil:
		// A missing namespace and a missing CRD both answer 404.
		if details := status.Status().Details; details != nil && details.Kind == "namespaces" {
			return fmt.Errorf("namespace %q not found: create it, or choose another with --namespace", namespace)
		}
		return errCRDNotInstalled
	}
	return apiError(err, fmt.Sprintf("create preview environment %q in namespace %q", name, namespace))
}

// invalidCauses lists the field errors of a 422 answer, or its message when
// it has none.
func invalidCauses(s metav1.Status) string {
	if s.Details == nil || len(s.Details.Causes) == 0 {
		return s.Message
	}
	causes := make([]string, 0, len(s.Details.Causes))
	for _, c := range s.Details.Causes {
		if c.Field == "" || strings.HasPrefix(c.Message, c.Field) {
			causes = append(causes, c.Message)
			continue
		}
		causes = append(causes, c.Field+": "+c.Message)
	}
	return strings.Join(causes, "; ")
}
