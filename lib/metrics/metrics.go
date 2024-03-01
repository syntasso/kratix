package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	PromiseReconciles = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "promise_reconciles",
			Help: "The total number of reconciles for Promise resources",
		},
	)
)
