package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/syntasso/kratix/internal/metrics"
)

func TestPromiseRuntimeOptions_Set(t *testing.T) {
	metrics.PromiseRuntimeOptions.WithLabelValues("p-opts", "rate_limit_qps").Set(50)
	got := testutil.ToFloat64(metrics.PromiseRuntimeOptions.WithLabelValues("p-opts", "rate_limit_qps"))
	if got != 50 {
		t.Fatalf("expected 50, got %v", got)
	}
}
