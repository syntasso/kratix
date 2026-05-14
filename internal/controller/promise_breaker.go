package controller

import (
	"strconv"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"

	"github.com/syntasso/kratix/api/v1alpha1"
	"github.com/syntasso/kratix/internal/circuit"
	"github.com/syntasso/kratix/internal/logging"
)

const (
	AnnotationCircuitBreakerBurst    = "kratix.io/circuit-breaker-burst"
	AnnotationCircuitBreakerRefill   = "kratix.io/circuit-breaker-refill"
	AnnotationCircuitBreakerCooldown = "kratix.io/circuit-breaker-cooldown"
	AnnotationCircuitBreakerDisabled = "kratix.io/circuit-breaker-disabled"
)

// BreakerDefaults are the operator-level defaults applied to every Promise's
// breaker. Annotations on a Promise override individual fields.
type BreakerDefaults struct {
	Burst                 float64
	RefillRate            float64
	Cooldown              time.Duration
	HalfOpenProbeInterval time.Duration
	Enabled               bool
}

// ResolveBreakerParams resolves the effective BreakerParams for a Promise.
// Returns the params plus a list of human-readable warning strings for
// annotations that failed to parse.
func ResolveBreakerParams(promise *v1alpha1.Promise, defaults BreakerDefaults) (circuit.BreakerParams, []string) {
	params := circuit.BreakerParams{
		Burst:                 defaults.Burst,
		RefillRate:            defaults.RefillRate,
		Cooldown:              defaults.Cooldown,
		HalfOpenProbeInterval: defaults.HalfOpenProbeInterval,
		Disabled:              !defaults.Enabled,
	}
	var warnings []string

	ann := promise.GetAnnotations()
	if ann == nil {
		return params, nil
	}

	if v, ok := ann[AnnotationCircuitBreakerBurst]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			params.Burst = f
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerBurst+": invalid value "+v+" (want positive float)")
		}
	}
	if v, ok := ann[AnnotationCircuitBreakerRefill]; ok {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			params.RefillRate = f
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerRefill+": invalid value "+v+" (want positive float)")
		}
	}
	if v, ok := ann[AnnotationCircuitBreakerCooldown]; ok {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			params.Cooldown = d
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerCooldown+": invalid duration "+v)
		}
	}
	if v, ok := ann[AnnotationCircuitBreakerDisabled]; ok {
		if b, err := strconv.ParseBool(v); err == nil {
			params.Disabled = b
		} else {
			warnings = append(warnings, AnnotationCircuitBreakerDisabled+": invalid bool "+v)
		}
	}

	return params, warnings
}

// emitBreakerWarnings records each warning on the Promise as a Warning Event
// and logs it. Safe to call with a nil recorder (logs only).
func emitBreakerWarnings(recorder record.EventRecorder, log logr.Logger, promise *v1alpha1.Promise, warnings []string) {
	for _, w := range warnings {
		logging.Info(log, "promise breaker annotation warning", "promise", promise.GetName(), "warning", w)
		if recorder != nil {
			recorder.Event(promise, corev1.EventTypeWarning, "CircuitBreakerAnnotationInvalid", w)
		}
	}
}
