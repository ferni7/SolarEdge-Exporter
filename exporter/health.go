package exporter

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	collectionSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "solaredge_collection_success",
		Help: "Whether the latest collection cycle successfully read the inverter, completed meter detection, and read all detected meters (1 = success, 0 = failure or not yet collected).",
	}, labels)
	collectionLastSuccess = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "solaredge_collection_last_success_timestamp_seconds",
		Help: "Unix timestamp of the last successful complete collection cycle; 0 until the first success. Cached measurements may be stale after this time.",
	}, labels)
)

// CollectionHealth tracks a complete collection cycle for one inverter.
// Measurement gauges are retained on failure; consumers must check freshness.
type CollectionHealth struct {
	success     prometheus.Gauge
	lastSuccess prometheus.Gauge
}

// NewCollectionHealth creates both series before any network activity.
func NewCollectionHealth(target string) *CollectionHealth {
	h := &CollectionHealth{
		success:     collectionSuccess.WithLabelValues(target),
		lastSuccess: collectionLastSuccess.WithLabelValues(target),
	}
	h.success.Set(0)
	h.lastSuccess.Set(0)
	return h
}

// Observe records completion, preserving the last-success timestamp on failure.
func (h *CollectionHealth) Observe(success bool, completedAt time.Time) {
	if !success {
		h.success.Set(0)
		return
	}
	h.lastSuccess.Set(float64(completedAt.UnixNano()) / 1e9)
	h.success.Set(1)
}
