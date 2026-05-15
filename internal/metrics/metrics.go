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

// CircuitBreakerState is the per-resource breaker state gauge. Only emitted
// when the breaker is open (1) or half-open (2); closed entries are explicitly
// deleted to keep cardinality bounded at "currently misbehaving resources".
var CircuitBreakerState = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "kratix_circuit_breaker_state",
		Help: "State of the per-resource circuit breaker. 1=open, 2=half-open. Closed breakers are deleted from the series to bound cardinality.",
	},
	[]string{"promise", "resource"},
)

// CircuitBreakerTripsTotal counts breaker trips (closed -> open transitions)
// per Promise.
var CircuitBreakerTripsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kratix_circuit_breaker_trips_total",
		Help: "Number of times the per-resource circuit breaker has tripped (closed -> open), per Promise.",
	},
	[]string{"promise"},
)

// DynamicRRWorkqueueDropsTotal counts events that were dropped at the enqueue
// layer (i.e. would have been added to the workqueue but were filtered out by
// the circuit breaker), labelled by Promise and the reason.
//
// Current reason values: "breaker_open".
var DynamicRRWorkqueueDropsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kratix_dynamic_rr_workqueue_drops_total",
		Help: "Events dropped at the dynamic resource-request controller's enqueue layer, per Promise and reason. Distinct from controller-runtime's workqueue_adds_total which only counts adds.",
	},
	[]string{"promise", "reason"},
)

// PromiseRuntimeOptions exposes the currently-effective per-Promise tuning
// fields that controller-runtime's stock metrics don't already cover:
// rate_limit_qps, rate_limit_burst, circuit_breaker_burst,
// circuit_breaker_refill_rate, circuit_breaker_cooldown_seconds,
// circuit_breaker_disabled.
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
		CircuitBreakerState,
		CircuitBreakerTripsTotal,
		DynamicRRWorkqueueDropsTotal,
		PromiseRuntimeOptions,
	)
}
