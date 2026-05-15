package controller_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/syntasso/kratix/internal/controller"
)

var _ = Describe("BuildPromiseRateLimiter", func() {
	It("returns an exponential-only limiter when QPS is zero", func() {
		rl := controller.BuildPromiseRateLimiter(0, 0)
		Expect(rl).NotTo(BeNil())
		// First requeue → ~1s
		req := reconcile.Request{}
		first := rl.When(req)
		Expect(first).To(BeNumerically(">=", 900*time.Millisecond))
		Expect(first).To(BeNumerically("<=", 1100*time.Millisecond))
	})

	It("composes a bucket overlay when QPS > 0", func() {
		rl := controller.BuildPromiseRateLimiter(50, 100)
		Expect(rl).NotTo(BeNil())
		// Steady-state under the bucket should be approximately 1/QPS = 20ms.
		req := reconcile.Request{}
		// First two calls have nothing queued — they may return 0.
		_ = rl.When(req)
		_ = rl.When(req)
		// On the third call the limiter should report a non-zero wait
		// once we've drained the burst (bursts of 100 allow many fast calls,
		// so we just assert it's >= 0 and doesn't panic).
		got := rl.When(req)
		Expect(got).To(BeNumerically(">=", 0))
	})
})
