// Package metrics owns the kratix-specific Prometheus instruments that aren't
// already exposed by controller-runtime's stock metric set. It registers
// against controller-runtime's registry so the instruments show up on the
// existing /metrics endpoint.
//
// What's intentionally NOT here (controller-runtime exposes these out of
// the box, labelled by the dynamic controller's name which equals the
// Promise name):
//   - workqueue_depth, workqueue_adds_total, workqueue_retries_total
//   - controller_runtime_reconcile_total{controller,result}
//   - controller_runtime_reconcile_time_seconds{controller}
//   - controller_runtime_active_workers{controller}
//   - controller_runtime_max_concurrent_reconciles{controller}
//
// Naming convention: kratix_<subsystem>_<metric>_<unit>. Labels: low-cardinality,
// operationally-meaningful.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// PromiseRuntimeOptions exposes the currently-effective per-Promise tuning
// fields that controller-runtime's stock metrics don't already cover:
// rate_limit_qps, rate_limit_burst.
//
// MaxConcurrentReconciles is intentionally NOT emitted here — it's already
// covered by stock controller_runtime_max_concurrent_reconciles{controller=<promise>}.
var PromiseRuntimeOptions = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "kratix_promise_runtime_options",
		Help: "Currently-effective per-Promise runtime tuning options not covered by controller-runtime stock metrics.",
	},
	[]string{"promise", "option"},
)

func init() {
	crmetrics.Registry.MustRegister(
		PromiseRuntimeOptions,
	)
}
