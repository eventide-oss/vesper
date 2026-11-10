// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// LimitsName is the name of the LimitRange and of the ResourceQuota of a
// preview, in the preview namespace.
const LimitsName = "preview"

// Resources bounds what a preview may use. Its containers get the requests
// and limits below, through a LimitRange, and the namespace a ResourceQuota.
type Resources struct {
	// CPURequest and MemoryRequest are reserved for each container.
	CPURequest, MemoryRequest resource.Quantity
	// CPULimit and MemoryLimit are the most each container may use.
	CPULimit, MemoryLimit resource.Quantity
	// QuotaCPU and QuotaMemory bound the sum of the limits of the pods of the
	// namespace, and QuotaPods their number.
	QuotaCPU, QuotaMemory resource.Quantity
	QuotaPods             int64
}

// DefaultResources returns the default bounds: enough for the largest
// preview, 5 replicas, during a rolling update, when the Deployment runs up
// to 7 pods (maxSurge of 25%, rounded up).
func DefaultResources() Resources {
	return Resources{
		CPURequest:    resource.MustParse("100m"),
		MemoryRequest: resource.MustParse("128Mi"),
		CPULimit:      resource.MustParse("500m"),
		MemoryLimit:   resource.MustParse("512Mi"),
		QuotaCPU:      resource.MustParse("4"),
		QuotaMemory:   resource.MustParse("4Gi"),
		QuotaPods:     10,
	}
}

// Validate reports values the API server would reject, or that would let no
// pod start: each value must be positive, each request at most its limit, and
// each limit at most the quota.
func (res Resources) Validate() error {
	var errs []error
	for _, q := range []struct {
		name  string
		value resource.Quantity
	}{
		{"cpu request", res.CPURequest}, {"memory request", res.MemoryRequest},
		{"cpu limit", res.CPULimit}, {"memory limit", res.MemoryLimit},
		{"cpu quota", res.QuotaCPU}, {"memory quota", res.QuotaMemory},
	} {
		if q.value.Sign() <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive, got %s", q.name, q.value.String()))
		}
	}
	if res.QuotaPods < 1 {
		errs = append(errs, fmt.Errorf("pod quota must be at least 1, got %d", res.QuotaPods))
	}
	for _, pair := range []struct {
		small, big           string
		smallValue, bigValue resource.Quantity
	}{
		{"cpu request", "cpu limit", res.CPURequest, res.CPULimit},
		{"memory request", "memory limit", res.MemoryRequest, res.MemoryLimit},
		{"cpu limit", "cpu quota", res.CPULimit, res.QuotaCPU},
		{"memory limit", "memory quota", res.MemoryLimit, res.QuotaMemory},
	} {
		if pair.smallValue.Cmp(pair.bigValue) > 0 {
			errs = append(errs, fmt.Errorf("%s %s is more than the %s %s",
				pair.small, pair.smallValue.String(), pair.big, pair.bigValue.String()))
		}
	}
	return errors.Join(errs...)
}

// resources returns the bounds of the reconciler, or the defaults.
func (r *PreviewEnvironmentReconciler) resources() Resources {
	if r.Resources == nil {
		return DefaultResources()
	}
	return *r.Resources
}

// ensureLimits bounds the resources of the preview: a LimitRange gives its
// containers their requests and limits, and a ResourceQuota bounds the whole
// namespace. Both are admission checks of the API server, applied when a pod
// is created: they must exist before the Deployment, and the LimitRange
// before the quota, which rejects pods without limits.
func (r *PreviewEnvironmentReconciler) ensureLimits(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, namespace string,
) error {
	res := r.resources()
	lr := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: LimitsName, Namespace: namespace}}
	if err := r.ensureOwned(ctx, preview, lr, "limitrange", func() { mutateLimitRange(lr, preview, res) }); err != nil {
		return err
	}
	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: LimitsName, Namespace: namespace}}
	return r.ensureOwned(ctx, preview, quota, "resourcequota", func() { mutateResourceQuota(quota, preview, res) })
}

// ensureOwned creates obj or updates it with mutate, unless it exists and
// Vesper did not create it for the preview.
func (r *PreviewEnvironmentReconciler) ensureOwned(
	ctx context.Context, preview *vesperv1alpha1.PreviewEnvironment, obj client.Object, kind string, mutate func(),
) error {
	result, err := controllerutil.CreateOrUpdate(ctx, r.Client, obj, func() error {
		if obj.GetResourceVersion() != "" && !ownedBy(obj, preview) { // it already exists
			return fmt.Errorf("%s %s/%s exists and was not created by Vesper for this preview",
				kind, obj.GetNamespace(), obj.GetName())
		}
		mutate()
		return nil
	})
	if err != nil {
		return fmt.Errorf("create or update %s %s/%s: %w", kind, obj.GetNamespace(), obj.GetName(), err)
	}
	if result != controllerutil.OperationResultNone {
		logf.FromContext(ctx).Info(kind+" "+string(result), "previewNamespace", obj.GetNamespace())
	}
	return nil
}

func mutateLimitRange(lr *corev1.LimitRange, preview *vesperv1alpha1.PreviewEnvironment, res Resources) {
	setPreviewLabels(lr, preview)
	lr.Spec.Limits = []corev1.LimitRangeItem{{
		Type: corev1.LimitTypeContainer,
		DefaultRequest: corev1.ResourceList{
			corev1.ResourceCPU:    res.CPURequest,
			corev1.ResourceMemory: res.MemoryRequest,
		},
		Default: corev1.ResourceList{
			corev1.ResourceCPU:    res.CPULimit,
			corev1.ResourceMemory: res.MemoryLimit,
		},
	}}
}

func mutateResourceQuota(q *corev1.ResourceQuota, preview *vesperv1alpha1.PreviewEnvironment, res Resources) {
	setPreviewLabels(q, preview)
	q.Spec.Hard = corev1.ResourceList{
		corev1.ResourceLimitsCPU:    res.QuotaCPU,
		corev1.ResourceLimitsMemory: res.QuotaMemory,
		corev1.ResourcePods:         *resource.NewQuantity(res.QuotaPods, resource.DecimalSI),
		// A preview is reached through its Ingress only: no Service may open
		// a port on the nodes or ask for a cloud load balancer.
		corev1.ResourceServicesNodePorts:     resource.MustParse("0"),
		corev1.ResourceServicesLoadBalancers: resource.MustParse("0"),
	}
}
