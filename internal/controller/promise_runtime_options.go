package controller

import (
	"strconv"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/circuit"
)

const (
	AnnotationMaxConcurrentReconciles = "kratix.io/max-concurrent-reconciles"
	AnnotationRateLimitQPS            = "kratix.io/rate-limit-qps"
	AnnotationRateLimitBurst          = "kratix.io/rate-limit-burst"
)

// PromiseRuntimeOptions is the resolved set of per-Promise tuning knobs.
// It is what eventually ends up applied to controller.Options and the
// per-resource breaker.
type PromiseRuntimeOptions struct {
	MaxConcurrentReconciles int
	RateLimitQPS            float32
	RateLimitBurst          int
	Breaker                 circuit.BreakerParams
}

// PromiseRuntimeDefaults are the operator-level defaults sourced from CLI
// flags. Annotations on the Promise override individual fields.
type PromiseRuntimeDefaults struct {
	MaxConcurrentReconciles int
	RateLimitQPS            float32
	RateLimitBurst          int
	Breaker                 BreakerDefaults
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
	breakerParams, warnings := ResolveBreakerParams(promise, defaults.Breaker)
	opts.Breaker = breakerParams

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
