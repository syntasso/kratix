package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/syntasso/kratix/internal/metrics"
)

func TestCircuitBreakerState_Set(t *testing.T) {
	metrics.CircuitBreakerState.With(prometheus.Labels{"promise": "p", "resource": "default/rr"}).Set(1)
	got := testutil.ToFloat64(metrics.CircuitBreakerState.With(prometheus.Labels{"promise": "p", "resource": "default/rr"}))
	if got != 1 {
		t.Fatalf("expected 1, got %v", got)
	}
}

func TestCircuitBreakerState_Delete(t *testing.T) {
	labels := prometheus.Labels{"promise": "p", "resource": "default/rr-delete"}
	metrics.CircuitBreakerState.With(labels).Set(1)
	metrics.CircuitBreakerState.Delete(labels)
	// After Delete the series is gone; .With re-creates it at 0.
	got := testutil.ToFloat64(metrics.CircuitBreakerState.With(labels))
	if got != 0 {
		t.Fatalf("expected 0 after Delete, got %v", got)
	}
}

func TestCircuitBreakerTripsTotal_Inc(t *testing.T) {
	metrics.CircuitBreakerTripsTotal.WithLabelValues("p-trips").Inc()
	got := testutil.ToFloat64(metrics.CircuitBreakerTripsTotal.WithLabelValues("p-trips"))
	if got != 1 {
		t.Fatalf("expected 1, got %v", got)
	}
}

func TestDynamicRRWorkqueueDropsTotal_Inc(t *testing.T) {
	metrics.DynamicRRWorkqueueDropsTotal.WithLabelValues("p-drops", "breaker_open").Inc()
	got := testutil.ToFloat64(metrics.DynamicRRWorkqueueDropsTotal.WithLabelValues("p-drops", "breaker_open"))
	if got != 1 {
		t.Fatalf("expected 1, got %v", got)
	}
}

func TestPromiseRuntimeOptions_Set(t *testing.T) {
	metrics.PromiseRuntimeOptions.WithLabelValues("p-opts", "rate_limit_qps").Set(50)
	got := testutil.ToFloat64(metrics.PromiseRuntimeOptions.WithLabelValues("p-opts", "rate_limit_qps"))
	if got != 50 {
		t.Fatalf("expected 50, got %v", got)
	}
}
