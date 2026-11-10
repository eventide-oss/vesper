// Copyright 2026 The Vesper Authors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	vesperv1alpha1 "github.com/eventide-oss/vesper/api/v1alpha1"
)

// phaseUnknown is the phase label of a preview the operator has not
// reconciled yet: its status has no phase.
const phaseUnknown = "Unknown"

// metricPhases are the values of the phase label, all reported even at zero
// so that a series never disappears from a graph.
var metricPhases = []string{
	string(vesperv1alpha1.PhasePending), string(vesperv1alpha1.PhaseReady),
	string(vesperv1alpha1.PhaseFailed), string(vesperv1alpha1.PhaseExpired), phaseUnknown,
}

// listTimeout bounds the read of the previews during a scrape.
const listTimeout = 5 * time.Second

var (
	previewsActiveDesc = prometheus.NewDesc(
		"vesper_previews_active",
		"Number of preview environments that exist and are not being deleted, by phase.",
		[]string{"phase"}, nil,
	)

	// expiredTotal counts the previews the operator deleted because their
	// ttl passed. Like every counter, it starts at zero when the operator
	// starts.
	expiredTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vesper_previews_expired_total",
		Help: "Number of preview environments deleted because their ttl passed, since the operator started.",
	})
)

func init() {
	// The registry of controller-runtime: its metrics server serves it.
	ctrlmetrics.Registry.MustRegister(expiredTotal)
}

// PreviewCollector reports vesper_previews_active. It counts the previews
// when Prometheus scrapes the metrics, from the cache of the manager: the
// value is always right, after a restart or a missed deletion, and no state
// is kept between scrapes.
type PreviewCollector struct {
	reader client.Reader
}

// NewPreviewCollector returns a collector that lists the previews with
// reader, normally the cache of the manager (mgr.GetCache()).
func NewPreviewCollector(reader client.Reader) *PreviewCollector {
	return &PreviewCollector{reader: reader}
}

// RegisterMetrics adds the collector of reader to the registry that the
// metrics server of controller-runtime serves.
func RegisterMetrics(reader client.Reader) error {
	return ctrlmetrics.Registry.Register(NewPreviewCollector(reader))
}

// Describe implements prometheus.Collector.
func (c *PreviewCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- previewsActiveDesc
}

// Collect implements prometheus.Collector. If the previews cannot be read,
// the scrape reports the error instead of wrong values.
func (c *PreviewCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	var list vesperv1alpha1.PreviewEnvironmentList
	if err := c.reader.List(ctx, &list); err != nil {
		ch <- prometheus.NewInvalidMetric(previewsActiveDesc, err)
		return
	}

	counts := make(map[string]int, len(metricPhases))
	for _, p := range metricPhases {
		counts[p] = 0
	}
	for i := range list.Items {
		preview := &list.Items[i]
		if !preview.DeletionTimestamp.IsZero() {
			continue
		}
		phase := string(preview.Status.Phase)
		if _, known := counts[phase]; !known {
			phase = phaseUnknown
		}
		counts[phase]++
	}
	for _, p := range metricPhases {
		ch <- prometheus.MustNewConstMetric(previewsActiveDesc, prometheus.GaugeValue, float64(counts[p]), p)
	}
}
