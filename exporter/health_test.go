package exporter

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var metric dto.Metric
	if err := g.Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetGauge().GetValue()
}

func TestCollectionHealth(t *testing.T) {
	h := NewCollectionHealth("health-test:502")
	other := NewCollectionHealth("other-test:502")
	assert := func(success, timestamp float64) {
		t.Helper()
		if got := gaugeValue(t, h.success); got != success {
			t.Fatalf("success = %v, want %v", got, success)
		}
		if got := gaugeValue(t, h.lastSuccess); got != timestamp {
			t.Fatalf("last success = %v, want %v", got, timestamp)
		}
	}
	assert(0, 0)
	h.Observe(false, time.Unix(100, 0))
	assert(0, 0)
	h.Observe(true, time.Unix(200, 500000000))
	assert(1, 200.5)
	h.Observe(false, time.Unix(300, 0))
	assert(0, 200.5)
	h.Observe(true, time.Unix(400, 0))
	assert(1, 400)
	if gaugeValue(t, other.success) != 0 || gaugeValue(t, other.lastSuccess) != 0 {
		t.Fatal("health leaked between inverters")
	}
}
