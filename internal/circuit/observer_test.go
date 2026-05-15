package circuit_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/types"
	testclock "k8s.io/utils/clock/testing"

	"github.com/syntasso/kratix/internal/circuit"
)

type capturedTransition struct {
	Key      types.NamespacedName
	OldState circuit.State
	NewState circuit.State
}

var _ = Describe("StateObserver", func() {
	It("is invoked on every state transition with old/new state and key", func() {
		clk := testclock.NewFakeClock(time.Unix(0, 0))
		params := circuit.BreakerParams{
			Burst:                 2,
			RefillRate:            0.01,
			Cooldown:              time.Minute,
			HalfOpenProbeInterval: 30 * time.Second,
		}
		var events []capturedTransition
		observer := circuit.StateObserverFunc(func(key types.NamespacedName, oldS, newS circuit.State) {
			events = append(events, capturedTransition{key, oldS, newS})
		})

		breaker := circuit.NewTokenBucketBreaker(params, clk).WithObserver(observer)
		key := types.NamespacedName{Namespace: "ns", Name: "rr"}

		// Drain burst, third call trips → expect Closed -> Open
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeFalse())
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateClosed, circuit.StateOpen}))

		// Cooldown elapses → Allow moves to Half-Open
		clk.Step(2 * time.Minute)
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateOpen, circuit.StateHalfOpen}))

		// Successful Observe drops back to Closed
		breaker.Observe(key, true)
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateHalfOpen, circuit.StateClosed}))

		// Trip again, fail in half-open → Half-Open -> Open
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeFalse())
		clk.Step(2 * time.Minute)
		Expect(breaker.Allow(key)).To(BeTrue())
		breaker.Observe(key, false)
		Expect(events).To(ContainElement(capturedTransition{key, circuit.StateHalfOpen, circuit.StateOpen}))
	})

	It("is a no-op when no observer is set", func() {
		clk := testclock.NewFakeClock(time.Unix(0, 0))
		breaker := circuit.NewTokenBucketBreaker(circuit.BreakerParams{
			Burst:                 1,
			RefillRate:            0.01,
			Cooldown:              time.Minute,
			HalfOpenProbeInterval: 30 * time.Second,
		}, clk)
		key := types.NamespacedName{Namespace: "ns", Name: "rr"}
		Expect(breaker.Allow(key)).To(BeTrue())
		Expect(breaker.Allow(key)).To(BeFalse())
	})
})
