package controller

import (
	"time"

	"golang.org/x/time/rate"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// BuildPromiseRateLimiter constructs a workqueue rate limiter for a single
// dynamic RR controller. The exponential failure limiter (1s → 30s) is always
// on. When qps > 0 the result is the max of the exponential limiter and a
// token-bucket steady-state limiter — matches controller-runtime's default
// composition pattern.
func BuildPromiseRateLimiter(qps float32, burst int) workqueue.TypedRateLimiter[reconcile.Request] {
	exp := workqueue.NewTypedItemExponentialFailureRateLimiter[reconcile.Request](
		1*time.Second, 30*time.Second,
	)
	if qps <= 0 {
		return exp
	}
	if burst <= 0 {
		// Sensible default mirroring controller-runtime: burst = 10 × QPS, capped.
		burst = int(qps) * 10
		if burst < 1 {
			burst = 1
		}
	}
	bucket := &workqueue.TypedBucketRateLimiter[reconcile.Request]{
		Limiter: rate.NewLimiter(rate.Limit(qps), burst),
	}
	return workqueue.NewTypedMaxOfRateLimiter(exp, bucket)
}
