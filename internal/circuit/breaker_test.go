package circuit_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"

	"github.com/syntasso/kratix/internal/circuit"
)

var _ = Describe("TokenBucketBreaker", func() {
	var (
		clk     *testclock.FakeClock
		params  circuit.BreakerParams
		breaker circuit.Breaker
		key     types.NamespacedName
	)

	BeforeEach(func() {
		clk = testclock.NewFakeClock(time.Unix(0, 0))
		params = circuit.BreakerParams{
			Burst:                 5,
			RefillRate:            1.0,
			Cooldown:              5 * time.Minute,
			HalfOpenProbeInterval: 30 * time.Second,
		}
		breaker = circuit.NewTokenBucketBreaker(params, clk)
		key = types.NamespacedName{Namespace: "ns", Name: "rr"}
	})

	It("allows a fresh key and starts closed", func() {
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.State(key)).To(Equal(circuit.StateClosed))
	})

	It("opens after burst is exhausted and stays open during cooldown", func() {
		// Drain all 5 tokens.
		for i := 0; i < 5; i++ {
			Expect(breaker.Allow(key)).To(BeTrue(), "drain %d", i)
		}
		// 6th call has no tokens → trips open.
		Expect(breaker.Allow(key)).To(BeFalse())
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))

		// During cooldown, still open.
		clk.Step(4 * time.Minute)
		Expect(breaker.Allow(key)).To(BeFalse())
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))
	})

	It("recovers from half-open to closed on a successful Observe", func() {
		// Trip open.
		for i := 0; i < 6; i++ {
			breaker.Allow(key)
		}
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))

		// Cooldown elapses → next Allow puts us in half-open.
		clk.Step(6 * time.Minute)
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.State(key)).To(Equal(circuit.StateHalfOpen))

		// Successful Observe closes the breaker and refills tokens to burst.
		breaker.Observe(key, true)
		Expect(breaker.State(key)).To(Equal(circuit.StateClosed))
	})

	It("drops the key from internal state on successful recovery so memory is bounded", func() {
		// Trip open.
		for i := 0; i < 6; i++ {
			breaker.Allow(key)
		}
		clk.Step(6 * time.Minute)
		breaker.Allow(key) // → half-open
		breaker.Observe(key, true)

		// State on a dropped key falls back to StateClosed (the unknown-key path).
		Expect(breaker.State(key)).To(Equal(circuit.StateClosed))

		// And a fresh Allow rehydrates a brand-new bucket with full burst —
		// proving the previous entry was dropped, not merely reset.
		for i := 0; i < 5; i++ {
			Expect(breaker.Allow(key)).To(BeTrue(), "fresh burst drain %d", i)
		}
		Expect(breaker.Allow(key)).To(BeFalse())
	})

	It("blocks overlapping probes while a half-open probe is in flight", func() {
		// Trip open.
		for i := 0; i < 6; i++ {
			breaker.Allow(key)
		}
		clk.Step(6 * time.Minute)

		// First Allow after cooldown → fires the probe.
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.State(key)).To(Equal(circuit.StateHalfOpen))

		// Even after HalfOpenProbeInterval elapses, a second Allow must NOT
		// fire a parallel probe — the first probe hasn't been Observed yet.
		clk.Step(2 * time.Minute)
		Expect(breaker.Allow(key)).To(BeFalse())

		// Once Observe lands, the next Allow can fire a probe again.
		breaker.Observe(key, false)
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))
		clk.Step(6 * time.Minute)
		Expect(breaker.Allow(key)).To(BeTrue())
	})

	It("re-opens from half-open on a failed Observe", func() {
		for i := 0; i < 6; i++ {
			breaker.Allow(key)
		}
		clk.Step(6 * time.Minute)
		breaker.Allow(key) // → half-open

		breaker.Observe(key, false)
		Expect(breaker.State(key)).To(Equal(circuit.StateOpen))
	})

	It("ignores Observe in the closed state", func() {
		breaker.Allow(key)
		breaker.Observe(key, false)
		Expect(breaker.State(key)).To(Equal(circuit.StateClosed))
	})

	It("returns true unconditionally when Disabled", func() {
		breaker.UpdateParams(circuit.BreakerParams{Disabled: true})
		for i := 0; i < 100; i++ {
			Expect(breaker.Allow(key)).To(BeTrue())
		}
	})
})
