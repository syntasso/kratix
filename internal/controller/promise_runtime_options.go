package controller

import (
	"strconv"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/logging"
	"github.com/syntasso/kratix/internal/metrics"
)

const (
	AnnotationMaxConcurrentReconciles = "kratix.io/max-concurrent-reconciles"
	AnnotationRateLimitQPS            = "kratix.io/rate-limit-qps"
	AnnotationRateLimitBurst          = "kratix.io/rate-limit-burst"
)

// PromiseRuntimeOptions is the resolved set of per-Promise tuning knobs.
// It is what eventually ends up applied to controller.Options.
type PromiseRuntimeOptions struct {
	MaxConcurrentReconciles int
	RateLimitQPS            float32
	RateLimitBurst          int
}

// PromiseRuntimeDefaults are the operator-level defaults sourced from CLI
// flags. Annotations on the Promise override individual fields.
type PromiseRuntimeDefaults struct {
	MaxConcurrentReconciles int
	RateLimitQPS            float32
	RateLimitBurst          int
}

// ResolvePromiseRuntimeOptions returns the effective options for a Promise.
// The second return is the list of human-readable warning strings for any
// annotation that failed to parse — caller is expected to log + emit Events.
func ResolvePromiseRuntimeOptions(promise *v1alpha1.Promise, defaults PromiseRuntimeDefaults) (PromiseRuntimeOptions, []string) {
	opts := PromiseRuntimeOptions{
		MaxConcurrentReconciles: defaults.MaxConcurrentReconciles,
		RateLimitQPS:            defaults.RateLimitQPS,
		RateLimitBurst:          defaults.RateLimitBurst,
	}
	var warnings []string

	ann := promise.GetAnnotations()
	if ann == nil {
		return opts, warnings
	}

	if v, ok := ann[AnnotationMaxConcurrentReconciles]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.MaxConcurrentReconciles = n
		} else {
			warnings = append(warnings, AnnotationMaxConcurrentReconciles+": invalid value "+v+" (want positive integer)")
		}
	}
	if v, ok := ann[AnnotationRateLimitQPS]; ok {
		if f, err := strconv.ParseFloat(v, 32); err == nil && f > 0 {
			opts.RateLimitQPS = float32(f)
		} else {
			warnings = append(warnings, AnnotationRateLimitQPS+": invalid value "+v+" (want positive float)")
		}
	}
	if v, ok := ann[AnnotationRateLimitBurst]; ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.RateLimitBurst = n
		} else {
			warnings = append(warnings, AnnotationRateLimitBurst+": invalid value "+v+" (want positive integer)")
		}
	}

	return opts, warnings
}

// emitRuntimeOptionsWarnings records each warning on the Promise as a Warning
// Event and logs it. Safe to call with a nil recorder (logs only).
func emitRuntimeOptionsWarnings(recorder record.EventRecorder, log logr.Logger, promise *v1alpha1.Promise, warnings []string) {
	for _, w := range warnings {
		logging.Info(log, "promise runtime options annotation warning", "promise", promise.GetName(), "warning", w)
		if recorder != nil {
			recorder.Event(promise, corev1.EventTypeWarning, "PromiseRuntimeOptionsAnnotationInvalid", w)
		}
	}
}

// EmitMetric writes the rate-limit subset of these options to
// kratix_promise_runtime_options. MaxConcurrentReconciles is intentionally
// omitted — controller-runtime's stock controller_runtime_max_concurrent_reconciles
// already covers it.
func (o PromiseRuntimeOptions) EmitMetric(promiseName string) {
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "rate_limit_qps").Set(float64(o.RateLimitQPS))
	metrics.PromiseRuntimeOptions.WithLabelValues(promiseName, "rate_limit_burst").Set(float64(o.RateLimitBurst))
}
